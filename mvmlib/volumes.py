"""Persistent volumes: ext4 disks that outlive microVMs (databases, uploads, caches).

* Stored as sparse files in DATA/volumes, attached to a *stopped* VM as an extra Firecracker drive (vdb, vdc ...);
  the guest init mounts them at the path you choose.
* Grow-only resize (file grows, e2fsck + resize2fs), done offline.
* Backups: reflink/sparse copies in DATA/backups with retention, optional schedule and a pre-backup command
  (for example a database dump). A running VM is `sync`ed and briefly paused so the copy is a clean point in time.
* Restore always takes a safety backup first. Replica sets get one volume per replica that survives rolling updates.
* Crash recovery: every start runs `e2fsck -p` on the root disk and on each volume before the guest sees it."""
import os
import re
import threading
import time
import uuid
from pathlib import Path

from . import agent, config as C, db
from .util import (Conflict, NotFound, UserError, age_s, int_in, now, reflink_copy, run, valid_name)

MOUNT_RE = re.compile(r"^/[A-Za-z0-9._/-]{1,200}$")
BAD_EXACT = {"/", "/bin", "/sbin", "/lib", "/lib64", "/usr", "/etc", "/tmp", "/var", "/root", "/boot"}
BAD_PREFIX = ("/proc", "/sys", "/dev", "/mvm", "/run", "/boot", "/etc/mvm")
_locks, _guard = {}, threading.Lock()


def _lock(vid):
    with _guard:
        return _locks.setdefault(vid, threading.RLock())


def path_of(v):
    return C.VOL / f"{v['id']}.ext4"


def valid_mount(m):
    m = str(m or "").strip().rstrip("/") or "/"
    if not MOUNT_RE.match(m) or ".." in m.split("/") or "//" in m or m in BAD_EXACT or any(m == p or m.startswith(p + "/") for p in BAD_PREFIX):
        raise UserError(f"bad mount path {m!r}: use an absolute path such as /var/lib/postgresql/data or /data")
    return m


def get(ref):
    v = db.get("volumes", ref) or db.get("volumes", ref, "name")
    if not v:
        raise NotFound(f"volume {ref}")
    return v


def find(name):
    return db.get("volumes", name, "name")


def _vm(vid):
    return db.get("vms", vid) if vid else None


def _state(vm):
    if not vm:
        return "stopped"
    from . import vmm
    return vmm.vm_state(vm)


def view(v):
    d = dict(v)
    p = path_of(v)
    d["allocated"] = p.stat().st_blocks * 512 if p.exists() else 0
    vm = _vm(v["attached_to"])
    d["vm_name"] = vm["name"] if vm else None
    d["vm_state"] = _state(vm) if vm else None
    d["backups"] = len(db.list_("backups", "WHERE volume_id=?", (v["id"],)))
    return d


def list_volumes():
    return [view(v) for v in db.list_("volumes")]


# ------------------------------------------------------------------ lifecycle
def create(name, size_mb, note="", rset=None, ridx=None, backup_every_h=0, backup_keep=5, pre_cmd=None):
    name = valid_name(name, "volume name")
    size = int_in(size_mb, 16, 1 << 21, "volume size (MiB)")
    if find(name):
        raise Conflict(f"a volume named '{name}' already exists")
    if C.DATA.exists() and size * 1048576 // 20 > __import__("shutil").disk_usage(C.DATA).free:
        raise UserError("not enough free disk space on the host for this volume's metadata")
    vid = uuid.uuid4().hex[:8]
    p = C.VOL / f"{vid}.ext4"
    try:
        with open(p, "wb") as f:
            f.truncate(size * 1048576)
        run(["mke2fs", "-q", "-t", "ext4", "-F", "-m", "0", "-L", f"vol-{name[:10]}", "-E", "lazy_itable_init=1,lazy_journal_init=1", p], timeout=300)
    except Exception:
        p.unlink(missing_ok=True)
        raise
    row = {"id": vid, "name": name, "size_mb": size, "fs": "ext4", "note": (note or "")[:200], "health": "ok", "rset": rset, "ridx": ridx,
           "backup_every_h": int_in(backup_every_h or 0, 0, 24 * 90, "backup interval"), "backup_keep": int_in(backup_keep or 5, 1, 100, "backups to keep"),
           "pre_cmd": (pre_cmd or None), "created": now()}
    db.upsert("volumes", row)
    return view(db.get("volumes", vid))


def update(ref, data):
    v = get(ref)
    f = {}
    if "note" in data:
        f["note"] = str(data["note"] or "")[:200]
    if "backup_every_h" in data:
        f["backup_every_h"] = int_in(data["backup_every_h"] or 0, 0, 24 * 90, "backup interval")
    if "backup_keep" in data:
        f["backup_keep"] = int_in(data["backup_keep"] or 5, 1, 100, "backups to keep")
    if "pre_cmd" in data:
        c = str(data["pre_cmd"] or "").strip()
        if len(c) > 2000:
            raise UserError("pre-backup command too long")
        f["pre_cmd"] = c or None
    if f:
        db.update("volumes", v["id"], f)
    return view(get(v["id"]))


def resize(ref, new_mb):
    """Grow only. Shrinking a filesystem in place is not offered: restore into a smaller new volume instead."""
    v = get(ref)
    new = int_in(new_mb, 16, 1 << 21, "new size (MiB)")
    with _lock(v["id"]):
        v = get(v["id"])
        if new < v["size_mb"]:
            raise UserError("volumes can only grow. To shrink: create a smaller volume and copy the data (or restore a backup into a new, smaller volume).")
        if new == v["size_mb"]:
            return view(v)
        if _state(_vm(v["attached_to"])) in ("running", "paused", "starting"):
            raise Conflict("stop the VM that uses this volume first (a filesystem is resized offline)")
        p = path_of(v)
        with open(p, "r+b") as f:
            f.truncate(new * 1048576)
        run(["e2fsck", "-fp", p], check=False, timeout=900)
        run(["resize2fs", p], timeout=900)
        db.update("volumes", v["id"], {"size_mb": new})
    return view(get(v["id"]))


def delete(ref, force=False):
    v = get(ref)
    if v["attached_to"]:
        raise Conflict(f"volume is attached to VM '{(_vm(v['attached_to']) or {}).get('name', v['attached_to'])}': detach it first")
    for b in db.list_("backups", "WHERE volume_id=?", (v["id"],)):
        _rm_backup_file(b)
        db.delete("backups", b["id"])
    path_of(v).unlink(missing_ok=True)
    db.delete("volumes", v["id"])
    return {"ok": True}


# ------------------------------------------------------------------ attach / detach (VM must be stopped)
def _vm_cfg(vm):
    from . import vmm
    return {**vmm.DEFAULT_CFG, **(vm.get("config") or {})}


def attach(ref, vm_id, mount):
    from . import vmm
    v = get(ref)
    vm = db.get("vms", vm_id) or db.get("vms", vm_id, "name")
    if not vm:
        raise NotFound(f"VM {vm_id}")
    mount = valid_mount(mount)
    with _lock(v["id"]):
        v = get(v["id"])
        if v["attached_to"]:
            raise Conflict("volume is already attached to a VM")
        if vm["status"] not in ("stopped", "error"):
            raise Conflict("stop the VM before attaching a volume (Firecracker attaches disks at boot)")
        cfg = _vm_cfg(vm)
        vols = list(cfg.get("volumes") or [])
        if len(vols) >= C.MAX_VOLUMES_PER_VM:
            raise UserError(f"at most {C.MAX_VOLUMES_PER_VM} volumes per VM")
        if any(x["mount"] == mount for x in vols):
            raise Conflict(f"{mount} is already used by another volume of this VM")
        vols.append({"vol": v["id"], "mount": mount})
        cfg["volumes"] = vols
        db.update("vms", vm["id"], {"config": cfg})
        db.update("volumes", v["id"], {"attached_to": vm["id"], "mount": mount})
    return view(get(v["id"]))


def detach(ref):
    v = get(ref)
    with _lock(v["id"]):
        v = get(v["id"])
        vm = _vm(v["attached_to"])
        if vm:
            if vm["status"] not in ("stopped", "error"):
                raise Conflict("stop the VM before detaching its volume")
            cfg = _vm_cfg(vm)
            cfg["volumes"] = [x for x in cfg.get("volumes", []) if x["vol"] != v["id"]]
            db.update("vms", vm["id"], {"config": cfg})
        db.update("volumes", v["id"], {"attached_to": None, "mount": None})
    return view(get(v["id"]))


def release_vm(vm_id):
    """VM is being deleted: its volumes stay (that is the point), they just become detached."""
    for v in db.list_("volumes", "WHERE attached_to=?", (vm_id,)):
        db.update("volumes", v["id"], {"attached_to": None, "mount": None})


def vm_volumes(vm):
    """Resolved volumes of a VM in device order (vdb, vdc ...)."""
    out = []
    for x in _vm_cfg(vm).get("volumes", []):
        v = db.get("volumes", x["vol"])
        if v:
            out.append({**x, "row": v})
    return out


def ensure_attach(vm_id, spec, rset=None, ridx=None):
    """spec: {name, mount, size_mb?}. Re-use the volume if it exists (data survives), otherwise create it."""
    name = valid_name(spec.get("name"), "volume name")
    v = find(name)
    if not v:
        if not spec.get("size_mb"):
            raise UserError(f"volume '{name}' does not exist: give a size_mb to create it")
        v = create(name, spec["size_mb"], rset=rset, ridx=ridx, backup_every_h=spec.get("backup_every_h") or 0,
                   backup_keep=spec.get("backup_keep") or 5, pre_cmd=spec.get("pre_cmd"))
    return attach(v["id"], vm_id, spec.get("mount"))


# ------------------------------------------------------------------ crash recovery
def fsck_file(path, label):
    """e2fsck -p: replays the journal after a crash and repairs what is safe. Exit >=4 = needs a human."""
    r = run(["e2fsck", "-p", str(path)], check=False, timeout=1800)
    if r.returncode >= 4:
        raise RuntimeError(f"{label}: filesystem needs manual repair (e2fsck exit {r.returncode}): {(r.stdout + r.stderr).strip()[-300:]}")
    return r.returncode


def preflight(v):
    try:
        rc = fsck_file(path_of(v), f"volume '{v['name']}'")
    except RuntimeError:
        db.update("volumes", v["id"], {"health": "needs repair"})
        raise
    db.update("volumes", v["id"], {"health": "repaired" if rc & 1 else "ok"})


def check(ref):
    v = get(ref)
    if _state(_vm(v["attached_to"])) in ("running", "paused", "starting"):
        raise Conflict("stop the VM first (a mounted filesystem cannot be checked safely)")
    r = run(["e2fsck", "-fn", path_of(v)], check=False, timeout=1800)
    ok = r.returncode == 0
    db.update("volumes", v["id"], {"health": "ok" if ok else "needs repair"})
    return {"ok": ok, "exit": r.returncode, "output": (r.stdout + r.stderr)[-2000:]}


def usage(ref):
    """Used/free inside the guest (needs the VM running)."""
    from . import vmm
    v = get(ref)
    vm = _vm(v["attached_to"])
    if not vm or vmm.vm_state(vm) != "running":
        return None
    r = agent.exec_(vmm.layout_of(vm).vsock, f"df -Pk {v['mount']} | tail -n 1", "/", 10)
    f = r["stdout"].split()
    try:
        return {"total": int(f[1]) * 1024, "used": int(f[2]) * 1024, "free": int(f[3]) * 1024, "pct": f[4]}
    except (IndexError, ValueError):
        return None


# ------------------------------------------------------------------ backups
def _bpath(b):
    return C.BKP / b["volume_id"] / f"{b['id']}.ext4"


def _rm_backup_file(b):
    _bpath(b).unlink(missing_ok=True)


def _running_path(v, vm):
    from . import vmm
    if vm and vm.get("jailed"):
        lay = vmm.layout_of(vm)
        for i, x in enumerate(_vm_cfg(vm).get("volumes", [])):
            if x["vol"] == v["id"]:
                jp = lay.host(f"vol{i}.ext4")
                if jp.exists() and not os.path.samefile(jp, path_of(v)):
                    return jp          # copied (not hard-linked) into the jail: the live data is there
    return path_of(v)


def backup(ref, note="", mode="auto", kind="manual"):
    """mode: auto|pause (sync + brief pause = clean point in time) | live (no pause, crash-consistent only)."""
    from . import vmm
    v = get(ref)
    with _lock(v["id"]):
        v = get(v["id"])
        vm = _vm(v["attached_to"])
        state = _state(vm)
        how = "offline (VM stopped)"
        paused_by_us = False
        try:
            if state == "running":
                if v["pre_cmd"]:
                    r = agent.exec_(vmm.layout_of(vm).vsock, v["pre_cmd"], "/", 600)
                    if r["exit_code"] != 0:
                        raise RuntimeError(f"pre-backup command failed ({r['exit_code']}): {(r['stderr'] or r['stdout'])[-300:]}")
                agent.exec_(vmm.layout_of(vm).vsock, "sync", "/", 120)
                if mode == "live":
                    how = "live (crash-consistent, like a power cut)"
                else:
                    vmm.pause_vm(vm["id"])
                    paused_by_us = True
                    how = "paused point-in-time" + (" + pre-backup command" if v["pre_cmd"] else "")
            elif state == "paused":
                how = "paused point-in-time"
            bid = uuid.uuid4().hex[:8]
            d = C.BKP / v["id"]
            d.mkdir(parents=True, exist_ok=True)
            dst = d / f"{bid}.ext4"
            reflink_copy(_running_path(v, vm), dst)
        finally:
            if paused_by_us:
                try:
                    vmm.resume_vm(vm["id"])
                except Exception:  # noqa: BLE001
                    pass
        row = {"id": bid, "volume_id": v["id"], "name": time.strftime("%Y-%m-%d %H:%M:%S"), "kind": kind, "created": now(),
               "size_bytes": dst.stat().st_blocks * 512, "note": (note or "")[:200], "consistent": how}
        db.upsert("backups", row)
        db.update("volumes", v["id"], {"last_backup": now()})
        _retain(v)
    return db.get("backups", bid)


def _retain(v):
    keep = int(v["backup_keep"] or 5)
    auto = [b for b in db.list_("backups", "WHERE volume_id=? AND kind IN ('manual','scheduled')", (v["id"],), order="rowid DESC")]      # insertion order: several backups can share one second
    for b in auto[keep:]:
        _rm_backup_file(b)
        db.delete("backups", b["id"])


def list_backups(ref):
    v = get(ref)
    return db.list_("backups", "WHERE volume_id=?", (v["id"],), order="rowid DESC")


def delete_backup(ref, bid):
    v = get(ref)
    b = db.get("backups", bid)
    if not b or b["volume_id"] != v["id"]:
        raise NotFound(bid)
    _rm_backup_file(b)
    db.delete("backups", bid)
    return {"ok": True}


def restore(ref, bid):
    """Put a backup back over the volume. The VM must be stopped. A safety backup of the current data is taken first."""
    v = get(ref)
    b = db.get("backups", bid)
    if not b or b["volume_id"] != v["id"] or not _bpath(b).exists():
        raise NotFound(f"backup {bid}")
    with _lock(v["id"]):
        v = get(v["id"])
        if _state(_vm(v["attached_to"])) in ("running", "paused", "starting"):
            raise Conflict("stop the VM that uses this volume before restoring")
        safety = backup(v["id"], note=f"automatic safety copy before restoring {b['name']}", kind="pre-restore")
        p = path_of(v)
        tmp = p.with_suffix(".restoring")
        reflink_copy(_bpath(b), tmp)
        os.replace(tmp, p)
        run(["e2fsck", "-fp", p], check=False, timeout=900)
        want = v["size_mb"]
        if p.stat().st_size < want * 1048576:           # the backup was taken before a resize: grow back
            with open(p, "r+b") as f:
                f.truncate(want * 1048576)
            run(["resize2fs", p], timeout=900)
        db.update("volumes", v["id"], {"health": "ok"})
    return {"restored": b["id"], "safety_backup": safety["id"], "volume": view(get(v["id"]))}


def restore_as_new(ref, bid, new_name, size_mb=None):
    v = get(ref)
    b = db.get("backups", bid)
    if not b or b["volume_id"] != v["id"] or not _bpath(b).exists():
        raise NotFound(f"backup {bid}")
    name = valid_name(new_name, "volume name")
    if find(name):
        raise Conflict(f"a volume named '{name}' already exists")
    nid = uuid.uuid4().hex[:8]
    p = C.VOL / f"{nid}.ext4"
    reflink_copy(_bpath(b), p)
    run(["e2fsck", "-fp", p], check=False, timeout=900)
    size = max(int(size_mb or 0), p.stat().st_size // 1048576)
    if size * 1048576 > p.stat().st_size:
        with open(p, "r+b") as f:
            f.truncate(size * 1048576)
        run(["resize2fs", p], timeout=900)
    db.upsert("volumes", {"id": nid, "name": name, "size_mb": size, "fs": "ext4", "note": f"copy of {v['name']} @ {b['name']}", "health": "ok",
                          "backup_every_h": 0, "backup_keep": 5, "created": now()})
    return view(db.get("volumes", nid))


# ------------------------------------------------------------------ scheduler
def schedule_once():
    for v in db.list_("volumes", "WHERE backup_every_h > 0"):
        if v["last_backup"] and age_s(v["last_backup"]) < v["backup_every_h"] * 3600:
            continue
        if _state(_vm(v["attached_to"])) in ("starting",):
            continue
        try:
            backup(v["id"], note="scheduled", kind="scheduled")
        except Exception:  # noqa: BLE001 - try again next round
            db.update("volumes", v["id"], {"last_backup": v["last_backup"]})


def _loop():
    while True:
        time.sleep(60)
        try:
            schedule_once()
        except Exception:  # noqa: BLE001
            pass


def startup():
    threading.Thread(target=_loop, daemon=True, name="volume-backups").start()

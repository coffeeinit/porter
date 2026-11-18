"""VM manager - everything about running microVMs through the Firecracker API.

Two launch modes:
  jailer  (recommended)  the official jailer: chroot + pivot_root, new mount namespace, cgroup v2 limits,
                         privileges dropped to an unprivileged uid, seccomp (Firecracker's own, always on).
  direct                 firecracker started as the web-app user (no root needed; no chroot/cgroups).
Disk images live at rest in DATA/vms/<id>/rootfs.ext4. In jailer mode they are copied into the jail while
the VM runs and moved back when it stops."""
import json
import os
import re
import shlex
import shutil
import signal
import subprocess
import tempfile
import threading
import time
import uuid
from pathlib import Path

from . import agent, config as C, db, host
from .fcapi import FC, FCError, rate_limiter
from .util import (Conflict, NotFound, UserError, age_s, int_in, now, out, reflink_copy, run, slug, tail_file,
                   valid_name, valid_vid, wait_for)

_locks, _locks_guard = {}, threading.Lock()
_procs = {}                      # vid -> Popen of the launcher (sudo/jailer or firecracker)
_ping_cache = {}


def vm_lock(vid):
    with _locks_guard:
        return _locks.setdefault(vid, threading.RLock())


# ------------------------------------------------------------------ layout
class Layout:
    def __init__(self, vid, jailed):
        self.vid, self.jailed = vid, jailed
        base = host.helper_info() or {}
        fcname = Path(base.get("fc", C.FC)).name if jailed else "firecracker"
        self.root = (Path(base.get("chroot_base", C.JAIL_BASE)) / fcname / vid / "root") if jailed else C.RUNTIME / vid
        self.api = self.root / ("run/firecracker.socket" if jailed else "api.sock")
        self.vsock = self.root / "vsock.sock"
        self.disk_rest = C.VMS / vid / "rootfs.ext4"

    def inside(self, name):
        return f"/{name}" if self.jailed else str(self.root / name)

    def host(self, name):
        return self.root / name

    @property
    def disk_inside(self):
        return "/rootfs.ext4" if self.jailed else str(self.disk_rest)

    @property
    def disk_running(self):
        return self.host("rootfs.ext4") if self.jailed else self.disk_rest


def _jailed_now(v):
    return bool(v.get("jailed"))


def layout_of(v):
    return Layout(v["id"], _jailed_now(v))


# ------------------------------------------------------------------ config
DEFAULT_CFG = {"net": True, "entropy": True, "balloon": None, "mmds": None, "cpu_template": "None", "smt": None,
               "track_dirty_pages": False, "huge_pages": "None", "hotplug_mib": 0, "volumes": [], "rx_mbit": 0, "tx_mbit": 0, "disk_mbit": 0,
               "disk_iops": 0, "env": {}, "command": None, "boot_args": "", "metrics": True, "autostart": False}


def normalize_config(c):
    c = {**DEFAULT_CFG, **(c or {})}
    out_ = dict(DEFAULT_CFG)
    out_["net"] = bool(c["net"])
    out_["entropy"] = bool(c["entropy"])
    out_["metrics"] = bool(c["metrics"])
    out_["autostart"] = bool(c["autostart"])
    out_["track_dirty_pages"] = bool(c["track_dirty_pages"])
    vols = []
    for x in c.get("volumes") or []:
        if isinstance(x, dict) and re.fullmatch(r"[a-f0-9]{8}", str(x.get("vol", ""))) and str(x.get("mount", "")).startswith("/"):
            vols.append({"vol": x["vol"], "mount": x["mount"]})
    out_["volumes"] = vols[:C.MAX_VOLUMES_PER_VM]
    ct = str(c["cpu_template"] or "None")
    if ct not in C.CPU_TEMPLATES:
        raise UserError(f"cpu_template must be one of {C.CPU_TEMPLATES}")
    out_["cpu_template"] = ct
    if c["smt"] not in (None, True, False):
        raise UserError("smt must be true, false or unset")
    out_["smt"] = c["smt"]
    if c["huge_pages"] not in ("None", "2M", "Transparent"):
        raise UserError("huge_pages must be None, Transparent or 2M")
    out_["hotplug_mib"] = int_in(c.get("hotplug_mib") or 0, 0, C.MAX_MEM_MIB, "hotplug size")
    if out_["hotplug_mib"] and out_["hotplug_mib"] < 128:
        raise UserError("hotpluggable memory must be at least 128 MiB (or 0 to disable)")
    out_["huge_pages"] = c["huge_pages"]
    for k, hi in (("rx_mbit", 100000), ("tx_mbit", 100000), ("disk_mbit", 100000), ("disk_iops", 10_000_000)):
        out_[k] = int_in(c[k] or 0, 0, hi, k)
    b = c["balloon"]
    if b:
        out_["balloon"] = {"amount_mib": int_in(b.get("amount_mib", 0), 0, C.MAX_MEM_MIB, "balloon amount"),
                           "deflate_on_oom": bool(b.get("deflate_on_oom", True)),
                           "stats_polling_interval_s": int_in(b.get("stats_polling_interval_s", 5), 0, 3600, "balloon stats interval")}
    m = c["mmds"]
    if m:
        data = m.get("data") or {}
        if not isinstance(data, dict):
            raise UserError("mmds data must be a JSON object")
        if len(json.dumps(data)) > 51200:
            raise UserError("mmds data larger than 50 KiB")
        out_["mmds"] = {"data": data}
    env = c["env"] or {}
    if not isinstance(env, dict):
        raise UserError("env must be an object")
    for k, v in env.items():
        if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]{0,63}", str(k)) or "\n" in str(v) or len(str(v)) > 4096:
            raise UserError(f"bad environment variable {k!r}")
    out_["env"] = {str(k): str(v) for k, v in env.items()}
    cmd = c["command"]
    if cmd:
        if not isinstance(cmd, list) or not all(isinstance(x, str) for x in cmd) or len(cmd) > 64:
            raise UserError("command must be a list of strings")
        out_["command"] = cmd
    extra = str(c["boot_args"] or "")
    if extra:
        if len(extra) > 400 or not re.fullmatch(r"[A-Za-z0-9._=:,/@+ -]+", extra):
            raise UserError("extra kernel arguments contain unsupported characters")
        if re.search(r"(^| )(init|root|mvm\.[a-z]+)=", extra):
            raise UserError("init=, root= and mvm.*= are managed by the app")
    out_["boot_args"] = extra
    return out_


def _mac(ip):
    return "AA:FC:" + ":".join(f"{int(x):02X}" for x in ip.split("."))


def boot_args(v, cfg):
    host_name = re.sub(r"[^a-z0-9-]", "-", (v["name"] or v["id"]).lower())[:60].strip("-") or v["id"]
    a = ["console=ttyS0", "reboot=k", "panic=1", "pci=off", "root=/dev/vda", "rw", "init=/mvm-init", "random.trust_cpu=on"]
    if C.MACH == "x86_64":
        a += ["i8042.noaux", "i8042.nomux", "i8042.nopnp", "i8042.dumbkbd"]
    a += [f"mvm.host={host_name}", "mvm.grace=8"]
    if cfg["net"] and v.get("ip"):
        a += [f"mvm.ip={v['ip']}", f"mvm.gw={C.NET}.1", "mvm.mask=24", f"mvm.dns={','.join(C.DNS)}"]
    if cfg["mmds"]:
        a.append("mvm.mmds=1")
    if cfg.get("volumes"):
        a.append("mvm.vols=" + ",".join(f"vd{chr(ord('b') + i)}:{x['mount']}" for i, x in enumerate(cfg["volumes"])))
    if cfg["boot_args"]:
        a.append(cfg["boot_args"])
    return " ".join(a)


# ------------------------------------------------------------------ queries
def _alive(pid):
    if not pid:
        return False
    try:
        os.kill(pid, 0)
        return Path(f"/proc/{pid}/cmdline").read_bytes().split(b"\0")[0].rsplit(b"/", 1)[-1].startswith(b"firecracker")
    except (OSError, IndexError):
        return False


def get_vm(vid):
    v = db.get("vms", valid_vid(vid))
    if not v:
        raise NotFound(vid)
    return v


def agent_sock(vid):
    return str(layout_of(get_vm(vid)).vsock)


def disk_path(vid):
    v = get_vm(vid)
    return Layout(vid, v["jailed"]).disk_rest


def console_tail(vid, n=6000):
    return tail_file(C.VMS / vid / "console.log", n)[-n:]


def reconcile():
    """Sync DB with reality: processes that died, processes that survived an app restart."""
    pids = host.all_fc_pids()
    for v in db.list_("vms"):
        pid = pids.get(v["id"])
        if v["status"] in ("running", "paused", "starting"):
            if pid:
                if v["pid"] != pid:
                    db.update("vms", v["id"], {"pid": pid})
            elif age_s(v.get("started")) > (180 if v["status"] == "starting" else 3):
                lk = vm_lock(v["id"])
                if lk.acquire(blocking=False):          # never interfere with a start/stop in progress
                    try:
                        cur = db.get("vms", v["id"])
                        if cur and cur["status"] in ("running", "paused", "starting") and not host.find_fc_pid(v["id"]):
                            _after_exit(cur, crashed=True)
                    finally:
                        lk.release()
        elif pid and v["status"] == "stopped":
            db.update("vms", v["id"], {"status": "running", "pid": pid})


def vm_state(v):
    """Authoritative status from the Firecracker API for running VMs."""
    if v["status"] not in ("running", "paused", "starting"):
        return v["status"]
    try:
        st = FC(layout_of(v).api, 2).info().get("state", "")
    except Exception:  # noqa: BLE001
        return v["status"]
    return {"Running": "running", "Paused": "paused", "Not started": "starting"}.get(st, v["status"])


def _agent_ok(v):
    c = _ping_cache.get(v["id"])
    if c and time.time() - c[0] < 3:
        return c[1]
    try:
        ok = bool(agent.ping(layout_of(v).vsock, 1))
    except Exception:  # noqa: BLE001
        ok = False
    _ping_cache[v["id"]] = (time.time(), ok)
    return ok


def view(v, light=False):
    d = dict(v)
    cfg = {**DEFAULT_CFG, **(v.get("config") or {})}
    d["config"] = cfg
    d["status"] = vm_state(v)
    d["running"] = d["status"] in ("running", "paused", "starting")
    d["uptime_s"] = age_s(v.get("started")) if d["running"] and v.get("started") else None
    d["agent"] = False if light or d["status"] != "running" else _agent_ok(v)
    img = db.get("images", v["image"], "name") or {}
    d["ports_declared"] = [int(re.match(r"\d+", str(p)).group()) for p in img.get("ports", []) if re.match(r"\d+", str(p))]
    d["forwards"] = db.list_("forwards", "WHERE vm_id=?", (v["id"],))
    if d["running"] and v.get("pid"):
        d["proc"] = host.proc_stats(v["pid"])
    d["snapshots"] = len(db.list_("snapshots", "WHERE vm_id=?", (v["id"],)))
    d["volumes"] = [{"id": x["vol"], "mount": x["mount"], "name": (db.get("volumes", x["vol"]) or {}).get("name"), "size_mb": (db.get("volumes", x["vol"]) or {}).get("size_mb")} for x in cfg.get("volumes", [])]
    return d


def list_vms(include_hidden=False):
    reconcile()
    return [view(v, light=False) for v in db.list_("vms", "" if include_hidden else "WHERE hidden=0")]


# ------------------------------------------------------------------ creation
def _alloc_ip(hint=None):
    used = {v["ip"] for v in db.list_("vms") if v.get("ip")}
    cands = ([hint] if hint else []) + [f"{C.NET}.{i}" for i in range(C.IP_FIRST, C.IP_LAST + 1)]
    for ip in cands:
        if ip not in used:
            return ip
    raise Conflict("no free guest IP addresses left in the subnet")


def _debugfs_write(disk, guest_path, data):
    """Replace a file inside an ext4 image without mounting it (e2fsprogs debugfs, no root needed)."""
    with tempfile.NamedTemporaryFile("wb", delete=False) as t:
        t.write(data if isinstance(data, bytes) else data.encode())
        tmp = t.name
    try:
        cmds = f"rm {guest_path}\nwrite {tmp} {guest_path}\n"
        r = run(["debugfs", "-w", "-f", "-", str(disk)], input=cmds, check=False, timeout=60)
        if "Allocated inode" not in (r.stdout + r.stderr):
            raise RuntimeError(f"debugfs could not write {guest_path}: {(r.stdout + r.stderr).strip()[-200:]}")
    finally:
        os.unlink(tmp)


def _grow(disk, disk_mb, log=lambda m: None):
    cur = Path(disk).stat().st_size // 1048576
    if disk_mb <= cur:
        return cur
    with open(disk, "r+b") as f:
        f.truncate(disk_mb * 1048576)
    run(["e2fsck", "-fp", str(disk)], check=False, timeout=600)
    run(["resize2fs", str(disk)], timeout=600)
    return disk_mb


def create_vm(image, name=None, vcpu=1, mem=512, disk_mb=0, config=None, hidden=False, start=False, rset=None, ridx=None, volumes=None):
    img = db.get("images", image, "name")
    if not img:
        raise NotFound(f"image {image}")
    vcpu = int_in(vcpu, 1, C.MAX_VCPU, "vcpus")
    mem = int_in(mem, 64, C.MAX_MEM_MIB, "memory (MiB)")
    cfg = normalize_config(config)
    if len([v for v in db.list_("vms") if not v["hidden"]]) >= C.MAX_VMS:
        raise Conflict(f"limit of {C.MAX_VMS} VMs reached")
    vid = uuid.uuid4().hex[:8]
    name = valid_name(name or f"vm-{slug(image, 30).lstrip('_')}-{vid[:4]}", "VM name")
    if db.get("vms", name, "name"):
        raise Conflict(f"a VM named '{name}' already exists")
    src = C.IMG / image / "rootfs.ext4"
    if not src.exists():
        raise RuntimeError("image disk is missing on disk - rebuild the image")
    vd = C.VMS / vid
    vd.mkdir(parents=True)
    try:
        disk = vd / "rootfs.ext4"
        reflink_copy(src, disk)
        size_mb = _grow(disk, int_in(disk_mb, 0, 1 << 20, "disk size")) if disk_mb else disk.stat().st_size // 1048576
        env = {**(img.get("env") or {}), **cfg["env"]}
        if cfg["env"]:
            _debugfs_write(disk, "/etc/mvm/env", "".join(f"{k}={v}\n" for k, v in env.items()))
        if cfg["command"]:
            wd = img.get("workdir") or "/"
            _debugfs_write(disk, "/etc/mvm/run", "#!/bin/sh\ncd " + shlex.quote(wd) + " 2>/dev/null || cd /\nexec " + " ".join(shlex.quote(a) for a in cfg["command"]) + "\n")
        ip = _alloc_ip()
        v = {"id": vid, "name": name, "image": image, "ip": ip, "mac": _mac(ip), "tap": f"tap{vid}", "vcpu": vcpu, "mem": mem,
             "disk_mb": size_mb, "status": "stopped", "pid": None, "jailed": 0, "config": cfg, "hidden": int(hidden),
             "created": now(), "autostart": int(cfg["autostart"]), "rset": rset, "ridx": ridx}
        db.upsert("vms", v)
    except Exception:
        shutil.rmtree(vd, ignore_errors=True)
        raise
    if volumes:
        from . import volumes as V
        try:
            for spec in volumes:
                V.ensure_attach(vid, spec)
        except Exception:
            delete_vm(vid)
            raise
    if start:
        start_vm(vid)
    return view(db.get("vms", vid))


# ------------------------------------------------------------------ launch
def _spawn(v, lay, nocg=False):
    console = open(C.VMS / v["id"] / "console.log", "ab")
    console.write(f"\n=== {now()} boot ===\n".encode())
    console.flush()
    if lay.jailed:
        host.jail_rm(v["id"])
        argv = ["sudo", "-n", C.PRIV, "jail-start", v["id"], str(v["vcpu"]), str(v["mem"])]
        if nocg or not C.CGROUPS:
            argv.append("nocg")
        cwd = "/"
    else:
        shutil.rmtree(lay.root, ignore_errors=True)
        lay.root.mkdir(parents=True, mode=0o700)
        argv = [C.FC, "--api-sock", str(lay.api), "--id", v["id"]]
        cwd = str(lay.root)
    proc = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=console, stderr=subprocess.STDOUT, cwd=cwd,
                            start_new_session=True, env={"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
                                                                 **{k: os.environ[k] for k in C.FC_PASSENV if k in os.environ}})
    console.close()
    _procs[v["id"]] = proc
    return proc


def _wait_api(lay, proc, timeout=20):
    def ready():
        if proc.poll() is not None:
            return "exited"
        if lay.api.exists():
            try:
                FC(lay.api, 2).version()
                return "ok"
            except Exception:  # noqa: BLE001
                return None
        return None
    r = wait_for(ready, timeout, 0.1)
    return r == "ok"


def _kill_pid(pid, grace=3.0):
    def send(sig):
        try:
            os.kill(pid, sig)
            return True
        except ProcessLookupError:
            return False
        except PermissionError:
            return False
    if not send(signal.SIGTERM):
        return True
    end = time.time() + grace
    while time.time() < end:
        if not _alive(pid):
            return True
        time.sleep(0.1)
    send(signal.SIGKILL)
    return bool(wait_for(lambda: not _alive(pid), 3, 0.1))


def _launch(v, lay):
    """Start the VMM process and return an FC client once its API answers."""
    proc = _spawn(v, lay)
    ok = _wait_api(lay, proc)
    warn = []
    if not ok and lay.jailed and C.CGROUPS and proc.poll() is not None:
        warn.append("cgroup limits unavailable on this host; started without them")
        proc = _spawn(v, lay, nocg=True)
        ok = _wait_api(lay, proc)
    if not ok:
        tail = console_tail(v["id"], 1500)
        _kill_proc(proc, v["id"])
        raise RuntimeError("firecracker did not start:\n" + tail)
    return FC(lay.api, 20), proc, warn


def _kill_proc(proc, vid):
    pid = host.find_fc_pid(vid)
    if pid:
        _kill_pid(pid)
    if proc and proc.poll() is None:
        try:
            proc.terminate()
            proc.wait(3)
        except Exception:  # noqa: BLE001
            try:
                proc.kill()
            except Exception:  # noqa: BLE001
                pass


def _stage_files(v, lay, extra=()):
    """Jailer mode: put kernel + disk (+ snapshot files) into the jail. Direct mode: nothing to copy."""
    for n in ("fc.log", "metrics.json"):
        lay.host(n).touch(mode=0o600)
    if lay.jailed:
        reflink_copy(C.KERNEL, lay.host("vmlinux"))
        reflink_copy(lay.disk_rest, lay.host("rootfs.ext4"))
        lay.disk_rest.unlink()
        for i, x in enumerate((v.get("config") or {}).get("volumes") or []):
            src, dst = C.VOL / f"{x['vol']}.ext4", lay.host(f"vol{i}.ext4")
            try:
                os.link(src, dst)                      # same inode: no copy, writes land in the persistent file
            except OSError:
                reflink_copy(src, dst)                 # different filesystem: copied in, copied back on stop
        for src, name in extra:
            reflink_copy(src, lay.host(name))


def _configure(fc, v, lay, cfg, warn):
    def soft(path, body, what):
        try:
            fc.put(path, body)
        except FCError as e:
            warn.append(f"{what}: {e.fault}")
    if cfg["metrics"]:
        soft("/metrics", {"metrics_path": lay.inside("metrics.json")}, "metrics")
    soft("/logger", {"log_path": lay.inside("fc.log"), "level": "Info", "show_level": True, "show_log_origin": False}, "logger")
    fc.put("/boot-source", {"kernel_image_path": lay.inside("vmlinux") if lay.jailed else str(C.KERNEL), "boot_args": boot_args(v, cfg)})
    drive = {"drive_id": "rootfs", "path_on_host": lay.disk_inside, "is_root_device": True, "is_read_only": False}
    rl = rate_limiter(cfg["disk_mbit"], cfg["disk_iops"])
    if rl:
        drive["rate_limiter"] = rl
    fc.put("/drives/rootfs", drive)
    for i, x in enumerate(cfg.get("volumes") or []):
        fc.put(f"/drives/vol{i}", {"drive_id": f"vol{i}", "path_on_host": _vol_inside(lay, i, x["vol"]), "is_root_device": False, "is_read_only": False})
    if cfg["net"] and v.get("ip") and host.net_enabled():
        nic = {"iface_id": "eth0", "host_dev_name": v["tap"], "guest_mac": v["mac"]}
        for k, key in (("rx_mbit", "rx_rate_limiter"), ("tx_mbit", "tx_rate_limiter")):
            r = rate_limiter(cfg[k])
            if r:
                nic[key] = r
        fc.put("/network-interfaces/eth0", nic)
    fc.put("/vsock", {"guest_cid": 3, "uds_path": lay.inside("vsock.sock")})
    mc = {"vcpu_count": v["vcpu"], "mem_size_mib": v["mem"], "track_dirty_pages": cfg["track_dirty_pages"]}
    if cfg["smt"] is not None and C.MACH == "x86_64":
        mc["smt"] = bool(cfg["smt"])
    if cfg["cpu_template"] != "None":
        mc["cpu_template"] = cfg["cpu_template"]
    if cfg["huge_pages"] != "None":
        mc["huge_pages"] = cfg["huge_pages"]
    fc.put("/machine-config", mc)
    if cfg["balloon"]:
        fc.put("/balloon", cfg["balloon"])
    if cfg["entropy"]:
        soft("/entropy", {}, "entropy device")
    if cfg["hotplug_mib"]:
        soft("/hotplug/memory", {"total_size_mib": cfg["hotplug_mib"] - cfg["hotplug_mib"] % 128, "slot_size_mib": 128}, "memory hotplug")
    if cfg["mmds"]:
        if cfg["net"] and v.get("ip") and host.net_enabled():
            fc.put("/mmds/config", {"version": "V2", "network_interfaces": ["eth0"], "ipv4_address": "169.254.169.254"})
            fc.put("/mmds", cfg["mmds"]["data"])
        else:
            warn.append("metadata service skipped: it needs a network interface")


def _vol_inside(lay, i, volid):
    from . import volumes as V
    return f"/vol{i}.ext4" if lay.jailed else str(C.VOL / f"{volid}.ext4")


def _preflight():
    if not host.kvm_ok():
        raise RuntimeError("/dev/kvm is not accessible for this user (add it to the 'kvm' group, then log in again)")
    if not host.have(C.FC):
        raise RuntimeError("firecracker is not installed - run ./install.sh")
    if not C.KERNEL.exists():
        raise RuntimeError("no guest kernel - press 'Get kernel' first")


def start_vm(vid):
    v = get_vm(vid)
    with vm_lock(vid):
        v = get_vm(vid)
        reconcile()
        v = get_vm(vid)
        if v["status"] in ("running", "paused", "starting"):
            raise Conflict("VM is already running")
        _preflight()
        cfg = {**DEFAULT_CFG, **v["config"]}
        jailed = host.mode() == "jailer"
        v["jailed"] = int(jailed)
        lay = Layout(vid, jailed)
        if not lay.disk_rest.exists():
            raise RuntimeError("VM disk is missing")
        db.update("vms", vid, {"status": "starting", "jailed": int(jailed), "error": None, "started": now()})
        proc, tap = None, False
        warn = []
        try:
            from . import volumes as V
            rc = V.fsck_file(lay.disk_rest, "root disk")           # replay the journal after a crash before the guest sees the disk
            if rc & 1:
                warn.append("root disk was repaired after an unclean shutdown")
            for x in V.vm_volumes(v):
                V.preflight(x["row"])
            if cfg["net"] and host.net_enabled():
                host.net_add(vid)
                tap = True
            elif cfg["net"]:
                warn.append("host network not set up: the VM has no network (run ./install.sh)")
            fc, proc, w = _launch(v, lay)
            warn += w
            _stage_files(v, lay)
            _configure(fc, v, lay, cfg, warn)
            fc.action("InstanceStart")
        except Exception as e:
            _cleanup_after_failure(v, lay, proc, tap)
            db.update("vms", vid, {"status": "error", "error": str(e)[:900], "pid": None})
            raise
        if cfg["hotplug_mib"] and cfg.get("hotplug_target_mib"):
            try:
                fc.patch("/hotplug/memory", {"requested_size_mib": min(cfg["hotplug_target_mib"], cfg["hotplug_mib"])})
            except FCError as e:
                warn.append(f"memory hotplug target not applied: {e.fault}")
        pid = wait_for(lambda: host.find_fc_pid(vid), 5, 0.1)
        db.update("vms", vid, {"status": "running", "pid": pid, "started": now(), "stopped": None, "error": "; ".join(warn)[:900] or None})
        _ping_cache.pop(vid, None)
        _forwards_apply(vid)
    return view(get_vm(vid))


def _cleanup_after_failure(v, lay, proc, tap):
    _kill_proc(proc, v["id"])
    _recover_disk(v, lay)
    if tap:
        host.net_del(v["id"])
    _teardown_runtime(v, lay)


def _recover_disk(v, lay):
    """Jailer mode: move the disk from the jail back to rest."""
    if not lay.jailed:
        return
    j = lay.host("rootfs.ext4")
    try:
        if j.exists():
            lay.disk_rest.parent.mkdir(parents=True, exist_ok=True)
            shutil.move(str(j), str(lay.disk_rest))
        for i, x in enumerate((v.get("config") or {}).get("volumes") or []):
            jp, real = lay.host(f"vol{i}.ext4"), C.VOL / f"{x['vol']}.ext4"
            if jp.exists() and real.exists() and not os.path.samefile(jp, real):
                shutil.copyfile(jp, real)
        for n in ("fc.log", "metrics.json"):
            if lay.host(n).exists():
                shutil.copyfile(lay.host(n), C.VMS / v["id"] / n)
    except OSError:
        pass


def _teardown_runtime(v, lay):
    if lay.jailed:
        host.jail_rm(v["id"])
    else:
        shutil.rmtree(lay.root, ignore_errors=True)
    _ping_cache.pop(v["id"], None)


def _after_exit(v, crashed=False):
    lay = layout_of(v)
    _recover_disk(v, lay)
    if host.helper_info():
        host.net_del(v["id"])
    _teardown_runtime(v, lay)
    err, status = None, "stopped"
    if crashed:
        tail = console_tail(v["id"], 800)
        if "MVM: shutdown" not in tail:            # not a clean guest-initiated power off
            err, status = "firecracker exited unexpectedly. Last console output:\n" + tail, "error"
    db.update("vms", v["id"], {"status": status, "pid": None, "stopped": now(), "error": err})


def stop_vm(vid, graceful=True, timeout=20):
    v = get_vm(vid)
    with vm_lock(vid):
        v = get_vm(vid)
        pid = v.get("pid") or host.find_fc_pid(vid)
        if v["status"] not in ("running", "paused", "starting") and not pid:
            return view(v)
        lay = layout_of(v)
        if pid and graceful:
            if v["status"] == "paused":
                try:
                    FC(lay.api, 5).resume()
                except Exception:  # noqa: BLE001
                    pass
            try:
                agent.shutdown(lay.vsock)
            except Exception:  # noqa: BLE001
                if C.MACH == "x86_64":
                    try:
                        FC(lay.api, 5).action("SendCtrlAltDel")
                    except Exception:  # noqa: BLE001
                        pass
            if not wait_for(lambda: not _alive(pid), timeout, 0.2):
                pass
        if pid and _alive(pid):
            _kill_pid(pid, 2.0)
        proc = _procs.pop(vid, None)
        if proc:
            try:
                proc.wait(2)
            except Exception:  # noqa: BLE001
                pass
        _after_exit(get_vm(vid))
    return view(get_vm(vid))


def restart_vm(vid):
    stop_vm(vid)
    return start_vm(vid)


def kill_vm(vid):
    return stop_vm(vid, graceful=False)


def pause_vm(vid):
    v = get_vm(vid)
    if vm_state(v) != "running":
        raise Conflict("VM is not running")
    FC(layout_of(v).api).pause()
    db.update("vms", vid, {"status": "paused"})
    return view(get_vm(vid))


def resume_vm(vid):
    v = get_vm(vid)
    if vm_state(v) != "paused":
        raise Conflict("VM is not paused")
    FC(layout_of(v).api).resume()
    db.update("vms", vid, {"status": "running"})
    return view(get_vm(vid))


def delete_vm(vid):
    v = get_vm(vid)
    with vm_lock(vid):
        if v["status"] in ("running", "paused", "starting") or host.find_fc_pid(vid):
            stop_vm(vid, graceful=False)
        for f in db.list_("forwards", "WHERE vm_id=?", (vid,)):
            _forward_remove_rule(get_vm(vid), f)
            db.delete("forwards", f["id"])
        from . import volumes as V
        V.release_vm(vid)
        shutil.rmtree(C.VMS / vid, ignore_errors=True)
        shutil.rmtree(C.SNAP / vid, ignore_errors=True)
        for s in db.list_("snapshots", "WHERE vm_id=?", (vid,)):
            db.delete("snapshots", s["id"])
        db.delete("vms", vid)
    return {"ok": True}


def clone_vm(vid, name=None):
    v = get_vm(vid)
    if v["status"] != "stopped":
        raise Conflict("stop the VM before cloning it (the disk must be consistent)")
    ccfg = dict(v["config"] or {})
    ccfg["volumes"] = []                                  # volumes belong to exactly one VM; attach new ones to the clone yourself
    new = create_vm(v["image"], name=name or f"{v['name']}-clone"[:48], vcpu=v["vcpu"], mem=v["mem"], config=ccfg)
    reflink_copy(C.VMS / vid / "rootfs.ext4", C.VMS / new["id"] / "rootfs.ext4")
    db.update("vms", new["id"], {"disk_mb": v["disk_mb"]})
    return view(get_vm(new["id"]))


def update_resources(vid, vcpu=None, mem=None, config=None, disk_mb=None):
    """Offline changes (apply on next start)."""
    v = get_vm(vid)
    if v["status"] != "stopped":
        raise Conflict("stop the VM to change vCPUs, memory, disk or boot options")
    f = {}
    if vcpu is not None:
        f["vcpu"] = int_in(vcpu, 1, C.MAX_VCPU, "vcpus")
    if mem is not None:
        f["mem"] = int_in(mem, 64, C.MAX_MEM_MIB, "memory (MiB)")
    if config is not None:
        old = {**DEFAULT_CFG, **v["config"]}
        new = normalize_config({**old, **config})
        f["config"] = new
        f["autostart"] = int(new["autostart"])
        img = db.get("images", v["image"], "name") or {}
        disk = C.VMS / vid / "rootfs.ext4"
        if new["env"] != old["env"]:
            env = {**(img.get("env") or {}), **new["env"]}
            _debugfs_write(disk, "/etc/mvm/env", "".join(f"{k}={x}\n" for k, x in env.items()))
        if new["command"] != old["command"] and new["command"]:
            wd = img.get("workdir") or "/"
            _debugfs_write(disk, "/etc/mvm/run", "#!/bin/sh\ncd " + shlex.quote(wd) + " 2>/dev/null || cd /\nexec " + " ".join(shlex.quote(a) for a in new["command"]) + "\n")
    if disk_mb:
        f["disk_mb"] = _grow(C.VMS / vid / "rootfs.ext4", int_in(disk_mb, 1, 1 << 20, "disk size"))
    if f:
        db.update("vms", vid, f)
    return view(get_vm(vid))


# ------------------------------------------------------------------ live adjustments (Firecracker PATCH endpoints)
def live_update(vid, data):
    v = get_vm(vid)
    if vm_state(v) not in ("running", "paused"):
        raise Conflict("VM is not running")
    fc = FC(layout_of(v).api)
    cfg = {**DEFAULT_CFG, **v["config"]}
    changed = []
    if "balloon_mib" in data:
        if not cfg["balloon"]:
            raise UserError("this VM was created without a balloon device (enable it in the VM settings, then restart)")
        mib = int_in(data["balloon_mib"], 0, v["mem"], "balloon size")
        fc.set_balloon(mib)
        cfg["balloon"] = {**cfg["balloon"], "amount_mib": mib}
        changed.append("balloon")
    for k, path in (("rx_mbit", "rx_rate_limiter"), ("tx_mbit", "tx_rate_limiter")):
        if k in data:
            if not (cfg["net"] and host.net_enabled()):
                raise UserError("VM has no network interface")
            val = int_in(data[k], 0, 100000, k)
            # PATCH with empty bucket removes the limit: Firecracker treats size 0 as unlimited
            body = rate_limiter(val) or {"bandwidth": {"size": 0, "refill_time": 100}}
            fc.patch("/network-interfaces/eth0", {"iface_id": "eth0", path: body})
            cfg[k] = val
            changed.append(k)
    if "disk_mbit" in data or "disk_iops" in data:
        mb = int_in(data.get("disk_mbit", cfg["disk_mbit"]), 0, 100000, "disk_mbit")
        iops = int_in(data.get("disk_iops", cfg["disk_iops"]), 0, 10_000_000, "disk_iops")
        body = rate_limiter(mb, iops) or {"bandwidth": {"size": 0, "refill_time": 100}, "ops": {"size": 0, "refill_time": 100}}
        fc.patch("/drives/rootfs", {"drive_id": "rootfs", "rate_limiter": body})
        cfg["disk_mbit"], cfg["disk_iops"] = mb, iops
        changed.append("disk")
    if "hotplug_mib" in data:
        if not cfg["hotplug_mib"]:
            raise UserError("memory hotplug is not enabled for this VM (Settings, then restart)")
        want = int_in(data["hotplug_mib"], 0, cfg["hotplug_mib"], "hotplug target")
        fc.patch("/hotplug/memory", {"requested_size_mib": want})
        cfg["hotplug_target_mib"] = want
        changed.append("hotplug")
    if "mmds" in data:
        if not cfg["mmds"]:
            raise UserError("metadata service (MMDS) is not enabled for this VM")
        md = data["mmds"]
        if not isinstance(md, dict) or len(json.dumps(md)) > 51200:
            raise UserError("metadata must be a JSON object under 50 KiB")
        fc.set_mmds(md)
        cfg["mmds"] = {"data": md}
        changed.append("mmds")
    if not changed:
        raise UserError("nothing to change")
    db.update("vms", vid, {"config": cfg})
    return view(get_vm(vid))


# ------------------------------------------------------------------ observability
def metrics(vid):
    """Flush Firecracker's metrics and return the latest sample plus balloon statistics."""
    v = get_vm(vid)
    lay = layout_of(v)
    res = {"vm": {}, "balloon": None, "proc": host.proc_stats(v["pid"]) if v.get("pid") else None, "fc": None}
    if vm_state(v) not in ("running", "paused"):
        return res
    fc = FC(lay.api, 5)
    try:
        fc.action("FlushMetrics")
    except FCError:
        pass
    txt = tail_file(lay.host("metrics.json") if lay.jailed else lay.host("metrics.json"), 200000)
    for line in reversed(txt.splitlines()):
        try:
            res["fc"] = json.loads(line)
            break
        except ValueError:
            continue
    cfg = v["config"] or {}
    if cfg.get("hotplug_mib"):
        res["hotplug"] = _try(lambda: fc.get("/hotplug/memory"))
    if cfg.get("balloon"):
        try:
            res["balloon"] = {**fc.balloon(), "stats": _try(fc.balloon_stats)}
        except FCError:
            pass
    return res


def _try(fn):
    try:
        return fn()
    except Exception:  # noqa: BLE001
        return None


def guest_stats(vid):
    v = get_vm(vid)
    if v["status"] != "running":
        raise Conflict("VM is not running")
    s = agent.stats(layout_of(v).vsock)
    s["t"] = time.time()
    return s


def firecracker_log(vid, n=20000):
    v = get_vm(vid)
    lay = layout_of(v)
    p = lay.host("fc.log") if v["status"] in ("running", "paused", "starting") else C.VMS / vid / "fc.log"
    raw = tail_file(p, n * 4)
    out, skip = [], False
    for line in raw.splitlines():          # hide the UI's own state polling (GET "/") - it drowns the useful lines
        if 'received a Get request on "/".' in line:
            skip = True
            continue
        if skip and "The request was executed successfully" in line:
            skip = False
            continue
        skip = False
        out.append(line)
    return "\n".join(out[-400:])


def machine_info(vid):
    v = get_vm(vid)
    if vm_state(v) not in ("running", "paused"):
        raise Conflict("VM is not running")
    fc = FC(layout_of(v).api, 5)
    return {"info": fc.info(), "config": _try(fc.vm_config), "version": _try(fc.version)}


def run_exec(vid, cmd, workdir="/", timeout=60):
    if C.DISABLE_EXEC:
        raise UserError("exec is disabled on this server (MVM_DISABLE_EXEC)")
    v = get_vm(vid)
    if vm_state(v) != "running":
        raise Conflict("VM is not running (paused VMs cannot execute commands)")
    cmd = str(cmd or "")
    if not cmd.strip() or len(cmd) > 100_000:
        raise UserError("command required (max 100 KB)")
    return agent.exec_(layout_of(v).vsock, cmd, workdir or "/", int_in(timeout, 1, 3600, "timeout"))


def file_put(vid, path, data, mode="644"):
    if C.DISABLE_EXEC:
        raise UserError("file transfer is disabled on this server")
    v = get_vm(vid)
    if vm_state(v) != "running":
        raise Conflict("VM is not running")
    if not path.startswith("/") or "\0" in path or len(path) > 4000:
        raise UserError("absolute guest path required")
    if not re.fullmatch(r"[0-7]{3,4}", mode):
        raise UserError("bad mode")
    return agent.put_file(layout_of(v).vsock, path, data, mode)


def file_get(vid, path):
    if C.DISABLE_EXEC:
        raise UserError("file transfer is disabled on this server")
    v = get_vm(vid)
    if vm_state(v) != "running":
        raise Conflict("VM is not running")
    if not path.startswith("/") or "\0" in path:
        raise UserError("absolute guest path required")
    return agent.get_file(layout_of(v).vsock, path)


# ------------------------------------------------------------------ snapshots
def snapshot_dir(vid, sid):
    return C.SNAP / vid / sid


def create_snapshot(vid, name, note=""):
    """Pause -> Firecracker /snapshot/create (Full) -> copy disk -> resume. Memory + device state + disk are kept."""
    v = get_vm(vid)
    if (v.get("config") or {}).get("volumes"):
        raise UserError("VM snapshots do not include attached volumes - use volume backups for this VM")
    name = valid_name(name or time.strftime("snap-%Y%m%d-%H%M%S"), "snapshot name")
    if any(s["name"] == name for s in db.list_("snapshots", "WHERE vm_id=?", (vid,))):
        raise Conflict("a snapshot with that name already exists")
    with vm_lock(vid):
        v = get_vm(vid)
        state = vm_state(v)
        if state not in ("running", "paused"):
            raise Conflict("snapshots need a running (or paused) VM")
        lay, fc = layout_of(v), FC(layout_of(v).api, 600)
        was_running = state == "running"
        sid = uuid.uuid4().hex[:8]
        sd = snapshot_dir(vid, sid)
        sd.mkdir(parents=True)
        try:
            if was_running:
                fc.pause()
            fc.put("/snapshot/create", {"snapshot_type": "Full", "snapshot_path": lay.inside("snap.state"),
                                        "mem_file_path": lay.inside("snap.mem")})
            reflink_copy(lay.disk_running, sd / "rootfs.ext4")
            shutil.move(str(lay.host("snap.state")), sd / "vmstate")
            shutil.move(str(lay.host("snap.mem")), sd / "mem")
        except Exception:
            shutil.rmtree(sd, ignore_errors=True)
            raise
        finally:
            if was_running:
                try:
                    fc.resume()
                except Exception:  # noqa: BLE001
                    pass
        size = sum(p.stat().st_size for p in sd.iterdir())
        db.upsert("snapshots", {"id": sid, "vm_id": vid, "name": name, "kind": "full", "mem_mib": v["mem"], "size_bytes": size,
                                "created": now(), "note": note[:200]})
    return db.get("snapshots", sid)


def list_snapshots(vid):
    get_vm(vid)
    return db.list_("snapshots", "WHERE vm_id=?", (vid,), order="created DESC")


def delete_snapshot(vid, sid):
    s = db.get("snapshots", sid)
    if not s or s["vm_id"] != vid:
        raise NotFound(sid)
    shutil.rmtree(snapshot_dir(vid, sid), ignore_errors=True)
    db.delete("snapshots", sid)
    return {"ok": True}


def restore_snapshot(vid, sid):
    """Restore a stopped VM from a snapshot: fresh Firecracker, /snapshot/load, resume."""
    v = get_vm(vid)
    s = db.get("snapshots", sid)
    if not s or s["vm_id"] != vid:
        raise NotFound(sid)
    if (v.get("config") or {}).get("volumes"):
        raise UserError("snapshot restore is not available for VMs with attached volumes - restore a volume backup instead")
    with vm_lock(vid):
        reconcile()
        v = get_vm(vid)
        if v["status"] in ("running", "paused", "starting"):
            raise Conflict("stop the VM first - a snapshot is loaded into a fresh Firecracker process")
        _preflight()
        sd = snapshot_dir(vid, sid)
        if not (sd / "vmstate").exists():
            raise RuntimeError("snapshot files are missing")
        jailed = host.mode() == "jailer"
        lay = Layout(vid, jailed)
        cfg = {**DEFAULT_CFG, **v["config"]}
        db.update("vms", vid, {"status": "starting", "jailed": int(jailed), "error": None, "started": now()})
        v["jailed"] = int(jailed)
        proc, tap, warn = None, False, []
        try:
            reflink_copy(sd / "rootfs.ext4", lay.disk_rest)
            if cfg["net"] and host.net_enabled():
                host.net_add(vid)
                tap = True
            fc, proc, w = _launch(v, lay)
            warn += w
            for n in ("fc.log", "metrics.json"):
                lay.host(n).touch(mode=0o600)
            if lay.jailed:
                reflink_copy(lay.disk_rest, lay.host("rootfs.ext4"))
                lay.disk_rest.unlink()
                reflink_copy(sd / "vmstate", lay.host("snap.state"))
                reflink_copy(sd / "mem", lay.host("snap.mem"))
                st_path, mem_path = "/snap.state", "/snap.mem"
            else:
                st_path, mem_path = str(sd / "vmstate"), str(sd / "mem")
            try:
                fc.put("/logger", {"log_path": lay.inside("fc.log"), "level": "Info", "show_level": True, "show_log_origin": False})
            except FCError:
                pass
            body = {"snapshot_path": st_path, "mem_backend": {"backend_path": mem_path, "backend_type": "File"}, "resume_vm": True}
            fc.put("/snapshot/load", body)
        except Exception as e:
            _cleanup_after_failure(v, lay, proc, tap)
            db.update("vms", vid, {"status": "error", "error": str(e)[:900], "pid": None})
            raise
        if lay.jailed:
            for n in ("snap.state", "snap.mem"):
                lay.host(n).unlink(missing_ok=True)
        pid = wait_for(lambda: host.find_fc_pid(vid), 5, 0.1)
        db.update("vms", vid, {"status": "running", "pid": pid, "started": now(), "error": "; ".join(warn) or None})
        _ping_cache.pop(vid, None)
    return view(get_vm(vid))


# ------------------------------------------------------------------ published ports
def forward_add(vid, host_port, guest_port, proto="tcp"):
    v = get_vm(vid)
    if not host.helper_info():
        raise UserError("port publishing needs the root helper (run ./install.sh)")
    hp = int_in(host_port, 1024, 65535, "host port")
    gp = int_in(guest_port, 1, 65535, "guest port")
    if proto not in ("tcp", "udp"):
        raise UserError("proto must be tcp or udp")
    if db.get("forwards", hp, "host_port"):
        raise Conflict(f"host port {hp} is already published")
    host.priv("pf-add", hp, v["ip"], gp, proto)
    fid = db.insert("forwards", {"vm_id": vid, "host_port": hp, "guest_port": gp, "proto": proto, "created": now()})
    return db.get("forwards", fid)


def _forward_remove_rule(v, f):
    try:
        host.priv("pf-del", f["host_port"], v["ip"], f["guest_port"], f["proto"], check=False)
    except RuntimeError:
        pass


def forward_remove(vid, fid):
    v = get_vm(vid)
    f = db.get("forwards", int(fid))
    if not f or f["vm_id"] != vid:
        raise NotFound(fid)
    _forward_remove_rule(v, f)
    db.delete("forwards", f["id"])
    return {"ok": True}


def _forwards_apply(vid):
    v = get_vm(vid)
    for f in db.list_("forwards", "WHERE vm_id=?", (vid,)):
        try:
            host.priv("pf-add", f["host_port"], v["ip"], f["guest_port"], f["proto"], check=False)
        except RuntimeError:
            pass


# ------------------------------------------------------------------ startup / shutdown
def startup(log=print):
    reconcile()
    adopted = [v["name"] for v in db.list_("vms") if v["status"] in ("running", "paused")]
    if adopted:
        log(f"adopted running VM(s): {', '.join(adopted)}")
    if host.helper_info():
        for v in db.list_("vms"):
            if v["status"] == "running":
                _forwards_apply(v["id"])
        known = {v["id"] for v in db.list_("vms")}
        try:
            for tap in host.priv("net-list", check=False).stdout.split():
                vid = tap[3:]
                if vid not in known or db.get("vms", vid)["status"] not in ("running", "paused", "starting"):
                    host.net_del(vid)
                    log(f"removed orphan {tap}")
        except RuntimeError:
            pass
    for v in db.list_("vms"):
        if v["status"] in ("stopped", "error") and v.get("autostart"):
            try:
                start_vm(v["id"])
                log(f"autostarted {v['name']}")
            except Exception as e:  # noqa: BLE001
                log(f"autostart {v['name']} failed: {e}")
    # leftovers of hidden builder VMs
    for v in db.list_("vms", "WHERE hidden=1"):
        try:
            delete_vm(v["id"])
        except Exception:  # noqa: BLE001
            pass


def shutdown_all():
    if not C.STOP_ON_EXIT:
        return
    for v in db.list_("vms"):
        if v["status"] in ("running", "paused", "starting"):
            try:
                stop_vm(v["id"])
            except Exception:  # noqa: BLE001
                pass

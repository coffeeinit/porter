"""Replica sets: N identical microVMs from one image, scaled / rolled / healed together."""
import re
import threading
import time
import uuid
from concurrent.futures import ThreadPoolExecutor

from . import agent, config as C, db, lb, vmm
from . import volumes as V
from .util import Conflict, NotFound, UserError, age_s, int_in, now, valid_name, wait_for

MAX_REPLICAS = 32
_locks, _guard = {}, threading.Lock()
_balancers = {}
_attempts = {}


def set_lock(sid):
    with _guard:
        return _locks.setdefault(sid, threading.RLock())


def get_set(sid):
    rs = db.get("replicasets", sid)
    if not rs:
        rs = db.get("replicasets", sid, "name")
    if not rs:
        raise NotFound(sid)
    return rs


def members(sid):
    return sorted(db.list_("vms", "WHERE rset=?", (sid,)), key=lambda v: v["ridx"] or 0)


def _cfg_for(rs, idx):
    cfg = dict(rs["config"] or {})
    env = dict(cfg.get("env") or {})
    env["REPLICA_INDEX"] = str(idx)
    env["REPLICA_SET"] = rs["name"]
    cfg["env"] = env
    cfg["autostart"] = False                    # the set (auto-heal) decides, not the per-VM flag
    cfg["volumes"] = []                         # per-replica volumes are attached by _make
    return cfg


def _make(rs, idx):
    vm = vmm.create_vm(rs["image"], name=f"{rs['name']}-{idx}", vcpu=rs["vcpu"], mem=rs["mem"], disk_mb=rs["disk_mb"] or 0,
                       config=_cfg_for(rs, idx), rset=rs["id"], ridx=idx)
    try:
        for t in rs.get("volumes") or []:            # one volume per replica, same name every time => data survives rollouts/heals
            V.ensure_attach(vm["id"], {"name": f"{rs['name']}-{idx}-{t['tag']}", "size_mb": t["size_mb"], "mount": t["mount"],
                                       "backup_every_h": t.get("backup_every_h") or 0, "backup_keep": t.get("backup_keep") or 5,
                                       "pre_cmd": t.get("pre_cmd")}, rset=rs["id"], ridx=idx)
    except Exception:
        vmm.delete_vm(vm["id"])
        raise
    return vmm.view(db.get("vms", vm["id"]))


def _vol_tpl(vols):
    out = []
    for t in vols or []:
        tag = valid_name(t.get("tag") or "data", "volume tag")
        out.append({"tag": tag, "size_mb": int_in(t.get("size_mb"), 16, 1 << 21, "volume size"), "mount": V.valid_mount(t.get("mount")),
                    "backup_every_h": int_in(t.get("backup_every_h") or 0, 0, 24 * 90, "backup interval"),
                    "backup_keep": int_in(t.get("backup_keep") or 5, 1, 100, "backups to keep"), "pre_cmd": (t.get("pre_cmd") or None)})
    if len({t["tag"] for t in out}) != len(out) or len({t["mount"] for t in out}) != len(out) or len(out) > C.MAX_VOLUMES_PER_VM:
        raise UserError("replica volumes need unique tags and mount paths (max %d)" % C.MAX_VOLUMES_PER_VM)
    return out


def _start_many(vms):
    errs = {}

    def go(v):
        try:
            vmm.start_vm(v["id"])
        except Exception as e:  # noqa: BLE001
            errs[v["name"]] = str(e).splitlines()[0][:200]
    with ThreadPoolExecutor(max_workers=4) as ex:
        list(ex.map(go, vms))
    return errs


def _ready(v):
    try:
        return bool(agent.ping(vmm.agent_sock(v["id"]), 2))
    except Exception:  # noqa: BLE001
        return False


# ------------------------------------------------------------------ create / change
def create_set(name, image, count, vcpu=1, mem=256, disk_mb=0, config=None, lb_port=None, service_port=None,
               lb_bind="127.0.0.1", autoheal=True, git_source=None, start=True, lb_algo="round_robin", volumes=None):
    name = valid_name(name, "replica set name")
    if len(name) > 40:
        raise UserError("replica set name: at most 40 characters")
    if db.get("replicasets", name, "name"):
        raise Conflict(f"replica set '{name}' already exists")
    if not db.get("images", image, "name"):
        raise NotFound(f"image {image}")
    count = int_in(count, 1, MAX_REPLICAS, "replicas")
    vcpu, mem = int_in(vcpu, 1, C.MAX_VCPU, "vcpus"), int_in(mem, 64, C.MAX_MEM_MIB, "memory (MiB)")
    cfg = vmm.normalize_config(config)
    lb_port, service_port, lb_bind = _lb_args(lb_port, service_port, lb_bind, None)
    lb_algo = _algo(lb_algo)
    volumes = _vol_tpl(volumes)
    sid = uuid.uuid4().hex[:8]
    rs = {"id": sid, "name": name, "image": image, "desired": count, "vcpu": vcpu, "mem": mem, "disk_mb": int(disk_mb or 0),
          "config": cfg, "state": "running" if start else "stopped", "autoheal": int(bool(autoheal)), "lb_port": lb_port,
          "service_port": service_port, "lb_bind": lb_bind, "git_source": git_source, "created": now(), "note": None,
          "lb_algo": lb_algo, "volumes": volumes}
    with set_lock(sid):
        db.upsert("replicasets", rs)
        made = []
        try:
            for i in range(1, count + 1):
                made.append(_make(rs, i))
        except Exception:
            for v in made:
                vmm.delete_vm(v["id"])
            db.delete("replicasets", sid)
            raise
        errs = _start_many(made) if start else {}
        if errs:
            db.update("replicasets", sid, {"note": "some replicas failed to start: " + "; ".join(f"{k}: {v}" for k, v in errs.items())[:600]})
        _sync_lb(get_set(sid))
    return view(get_set(sid))


def _algo(a):
    a = (a or "round_robin").lower().replace("-", "_")
    if a not in lb.ALGOS:
        raise UserError(f"load balancing must be one of {', '.join(lb.ALGOS)}")
    return a


def _lb_args(lb_port, service_port, lb_bind, current_id):
    if not lb_port:
        return None, None, "127.0.0.1"
    lb_port = int_in(lb_port, 1024, 65535, "load balancer port")
    service_port = int_in(service_port, 1, 65535, "service port")
    if lb_bind not in ("127.0.0.1", "0.0.0.0"):
        raise UserError("load balancer bind must be 127.0.0.1 or 0.0.0.0")
    for o in db.list_("replicasets"):
        if o["lb_port"] == lb_port and o["id"] != current_id:
            raise Conflict(f"load balancer port {lb_port} is used by set '{o['name']}'")
    return lb_port, service_port, lb_bind


def update_set(sid, data):
    rs = get_set(sid)
    with set_lock(rs["id"]):
        f = {}
        if "vcpu" in data:
            f["vcpu"] = int_in(data["vcpu"], 1, C.MAX_VCPU, "vcpus")
        if "mem" in data:
            f["mem"] = int_in(data["mem"], 64, C.MAX_MEM_MIB, "memory (MiB)")
        if "disk_mb" in data:
            f["disk_mb"] = int_in(data["disk_mb"] or 0, 0, 1 << 20, "disk")
        if "image" in data:
            if not db.get("images", data["image"], "name"):
                raise NotFound(f"image {data['image']}")
            f["image"] = data["image"]
        if "config" in data:
            f["config"] = vmm.normalize_config({**(rs["config"] or {}), **data["config"]})
        if "autoheal" in data:
            f["autoheal"] = int(bool(data["autoheal"]))
        if "lb_algo" in data:
            f["lb_algo"] = _algo(data["lb_algo"])
        if "volumes" in data:
            f["volumes"] = _vol_tpl(data["volumes"])
        if any(k in data for k in ("lb_port", "service_port", "lb_bind")):
            lp, sp, bd = _lb_args(data.get("lb_port", rs["lb_port"]), data.get("service_port", rs["service_port"]),
                                  data.get("lb_bind", rs["lb_bind"]), rs["id"])
            f.update(lb_port=lp, service_port=sp, lb_bind=bd)
        if f:
            db.update("replicasets", rs["id"], f)
            _sync_lb(get_set(rs["id"]))
        if any(k in f for k in ("vcpu", "mem", "disk_mb", "image", "config")):
            db.update("replicasets", rs["id"], {"note": "template changed: run a rolling update to apply it to existing replicas"})
    return view(get_set(sid))


def scale(sid, n):
    rs = get_set(sid)
    n = int_in(n, 0, MAX_REPLICAS, "replicas")
    with set_lock(rs["id"]):
        mem = members(rs["id"])
        have = {v["ridx"]: v for v in mem}
        if n > len(mem):
            new = []
            for i in range(1, n + len(mem) + 1):
                if len(have) + len(new) >= n:
                    break
                if i not in have:
                    new.append(_make(rs, i))
            if rs["state"] == "running":
                errs = _start_many(new)
                if errs:
                    db.update("replicasets", rs["id"], {"note": "start failed: " + "; ".join(f"{k}: {v}" for k, v in errs.items())[:600]})
        else:
            for v in sorted(mem, key=lambda x: x["ridx"] or 0, reverse=True)[: len(mem) - n]:
                vmm.delete_vm(v["id"])
        db.update("replicasets", rs["id"], {"desired": n})
        _sync_lb(get_set(rs["id"]))
    return view(get_set(sid))


def start_set(sid):
    rs = get_set(sid)
    with set_lock(rs["id"]):
        db.update("replicasets", rs["id"], {"state": "running", "note": None})
        errs = _start_many([v for v in members(rs["id"]) if not v["status"] in ("running", "paused", "starting")])
        if errs:
            db.update("replicasets", rs["id"], {"note": "; ".join(f"{k}: {v}" for k, v in errs.items())[:600]})
        _sync_lb(get_set(rs["id"]))
    return view(get_set(sid))


def stop_set(sid):
    rs = get_set(sid)
    with set_lock(rs["id"]):
        db.update("replicasets", rs["id"], {"state": "stopped"})       # first: so auto-heal leaves them alone
        _sync_lb(get_set(rs["id"]))
        with ThreadPoolExecutor(max_workers=4) as ex:
            list(ex.map(lambda v: vmm.stop_vm(v["id"]), [v for v in members(rs["id"]) if v["status"] in ("running", "paused", "starting")]))
    return view(get_set(sid))


def restart_set(sid):
    stop_set(sid)
    return start_set(sid)


def delete_set(sid, delete_volumes=False):
    rs = get_set(sid)
    with set_lock(rs["id"]):
        db.update("replicasets", rs["id"], {"state": "stopped"})
        _stop_lb(rs["id"])
        for v in members(rs["id"]):
            vmm.delete_vm(v["id"])
        kept = db.list_("volumes", "WHERE rset=?", (rs["id"],))
        if delete_volumes:
            for vol in kept:
                V.delete(vol["id"])
            kept = []
        db.delete("replicasets", rs["id"])
    return {"ok": True, "kept_volumes": [x["name"] for x in kept]}


def rollout(sid, log=print, ready_timeout=120):
    """Rolling update: replace replicas one by one (stop -> recreate from the CURRENT image/template ->
    start -> wait until its agent answers) so the rest keep serving. Aborts on the first failure."""
    rs = get_set(sid)
    with set_lock(rs["id"]):
        mem = members(rs["id"])
        log(f"rolling update of '{rs['name']}': {len(mem)} replica(s) from image '{rs['image']}'")
        for old in mem:
            idx = old["ridx"]
            log(f"  replica {idx}: replacing")
            vmm.delete_vm(old["id"])
            v = _make(rs, idx)
            if rs["state"] == "running":
                vmm.start_vm(v["id"])
                if not wait_for(lambda: _ready(v), ready_timeout, 1):
                    db.update("replicasets", rs["id"], {"note": f"rollout stopped: replica {idx} did not become ready"})
                    raise RuntimeError(f"replica {idx} did not become ready within {ready_timeout}s - rollout stopped")
            log(f"  replica {idx}: ready")
        db.update("replicasets", rs["id"], {"last_rollout": now(), "note": None})
        _sync_lb(get_set(rs["id"]))
    return view(get_set(sid))


# ------------------------------------------------------------------ view
def view(rs):
    mem = members(rs["id"])
    reps = []
    for v in mem:
        d = vmm.view(v, light=True)
        d["ready"] = d["status"] == "running" and vmm._agent_ok(v)
        reps.append({k: d[k] for k in ("id", "name", "ridx", "status", "ip", "ready", "uptime_s", "agent", "error", "vcpu", "mem")})
    b = _balancers.get(rs["id"])
    out = dict(rs)
    out.update(replicas=reps, ready=sum(1 for r in reps if r["ready"]), running=sum(1 for r in reps if r["status"] == "running"),
               lb={"stats": b.stats if b else None, "live": dict(b.live) if b else {}, "algo": rs.get("lb_algo") or "round_robin", "active": bool(b)})
    return out


def list_sets():
    vmm.reconcile()
    return [view(rs) for rs in db.list_("replicasets")]


# ------------------------------------------------------------------ load balancer
def _backends_fn(sid):
    def fn():
        return [(v["name"], v["ip"]) for v in members(sid) if v["status"] == "running" and v.get("ip")]
    return fn


def _stop_lb(sid):
    b = _balancers.pop(sid, None)
    if b:
        b.stop()


def _sync_lb(rs):
    _stop_lb(rs["id"])
    if rs["lb_port"] and rs["state"] == "running":
        b = lb.Balancer(rs["lb_bind"], rs["lb_port"], rs["service_port"], _backends_fn(rs["id"]), algo=rs.get("lb_algo") or "round_robin")
        try:
            b.start()
        except OSError as e:
            db.update("replicasets", rs["id"], {"note": f"load balancer could not listen on {rs['lb_bind']}:{rs['lb_port']}: {e}"})
            return
        _balancers[rs["id"]] = b


# ------------------------------------------------------------------ self-healing
def _may_try(vid):
    t = [x for x in _attempts.get(vid, []) if time.time() - x < 600]
    if len(t) >= 3:
        _attempts[vid] = t
        return False
    t.append(time.time())
    _attempts[vid] = t
    return True


def heal_once():
    for rs in db.list_("replicasets"):
        if rs["state"] != "running" or not rs["autoheal"]:
            continue
        lk = set_lock(rs["id"])
        if not lk.acquire(blocking=False):
            continue
        try:
            rs = get_set(rs["id"])
            mem = members(rs["id"])
            have = {v["ridx"] for v in mem}
            for i in range(1, rs["desired"] + 1):
                if i not in have and _may_try(f"{rs['id']}-{i}"):
                    try:
                        vmm.start_vm(_make(rs, i)["id"])
                    except Exception as e:  # noqa: BLE001
                        db.update("replicasets", rs["id"], {"note": f"auto-heal: replica {i}: {str(e)[:200]}"})
            for v in mem:
                if v["status"] in ("stopped", "error") and age_s(v.get("stopped") or v.get("started") or v.get("created")) > 5 and _may_try(v["id"]):
                    try:
                        vmm.start_vm(v["id"])
                    except Exception as e:  # noqa: BLE001
                        db.update("replicasets", rs["id"], {"note": f"auto-heal: {v['name']}: {str(e)[:200]}"})
        finally:
            lk.release()


def _heal_loop():
    while True:
        time.sleep(10)
        try:
            vmm.reconcile()
            heal_once()
        except Exception:  # noqa: BLE001
            pass


def startup():
    for rs in db.list_("replicasets"):
        _sync_lb(rs)
    threading.Thread(target=_heal_loop, daemon=True, name="replica-heal").start()

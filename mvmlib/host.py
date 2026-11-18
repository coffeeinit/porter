"""Everything that touches the host: the root helper, kernel download, health checks."""
import json
import mmap
import os
import re
import shutil
import threading
import time
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET
from pathlib import Path

from . import config as C
from .util import UserError, out, run, sha256_file

_cache = {}
_lock = threading.Lock()


def cached(key, ttl, fn):
    with _lock:
        v = _cache.get(key)
        if v and time.time() - v[0] < ttl:
            return v[1]
    val = fn()
    with _lock:
        _cache[key] = (time.time(), val)
    return val


def have(tool):
    return bool(shutil.which(tool)) or (os.path.isabs(tool) and os.access(tool, os.X_OK))


# ---------------------------------------------------------------- root helper
def _helper_info():
    if not os.access(C.PRIV, os.X_OK):
        return None
    try:
        r = run(["sudo", "-n", C.PRIV, "info"], timeout=8, check=False)
        return json.loads(r.stdout) if r.returncode == 0 else None
    except (RuntimeError, ValueError):
        return None


def helper_info():
    return cached("helper", 20, _helper_info)


def priv(*args, timeout=60, check=True):
    """Run a fixed mvm-priv sub-command via sudo. Arguments are validated again by the helper."""
    if not helper_info():
        raise RuntimeError("root helper not installed - run ./install.sh (it sets up mvm-priv and sudoers)")
    return run(["sudo", "-n", C.PRIV, *[str(a) for a in args]], timeout=timeout, check=check)


def mode():
    """jailer (recommended) or direct (no jailer, no root helper needed; VM still seccomp-sandboxed by Firecracker)."""
    h = helper_info()
    want = C.MODE
    if want == "jailer" or (want == "auto" and h and h.get("jailer_ok") and h.get("fc_ok")):
        if not (h and h.get("jailer_ok")):
            raise RuntimeError("MVM_MODE=jailer but the jailer/helper is not usable (run ./install.sh)")
        return "jailer"
    return "direct"


def net_enabled():
    h = helper_info()
    return bool(h) and Path(f"/sys/class/net/{C.BRIDGE}").exists()


def net_add(vid): priv("net-add", vid)
def net_del(vid): priv("net-del", vid, check=False)
def jail_rm(vid): priv("jail-rm", vid, check=False)


def net_status():
    if not helper_info():
        return {"helper": False}
    try:
        r = run(["sudo", "-n", C.PRIV, "status"], timeout=8, check=False)
        d = json.loads(r.stdout) if r.returncode == 0 else {}
    except (RuntimeError, ValueError):
        d = {}
    d["helper"] = True
    return d


# ---------------------------------------------------------------- processes
def find_fc_pid(vid):
    """The firecracker process for a VM id (both jailer and direct mode start it with --id <vid>)."""
    for p in Path("/proc").glob("[0-9]*/cmdline"):
        try:
            parts = p.read_bytes().split(b"\0")
        except OSError:
            continue
        if parts and parts[0].rsplit(b"/", 1)[-1].startswith(b"firecracker") and b"--id" in parts:
            i = parts.index(b"--id")
            if i + 1 < len(parts) and parts[i + 1] == vid.encode():
                return int(p.parent.name)
    return None


def all_fc_pids():
    res = {}
    for p in Path("/proc").glob("[0-9]*/cmdline"):
        try:
            parts = p.read_bytes().split(b"\0")
        except OSError:
            continue
        if parts and parts[0].rsplit(b"/", 1)[-1].startswith(b"firecracker") and b"--id" in parts:
            i = parts.index(b"--id")
            if i + 1 < len(parts):
                res[parts[i + 1].decode("ascii", "replace")] = int(p.parent.name)
    return res


def proc_stats(pid):
    """RSS + cumulative CPU seconds of the firecracker process (what the VM really costs the host)."""
    try:
        st = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
        ticks = os.sysconf("SC_CLK_TCK")
        cpu = (int(st[11]) + int(st[12])) / ticks
        rss = 0
        for line in Path(f"/proc/{pid}/status").read_text().splitlines():
            if line.startswith("VmRSS:"):
                rss = int(line.split()[1]) * 1024
        threads = int(st[17])
        return {"rss": rss, "cpu_s": cpu, "threads": threads}
    except (OSError, IndexError, ValueError):
        return None


# ---------------------------------------------------------------- kernel
def fc_version():
    def f():
        m = re.search(r"v?(\d+)\.(\d+)\.(\d+)", out([C.FC, "--version"]))
        return ".".join(m.groups()) if m else None
    return cached("fcver", 60, f)


def kernel_version():
    p = C.KERNEL
    if not p.exists():
        return None
    key = ("kv", p.stat().st_mtime_ns)
    return cached(key, 3600, lambda: _kver(p))


def _kver(p):
    try:
        with open(p, "rb") as f, mmap.mmap(f.fileno(), 0, access=mmap.ACCESS_READ) as mm:
            m = re.search(rb"Linux version (\d+\.\d+\.\d+[^\s]*)", mm)
            return m.group(1).decode() if m else None
    except (OSError, ValueError):
        return None


def kernel_info():
    v = kernel_version()
    legacy = False
    if v:
        try:
            legacy = int(v.split(".")[0]) < 5
        except ValueError:
            pass
    sha = None
    if C.KERNEL.exists():
        sha = cached(("ks", C.KERNEL.stat().st_mtime_ns), 3600, lambda: sha256_file(C.KERNEL))
    return {"present": C.KERNEL.exists(), "version": v, "legacy": legacy, "sha256": sha,
            "size": C.KERNEL.stat().st_size if C.KERNEL.exists() else 0, "path": str(C.KERNEL)}


def _s3_list(prefix, delimiter=None):
    q = {"list-type": "2", "prefix": prefix}
    if delimiter:
        q["delimiter"] = delimiter
    qs = urllib.parse.urlencode(q)
    last, root = None, None
    for base in (f"https://s3.amazonaws.com/{C.S3_BUCKET}/?", f"https://{C.S3_BUCKET}.s3.amazonaws.com/?"):
        try:
            with urllib.request.urlopen(base + qs, timeout=30) as r:
                root = ET.fromstring(r.read())
            break
        except Exception as e:  # noqa: BLE001 - report whatever the network did
            last = e
    if root is None:
        raise RuntimeError(f"cannot list the Firecracker CI bucket: {last}")
    ns = root.tag.split("}")[0] + "}" if root.tag.startswith("{") else ""
    keys = [e.text for e in root.iter(f"{ns}Key") if e.text]
    cps = [e.find(f"{ns}Prefix").text for e in root.iter(f"{ns}CommonPrefixes") if e.find(f"{ns}Prefix") is not None]
    return keys, cps


def _ci_versions(log):
    versions = []
    fv = fc_version()
    if fv:
        versions.append("v" + ".".join(fv.split(".")[:2]))
    try:
        _, cps = _s3_list("firecracker-ci/", "/")
        found = {p.rstrip("/").split("/")[-1] for p in cps}
        versions += sorted((x for x in found if re.fullmatch(r"v\d+\.\d+", x)),
                           key=lambda s: tuple(int(i) for i in s[1:].split(".")), reverse=True)
    except RuntimeError as e:
        log(f"WARN: {e}")
    seen = []
    for v in versions:
        if v not in seen:
            seen.append(v)
    return seen[:8]


def resolve_kernel_url(log):
    if C.KERNEL_URL:
        return C.KERNEL_URL
    for ver in _ci_versions(log):
        try:
            keys, _ = _s3_list(f"firecracker-ci/{ver}/{C.FC_ARCH}/vmlinux-")
        except RuntimeError as e:
            log(f"WARN: {e}")
            continue
        cand = []
        for k in keys:
            m = re.fullmatch(r".*/vmlinux-(\d+)\.(\d+)\.(\d+)", k)
            if m:
                cand.append((tuple(int(x) for x in m.groups()), k))
        if cand:
            pick = [c for c in cand if c[0][:2] == (6, 1)] or cand      # 6.1 is the longest supported guest kernel
            return f"https://s3.amazonaws.com/{C.S3_BUCKET}/{max(pick)[1]}"
    raise RuntimeError("could not find a kernel in the Firecracker CI bucket - set MVM_KERNEL_URL or MVM_KERNEL")


def resolve_rootfs_url(log):
    """Official Firecracker CI Ubuntu rootfs (squashfs)."""
    for ver in _ci_versions(log):
        try:
            keys, _ = _s3_list(f"firecracker-ci/{ver}/{C.FC_ARCH}/ubuntu-")
        except RuntimeError as e:
            log(f"WARN: {e}")
            continue
        cand = []
        for k in keys:
            m = re.fullmatch(r".*/ubuntu-(\d+)\.(\d+)\.squashfs", k)
            if m:
                cand.append((tuple(int(x) for x in m.groups()), k))
        if cand:
            return f"https://s3.amazonaws.com/{C.S3_BUCKET}/{max(cand)[1]}"
    raise RuntimeError("no official Ubuntu rootfs found in the Firecracker CI bucket")


def download(url, dest, log, min_size=1 << 20, max_size=8 << 30):
    if not url.startswith("https://"):
        raise UserError("only https downloads are allowed")
    log(f"download {url}")
    tmp = Path(str(dest) + ".part")
    done = 0
    with urllib.request.urlopen(url, timeout=60) as r, open(tmp, "wb") as f:
        while True:
            chunk = r.read(1 << 20)
            if not chunk:
                break
            done += len(chunk)
            if done > max_size:
                tmp.unlink(missing_ok=True)
                raise RuntimeError("download too large")
            f.write(chunk)
    if done < min_size:
        tmp.unlink(missing_ok=True)
        raise RuntimeError(f"download too small ({done} bytes)")
    os.replace(tmp, dest)
    return done


def fetch_kernel(log, force=False):
    cur = kernel_info()
    if cur["present"] and not force and not cur["legacy"]:
        log(f"kernel already present ({cur['version']})")
        return ["kernel"]
    url = resolve_kernel_url(log)
    C.KERNEL.parent.mkdir(parents=True, exist_ok=True)
    download(url, C.KERNEL, log)
    sha = sha256_file(C.KERNEL)
    if C.KERNEL_SHA256 and sha != C.KERNEL_SHA256.lower():
        C.KERNEL.unlink(missing_ok=True)
        raise RuntimeError(f"kernel sha256 mismatch (got {sha})")
    _cache.clear()
    log(f"kernel saved: {kernel_version() or '?'}  sha256 {sha[:16]}…")
    return ["kernel"]


# ---------------------------------------------------------------- health
def kvm_ok():
    return C.SMOKE or os.access("/dev/kvm", os.R_OK | os.W_OK)


def host_capacity():
    mem = 0
    try:
        for line in Path("/proc/meminfo").read_text().splitlines():
            if line.startswith("MemTotal:"):
                mem = int(line.split()[1]) * 1024
    except OSError:
        pass
    return {"cpus": os.cpu_count() or 0, "mem": mem}


def health():
    tools = {t: have(t) for t in ("skopeo", "umoci", "mke2fs", "e2fsck", "resize2fs", "git", "fakeroot", "unsquashfs")}
    tools["firecracker"] = have(C.FC)
    h = helper_info()
    problems = []
    if not kvm_ok():
        problems.append("/dev/kvm is not accessible - add your user to the 'kvm' group and log in again")
    if not tools["firecracker"]:
        problems.append("firecracker is not installed - run ./install.sh")
    if not C.GUEST_BIN.exists():
        problems.append("guest agent not built - run ./install.sh (needs gcc) or: make -C guest")
    if not kernel_info()["present"]:
        problems.append("no guest kernel - press 'Get kernel'")
    elif kernel_info()["legacy"]:
        problems.append("guest kernel is older than 5.x - press 'Upgrade kernel'")
    if not h:
        problems.append("root helper missing: VMs will have no network and no jailer - run ./install.sh")
    elif not Path(f"/sys/class/net/{C.BRIDGE}").exists():
        problems.append(f"bridge {C.BRIDGE} missing - run: sudo {C.PRIV} fw-apply")
    for t in ("skopeo", "umoci", "mke2fs"):
        if not tools[t]:
            problems.append(f"missing tool: {t} (needed to build images)")
    try:
        md = mode()
    except RuntimeError as e:
        md = "error"
        problems.append(str(e))
    if md == "direct":
        problems.append("running WITHOUT the jailer (direct mode). Install it for chroot/cgroup/namespace isolation")
    return {"version": C.VERSION, "arch": C.MACH, "mode": md, "kvm": kvm_ok(), "tools": tools,
            "firecracker": fc_version(), "kernel": kernel_info(), "helper": bool(h),
            "guest_agent": C.GUEST_BIN.exists(), "net": net_status() if h else {"helper": False},
            "data": str(C.DATA), "problems": problems, "readonly": C.READONLY, "exec_enabled": not C.DISABLE_EXEC,
            "cgroup_v2": bool(h and h.get("cgroup_v2")), "capacity": host_capacity(), "hostname": os.uname().nodename, "bridge": C.BRIDGE, "subnet": f"{C.NET}.0/24"}

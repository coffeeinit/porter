"""Small, dependency-free helpers. Nothing here ever builds a shell string."""
import hashlib
import os
import re
import shutil
import subprocess
import time
from datetime import datetime, timezone
from pathlib import Path


class UserError(ValueError):
    """Bad input from the client (HTTP 400)."""


class NotFound(KeyError):
    """Unknown object (HTTP 404)."""


class Conflict(RuntimeError):
    """State conflict, e.g. VM already running (HTTP 409)."""


def now():
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def age_s(iso):
    try:
        return (datetime.now(timezone.utc) - datetime.fromisoformat(iso)).total_seconds()
    except (TypeError, ValueError):
        return 1e9


def slug(s, maxlen=48):
    return re.sub(r"[^a-z0-9._]+", "-", str(s).lower()).strip("-.")[:maxlen] or "x"


NAME_RE = re.compile(r"^[a-z0-9][a-z0-9._-]{0,47}$")
VID_RE = re.compile(r"^[a-f0-9]{8}$")


def valid_name(name, what="name"):
    name = str(name or "").strip().lower()
    if not NAME_RE.match(name):
        raise UserError(f"invalid {what}: use 1-48 chars of a-z 0-9 . _ - (start with a letter or digit)")
    return name


def valid_vid(vid):
    if not VID_RE.match(str(vid or "")):
        raise NotFound(vid)
    return vid


def int_in(v, lo, hi, what):
    try:
        n = int(v)
    except (TypeError, ValueError):
        raise UserError(f"{what} must be a number") from None
    if not lo <= n <= hi:
        raise UserError(f"{what} must be between {lo} and {hi}")
    return n


def norm_ref(ref):
    """Normalise an OCI image reference and reject anything odd."""
    ref = (ref or "").strip().removeprefix("docker://")
    if not ref or len(ref) > 255 or not re.fullmatch(r"[A-Za-z0-9._:/@-]+", ref) or ref.startswith(("-", "/")) or ".." in ref:
        raise UserError(f"bad image reference: {ref!r}")
    first = ref.split("/")[0]
    if "/" not in ref:
        ref = "docker.io/library/" + ref
    elif not ("." in first or ":" in first or first == "localhost"):
        ref = "docker.io/" + ref
    return ref


def short_ref(ref):
    return slug(ref.removeprefix("docker.io/library/").removeprefix("docker.io/"))


def run(argv, timeout=120, check=True, input=None, env=None, cwd=None):
    """Run a command (argv list only). Returns CompletedProcess (text mode)."""
    try:
        r = subprocess.run([str(a) for a in argv], capture_output=True, text=True, timeout=timeout,
                           input=input, env=env, cwd=cwd)
    except FileNotFoundError:
        raise RuntimeError(f"{argv[0]} is not installed (run ./install.sh)") from None
    except subprocess.TimeoutExpired:
        raise RuntimeError(f"{argv[0]} timed out after {timeout}s") from None
    if check and r.returncode:
        raise RuntimeError(f"{' '.join(str(a) for a in argv[:2])} failed: {(r.stderr or r.stdout).strip()[-600:]}")
    return r


def out(argv, timeout=10):
    try:
        return subprocess.run([str(a) for a in argv], capture_output=True, text=True, timeout=timeout).stdout
    except (OSError, subprocess.SubprocessError):
        return ""


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def reflink_copy(src, dst):
    """Sparse, copy-on-write when the filesystem supports it."""
    run(["cp", "--reflink=auto", "--sparse=always", str(src), str(dst)], timeout=900)


def tail_file(path, nbytes=60000):
    p = Path(path)
    if not p.exists():
        return ""
    with open(p, "rb") as f:
        f.seek(0, 2)
        f.seek(max(0, f.tell() - nbytes))
        return f.read().decode("utf-8", "replace")


def human(n):
    n = float(n or 0)
    for u in ("B", "KiB", "MiB", "GiB", "TiB"):
        if n < 1024 or u == "TiB":
            return f"{n:.0f} {u}" if u == "B" else f"{n:.1f} {u}"
        n /= 1024


def wait_for(fn, timeout=10.0, interval=0.1):
    end = time.time() + timeout
    while time.time() < end:
        v = fn()
        if v:
            return v
        time.sleep(interval)
    return None


def port_numbers(declared):
    res = []
    for p in declared or []:
        s = str(p)
        if s.endswith("/udp"):
            continue
        m = re.match(r"\d+", s)
        if m and int(m.group()) not in res:
            res.append(int(m.group()))
    return res


def disk_free(path):
    try:
        return shutil.disk_usage(path).free
    except OSError:
        return 0


def secure_write(path, text, mode=0o600):
    p = Path(path)
    tmp = p.with_name(p.name + ".tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode)
    with os.fdopen(fd, "w") as f:
        f.write(text)
    os.replace(tmp, p)

"""All settings. Everything is overridable with MVM_* environment variables."""
import os
import platform
from pathlib import Path

VERSION = "3.5.0-beta"
BASE = Path(__file__).resolve().parent.parent


def _e(name, default=None):
    v = os.environ.get("MVM_" + name)
    return v if v not in (None, "") else default


def _b(name, default=False):
    v = _e(name)
    return default if v is None else v.lower() in ("1", "true", "yes", "on")


MACH = platform.machine()
ARCH = {"x86_64": "amd64", "aarch64": "arm64"}.get(MACH, "amd64")
FC_ARCH = "aarch64" if MACH == "aarch64" else "x86_64"

HOST = _e("HOST", "127.0.0.1")
PORT = int(_e("PORT", "8080"))
DATA = Path(_e("DATA", str(BASE / "mvm-data"))).expanduser().resolve()
RUNTIME = Path(_e("RUNTIME", f"/tmp/mvm-{os.getuid()}"))          # short path: unix sockets are limited to 107 bytes

FC = _e("FC", "firecracker")
JAILER = _e("JAILER", "jailer")
MODE = _e("MODE", "auto")                                          # auto | jailer | direct
PRIV = _e("PRIV_HELPER", "/usr/local/sbin/mvm-priv")               # tiny root helper (TAP, jailer, firewall)
JAIL_BASE = Path(_e("CHROOT_BASE", "/srv/jailer"))
GUEST_BIN = Path(_e("GUEST_BIN", str(BASE / "guest" / "mvm-guest")))
KERNEL = Path(_e("KERNEL", str(DATA / "vmlinux")))
KERNEL_URL = _e("KERNEL_URL")
KERNEL_SHA256 = _e("KERNEL_SHA256")
S3_BUCKET = "spec.ccfc.min"

BRIDGE = _e("BRIDGE", "mvmbr0")
NET = _e("NET", "172.30.0")                                        # /24, gateway .1, guests .100-.254
DNS = [x.strip() for x in _e("DNS", "1.1.1.1,8.8.8.8").split(",") if x.strip()]
IP_FIRST, IP_LAST = 100, 254

ALLOWED_HOSTS = {"localhost", "127.0.0.1", "::1", HOST.lower()} | {
    h.strip().lower() for h in _e("ALLOWED_HOSTS", "").split(",") if h.strip()}
TLS_CERT, TLS_KEY = _e("TLS_CERT"), _e("TLS_KEY")
DISABLE_EXEC = _b("DISABLE_EXEC", False)
READONLY = _b("READONLY", False)                                   # UI can look but not change anything
STOP_ON_EXIT = _b("STOP_ON_EXIT", False)                           # VMs survive a restart of the web app by default
MAX_VMS = int(_e("MAX_VMS", "64"))
MAX_VCPU = int(_e("MAX_VCPU", "32"))
MAX_MEM_MIB = int(_e("MAX_MEM_MIB", "65536"))
CGROUPS = _b("CGROUPS", True)                                      # cpu/memory/pids limits through the jailer (cgroup v2)
TRAEFIK = _e("TRAEFIK", "traefik")
INGRESS_HTTP = int(_e("INGRESS_HTTP", "80"))
INGRESS_HTTPS = int(_e("INGRESS_HTTPS", "443"))
MAX_VOLUMES_PER_VM = 4
# builder VMs (BuildKit) - separate from production VMs
BUILDER_IMAGE = "_buildkit"                                        # internal image, hidden from the image list
BUILDER_CPU = int(_e("BUILDER_CPU", "2"))
BUILDER_MEM = int(_e("BUILDER_MEM", "3072"))                       # MiB
BUILDER_DISK = int(_e("BUILDER_DISK", "6144"))                     # MiB of scratch disk besides the cache volume
BUILD_TIMEOUT = int(_e("BUILD_TIMEOUT", "3600"))                   # seconds per build
BUILDKIT_VERSION = _e("BUILDKIT_VERSION")                          # e.g. v0.35.0 (default: latest release)
BUILDKIT_SHA256 = _e("BUILDKIT_SHA256")                            # optional pin of the BuildKit tarball
RAILPACK_VERSION = _e("RAILPACK_VERSION")
BUILD_CACHE_MB = int(_e("BUILD_CACHE_MB", "10240"))                # persistent BuildKit cache volume (sparse)
BUILD_CONTEXT_MAX_MB = int(_e("BUILD_CONTEXT_MAX_MB", "2048"))
BUILD_PLATFORM = "linux/arm64" if MACH == "aarch64" else "linux/amd64"
FC_PASSENV = [x for x in (_e("FC_PASSENV", "") or "").split(",") if x]            # test harness only: env vars handed to the VMM process
SMOKE = _b("SMOKE", False)                                         # test-suite switch: skip host checks

IMG, OCI, VMS, SNAP, BUILD, VOL, BKP, ING = (DATA / d for d in ("images", "oci", "vms", "snapshots", "build", "volumes", "backups", "ingress"))
DB_PATH = DATA / "mvm.sqlite3"
AUTH_FILE = DATA / "auth.json"

STORE = [
    {"ref": "nginx:alpine", "title": "Nginx", "desc": "Web server / reverse proxy"},
    {"ref": "redis:alpine", "title": "Redis", "desc": "In-memory key-value store"},
    {"ref": "postgres:17-alpine", "title": "PostgreSQL 17", "desc": "SQL database"},
    {"ref": "caddy:alpine", "title": "Caddy", "desc": "Web server with automatic HTTPS"},
    {"ref": "python:3-alpine", "title": "Python 3", "desc": "Python runtime"},
    {"ref": "node:lts-alpine", "title": "Node.js LTS", "desc": "Node runtime"},
    {"ref": "mariadb:lts", "title": "MariaDB", "desc": "SQL database"},
    {"ref": "alpine:3", "title": "Alpine Linux", "desc": "Minimal bare microVM (shell only)"},
]

CPU_TEMPLATES = ["None", "T2", "T2S", "T2CL", "T2A", "C3", "V1N1"]


def ensure_dirs():
    DATA.mkdir(parents=True, exist_ok=True)
    os.chmod(DATA, 0o700)
    for d in (IMG, OCI, VMS, SNAP, BUILD, VOL, BKP, ING):
        d.mkdir(parents=True, exist_ok=True)
    RUNTIME.mkdir(parents=True, exist_ok=True)
    os.chmod(RUNTIME, 0o700)

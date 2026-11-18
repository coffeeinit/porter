"""BuildKit engine. Dockerfile (full syntax: multi-stage, COPY --from, ARG, targets) and Railpack builds.

Nothing from a build ever runs on the host: every build gets a TEMPORARY FIRECRACKER VM that contains
buildkitd + buildctl + runc (+ railpack). The VM is destroyed afterwards; only the exported OCI image comes out and
goes through the normal importer (skopeo -> rootfs -> ext4 + mvm-guest). A persistent volume keeps BuildKit's layer
cache between builds.

    source -> context tar.gz --vsock--> builder VM -> buildctl (dockerfile.v0 | railpack gateway) -> OCI archive
           <--vsock-- out.tar -> images.build_oci(from_archive=...) -> ext4 -> Firecracker runtime
"""
import fnmatch
import hashlib
import json
import os
import re
import shlex
import shutil
import tarfile
import tempfile
import time
import urllib.request
from pathlib import Path

from . import agent, config as C, db, host
from .util import Conflict, NotFound, UserError, wait_for

SOCK = "unix:///run/buildkit/buildkitd.sock"
CACHE_VOLUME = "buildkit-cache"


# ------------------------------------------------------------------ builder image
def _latest_tag(repo):
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *a, **k):
            return None
    op = urllib.request.build_opener(NoRedirect)
    try:
        op.open(urllib.request.Request(f"https://github.com/{repo}/releases/latest", method="HEAD"), timeout=30)
    except urllib.error.HTTPError as e:
        loc = e.headers.get("Location", "")
        m = re.search(r"/tag/([^/?#]+)$", loc)
        if m:
            return m.group(1)
    raise RuntimeError(f"could not determine the latest release of {repo} (set MVM_BUILDKIT_VERSION / MVM_RAILPACK_VERSION)")


def _sha(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for c in iter(lambda: f.read(1 << 20), b""):
            h.update(c)
    return h.hexdigest()


def builder_info():
    row = db.get("images", C.BUILDER_IMAGE, "name")
    return {"present": bool(row), "notes": (row or {}).get("notes", ""), "size_mb": (row or {}).get("size_mb"), "created": (row or {}).get("created"),
            "cpu": C.BUILDER_CPU, "mem_mib": C.BUILDER_MEM, "disk_mib": C.BUILDER_DISK, "timeout_s": C.BUILD_TIMEOUT,
            "cache": cache_info()}


def cache_info():
    v = db.get("volumes", CACHE_VOLUME, "name")
    if not v:
        return {"exists": False, "size_mb": C.BUILD_CACHE_MB}
    p = C.VOL / f"{v['id']}.ext4"
    return {"exists": True, "size_mb": v["size_mb"], "allocated": p.stat().st_blocks * 512 if p.exists() else 0}


def reset_cache():
    from . import volumes as V
    v = db.get("volumes", CACHE_VOLUME, "name")
    if v:
        V.delete(v["id"])
    return cache_info()


def _extract(t, name, dest):
    try:
        t.extract(name, dest, filter="data")
    except TypeError:                      # Python < 3.11.4 has no extraction filters
        t.extract(name, dest)


def ensure_builder_image(log, force=False):
    from . import images
    if db.get("images", C.BUILDER_IMAGE, "name") and not force:
        return
    arch = "arm64" if C.MACH == "aarch64" else "amd64"
    rp_arch = "aarch64" if C.MACH == "aarch64" else "x86_64"
    bk = C.BUILDKIT_VERSION or _latest_tag("moby/buildkit")
    rp = C.RAILPACK_VERSION or _latest_tag("railwayapp/railpack")
    log(f"assembling the builder image: BuildKit {bk}, Railpack {rp}")
    work = Path(tempfile.mkdtemp(dir=C.BUILD))
    try:
        bk_tgz, rp_tgz = work / "bk.tgz", work / "rp.tgz"
        host.download(f"https://github.com/moby/buildkit/releases/download/{bk}/buildkit-{bk}.linux-{arch}.tar.gz", bk_tgz, log, min_size=10 << 20)
        if C.BUILDKIT_SHA256 and _sha(bk_tgz) != C.BUILDKIT_SHA256.lower():
            raise RuntimeError("BuildKit tarball sha256 does not match MVM_BUILDKIT_SHA256")
        asset = f"railpack-{rp}-{rp_arch}-unknown-linux-musl.tar.gz"
        host.download(f"https://github.com/railwayapp/railpack/releases/download/{rp}/{asset}", rp_tgz, log, min_size=1 << 20)
        host.download(f"https://github.com/railwayapp/railpack/releases/download/{rp}/checksums.txt", work / "rp.sums", log, min_size=10)
        want = next((l.split()[0] for l in (work / "rp.sums").read_text().splitlines() if asset in l), None)
        if not want or _sha(rp_tgz) != want:
            raise RuntimeError("Railpack tarball failed its checksum verification")
        files = []
        with tarfile.open(bk_tgz) as t:
            for n, dst in (("bin/buildctl", "/usr/local/bin/buildctl"), ("bin/buildkitd", "/usr/local/bin/buildkitd"), ("bin/buildkit-runc", "/usr/local/bin/buildkit-runc")):
                _extract(t, n, work)
                files.append({"src": str(work / n), "dst": dst, "mode": 0o755})
        with tarfile.open(rp_tgz) as t:
            _extract(t, "railpack", work)
        files.append({"src": str(work / "railpack"), "dst": "/usr/local/bin/railpack", "mode": 0o755})
        if C.BUILDKIT_SHA256 is None:
            log(f"BuildKit tarball sha256 {_sha(bk_tgz)[:16]}… (pin it with MVM_BUILDKIT_SHA256)")
        args = ["/usr/local/bin/buildkitd", "--addr", SOCK, "--root", "/var/lib/buildkit", "--oci-worker-snapshotter=overlayfs",
                "--oci-worker-net=host", f"--oci-worker-gc-keepstorage={max(1024, C.BUILD_CACHE_MB - 1024)}"]
        old = db.get("images", C.BUILDER_IMAGE, "name")
        tmp = f"_t{os.urandom(3).hex()}"
        images.build_oci(tmp, "docker.io/library/alpine:3", log, entrypoint=args, cmd=[], env={"BUILDKIT_HOST": SOCK, "RAILPACK_VERSION_PIN": rp},
                         extra_mb=512, source="builder", hostname="builder", extra_files=files, refresh=True)
        images.replace_image(tmp, C.BUILDER_IMAGE)
        db.update("images", C.BUILDER_IMAGE, {"notes": f"BuildKit {bk} · Railpack {rp}"}, pk="name")
        log(f"builder image ready ({bk}, {rp})")
    finally:
        shutil.rmtree(work, ignore_errors=True)


# ------------------------------------------------------------------ build context
def _ignore_rules(ctx):
    f = Path(ctx) / ".dockerignore"
    rules = []
    if f.is_file():
        for line in f.read_text(errors="replace").splitlines():
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            neg = line.startswith("!")
            pat = line[1:] if neg else line
            pat = pat.strip("/").removeprefix("./")
            rx = ""
            i = 0
            while i < len(pat):
                if pat.startswith("**/", i):
                    rx += "(?:.*/)?"
                    i += 3
                elif pat.startswith("**", i):
                    rx += ".*"
                    i += 2
                elif pat[i] == "*":
                    rx += "[^/]*"
                    i += 1
                elif pat[i] == "?":
                    rx += "[^/]"
                    i += 1
                else:
                    rx += re.escape(pat[i])
                    i += 1
            rules.append((neg, re.compile(f"^{rx}(?:/.*)?$")))
    return rules


def _excluded(rel, rules):
    out = False
    for neg, rx in rules:
        if rx.match(rel):
            out = not neg
    return out


def make_context(ctx, dest, keep=()):
    """tar.gz of the build context, honouring .dockerignore (the Dockerfile itself is always kept)."""
    ctx = Path(ctx).resolve()
    rules = _ignore_rules(ctx)
    total = 0
    with tarfile.open(dest, "w:gz", compresslevel=3) as t:
        for dp, dns, fns in os.walk(ctx):
            rel_dir = os.path.relpath(dp, ctx)
            dns[:] = [d for d in dns if not (_excluded(os.path.join(rel_dir, d).removeprefix("./"), rules)
                                             and not any(r[0] for r in rules))]          # cannot prune a dir that has "!" exceptions
            for fn in fns + [d for d in dns if os.path.islink(os.path.join(dp, d))]:
                full = os.path.join(dp, fn)
                rel = os.path.relpath(full, ctx)
                if rel in keep or not _excluded(rel, rules):
                    if os.path.islink(full) or os.path.isfile(full):
                        total += os.lstat(full).st_size
                        if total > C.BUILD_CONTEXT_MAX_MB * 1048576:
                            raise UserError(f"build context larger than {C.BUILD_CONTEXT_MAX_MB} MiB - add a .dockerignore (node_modules, .git, build outputs)")
                        t.add(full, arcname=rel, recursive=False)
    return total


# ------------------------------------------------------------------ the build
def _x(sock, cmd, timeout=60, wd="/"):
    return agent.exec_(sock, cmd, wd, timeout)


def _builder_ready(sock, vid, log):
    from . import vmm
    if not wait_for(lambda: _ping(sock), 150, 0.5):
        raise RuntimeError("builder VM did not come up. Console:\n" + vmm.console_tail(vid, 2500))
    ok = wait_for(lambda: _x(sock, f"buildctl --addr {SOCK} debug workers", 15)["exit_code"] == 0, 120, 2)
    if not ok:
        raise RuntimeError("buildkitd did not become ready (does the guest kernel support OverlayFS and cgroups?). Console:\n" + vmm.console_tail(vid, 3000))
    log("builder VM ready (buildkitd answers)")


def _ping(sock):
    try:
        return agent.ping(sock, 2)
    except Exception:  # noqa: BLE001
        return None


def _cache_volume(log):
    """Persistent layer cache. Created on first use; failure just means 'no cache', never a failed build."""
    from . import volumes as V
    try:
        v = V.find(CACHE_VOLUME)
        if not v:
            v = V.create(CACHE_VOLUME, C.BUILD_CACHE_MB, note="BuildKit layer cache (safe to delete: builds get slower once)")
        if v["attached_to"]:
            V.detach(v["id"])
        return v
    except Exception as e:  # noqa: BLE001
        log(f"WARN: no persistent build cache ({e})")
        return None


def build(ctx_dir, log, *, frontend="dockerfile", dockerfile="Dockerfile", target=None, build_args=None, platform=None, no_cache=False):
    """Returns (oci_archive_path, info). ctx_dir is a directory on the host; Dockerfile path is relative to it."""
    from . import vmm, volumes as V
    if not host.net_enabled() and not C.SMOKE:
        raise RuntimeError("builds need the network helper for the builder VM (run ./install.sh)")
    if frontend not in ("dockerfile", "railpack"):
        raise UserError("frontend must be dockerfile or railpack")
    dockerfile = (dockerfile or "Dockerfile").lstrip("/")
    if frontend == "dockerfile" and not (Path(ctx_dir) / dockerfile).is_file():
        raise UserError(f"{dockerfile} not found in the build context")
    ensure_builder_image(log)
    work = Path(tempfile.mkdtemp(dir=C.BUILD))
    vid = None
    try:
        tarball = work / "context.tar.gz"
        size = make_context(ctx_dir, tarball, keep=(dockerfile,))
        log(f"build context packed: {size / 1048576:.1f} MiB (before compression)")
        vol = _cache_volume(log)
        vm = vmm.create_vm(C.BUILDER_IMAGE, name=None, vcpu=C.BUILDER_CPU, mem=C.BUILDER_MEM, disk_mb=C.BUILDER_DISK, hidden=True,
                           config={"net": True, "entropy": True})
        vid = vm["id"]
        if vol:
            try:
                V.attach(vol["id"], vid, "/var/lib/buildkit")
            except Exception as e:  # noqa: BLE001
                log(f"WARN: cache volume not attached ({e})")
        log(f"builder microVM starting ({C.BUILDER_CPU} vCPU, {C.BUILDER_MEM} MiB)")
        vmm.start_vm(vid)
        sock = vmm.agent_sock(vid)
        _builder_ready(sock, vid, log)

        with open(tarball, "rb") as f:
            agent.put_stream(sock, "/build/context.tar.gz", f, tarball.stat().st_size)
        r = _x(sock, "mkdir -p /build/ctx /build/plan && tar -xzf /build/context.tar.gz -C /build/ctx", 300)
        if r["exit_code"]:
            raise RuntimeError("could not unpack the context in the builder: " + (r["stderr"] or r["stdout"])[-300:])
        log("context delivered to the builder VM")

        plat = platform or C.BUILD_PLATFORM
        opts = [f"--opt platform={shlex.quote(plat)}"]
        info = {"frontend": frontend}
        if frontend == "dockerfile":
            ddir = os.path.dirname(dockerfile) or "."
            cmd = [f"buildctl --addr {SOCK} build --frontend dockerfile.v0 --local context=/build/ctx",
                   f"--local dockerfile={shlex.quote('/build/ctx/' + ddir)} --opt filename={shlex.quote(os.path.basename(dockerfile))}"]
            if target:
                opts.append(f"--opt target={shlex.quote(target)}")
        else:
            envs = " ".join(f"--env {shlex.quote(k + '=' + str(v))}" for k, v in (build_args or {}).items())
            log("railpack: analysing the project (framework detection)")
            r = _x(sock, f"railpack prepare /build/ctx --plan-out /build/plan/railpack-plan.json --info-out /build/plan/railpack-info.json {envs}", 300)
            for line in (r["stdout"] + r["stderr"]).splitlines()[-30:]:
                log("  | " + line)
            if r["exit_code"]:
                raise RuntimeError("railpack could not build a plan for this project: " + (r["stderr"] or r["stdout"])[-300:])
            try:
                pinfo = json.loads(agent.get_file(sock, "/build/plan/railpack-info.json", 4 << 20))
                info["railpack"] = pinfo
                det = pinfo.get("detectedProviders") or pinfo.get("providers") or []
                log(f"railpack detected: {', '.join(map(str, det)) or 'unknown'}")
            except Exception:  # noqa: BLE001
                pass
            ver = re.search(r"(\d+\.\d+\.\d+)", _x(sock, "railpack --version", 15)["stdout"])
            tag = f"v{ver.group(1)}" if ver else "latest"
            cmd = [f"buildctl --addr {SOCK} build --frontend gateway.v0 --opt source=ghcr.io/railwayapp/railpack-frontend:{tag}",
                   "--local context=/build/ctx --local dockerfile=/build/plan"]
        if no_cache:
            opts.append("--no-cache")
        if frontend == "dockerfile":
            for k, v in (build_args or {}).items():
                if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", str(k)):
                    raise UserError(f"bad build argument name: {k!r}")
                opts.append(f"--opt build-arg:{k}={shlex.quote(str(v))}")
        full = " ".join(cmd + opts) + " --progress=plain --output type=oci,dest=/build/out.tar"
        _run_with_logs(sock, full, log)

        out = work / "image.tar"
        n = agent.get_stream(sock, "/build/out.tar", out)
        log(f"image exported: {n / 1048576:.1f} MiB (OCI archive)")
        keep = Path(tempfile.mkdtemp(dir=C.BUILD)) / "image.tar"
        shutil.move(str(out), keep)
        return keep, info
    finally:
        if vid:
            try:
                vmm.delete_vm(vid)
            except Exception as e:  # noqa: BLE001
                log(f"WARN: builder cleanup: {e}")
        shutil.rmtree(work, ignore_errors=True)


def _run_with_logs(sock, cmd, log):
    """Start buildctl in the background inside the VM and stream its log until it exits (survives long builds, enforces the timeout)."""
    inner = f"{cmd} > /build/build.log 2>&1; echo $? > /build/exit"
    _x(sock, f"rm -f /build/exit /build/build.log; (setsid sh -c {shlex.quote(inner)} </dev/null >/dev/null 2>&1 &); echo started", 30)
    log("build started in the builder VM")
    end, tail = time.time() + C.BUILD_TIMEOUT, []
    state = {"off": 0}

    def read_log():
        """Fetch new log bytes (+ the exit code, if any). Returns the exit code string or ''."""
        r = _x(sock, f"tail -c +{state['off'] + 1} /build/build.log 2>/dev/null | head -c 65536; echo; echo '@@EXIT@@'; cat /build/exit 2>/dev/null", 30)
        text, _, ex = r["stdout"].rpartition("@@EXIT@@")
        chunk = text.rstrip("\n")
        if chunk.strip():
            state["off"] += len(text.encode("utf-8", "replace")) - 1
            for line in chunk.splitlines():
                tail.append(line)
                log("  | " + line[:300])
            del tail[:-40]
        return ex.strip()

    while True:
        code = read_log()
        if code != "":
            read_log()                       # the exit code can be written a moment before the last log lines are flushed
            if int(code) != 0:
                raise RuntimeError(f"build failed (exit {code}):\n" + "\n".join(tail[-25:]))
            return
        if time.time() > end:
            _x(sock, "pkill -f 'buildctl' || true", 15)
            raise RuntimeError(f"build timed out after {C.BUILD_TIMEOUT}s")
        time.sleep(3)

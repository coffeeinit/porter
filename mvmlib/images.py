"""Image pipeline: OCI registry / Dockerfile / Compose / Git / tarball / official Firecracker rootfs
-> ext4 root filesystem with the static mvm-guest init + agent injected.

Nothing from an image is ever executed on the host. Dockerfile RUN steps run inside a throw-away
builder microVM (hardware isolation), never in a host chroot."""
import json
import os
import re
import shlex
import shutil
import tempfile
import uuid
from pathlib import Path

import yaml

from . import config as C, db, host
from .util import UserError, Conflict, NotFound, int_in, norm_ref, now, out, run, sha256_file, short_ref, slug, valid_name, wait_for

TOOL = Path(__file__).resolve().parent / "rootfs_tool.py"


# ---------------------------------------------------------------- catalog
def store():
    items = list(C.STORE)
    try:
        items += json.loads((C.DATA / "store.json").read_text())
    except (OSError, ValueError):
        pass
    res = []
    for s in items:
        try:
            name = short_ref(norm_ref(s["ref"]))
        except UserError:
            continue
        res.append({**s, "name": name, "built": bool(db.get("images", name, "name"))})
    return res


def list_images():
    return [i for i in db.list_("images") if not i["name"].startswith("_")]


def get_image(name):
    i = db.get("images", name, "name")
    if not i:
        raise NotFound(name)
    return i


def delete_image(name):
    get_image(name)
    users = [v["name"] for v in db.list_("vms", "WHERE image=? AND hidden=0", (name,))]
    if users:
        raise Conflict(f"image is used by VM(s): {', '.join(users)}")
    shutil.rmtree(C.IMG / name, ignore_errors=True)
    db.delete("images", name, "name")


# ---------------------------------------------------------------- fakeroot wrapper
class Fake:
    """Run tools so that file ownership survives into the ext4 image (fakeroot keeps a state file)."""

    def __init__(self, work, log):
        self.state, self.first, self.on = Path(work) / "fakeroot.state", True, host.have("fakeroot")
        if not self.on:
            log("WARN: fakeroot not installed - every file in the image will be owned by the build user (apt install fakeroot)")

    def run(self, argv, write=True, timeout=1800):
        argv = [str(a) for a in argv]
        if not self.on:
            return run(argv, timeout=timeout)
        pre = ["fakeroot"]
        if not self.first:
            pre += ["-i", str(self.state)]
        if write:
            pre += ["-s", str(self.state)]
        self.first = False if write else self.first
        return run([*pre, "--", *argv], timeout=timeout)


def _need(*tools):
    miss = [t for t in tools if not host.have(t)]
    if miss:
        raise RuntimeError(f"missing tool(s): {', '.join(miss)} - run ./install.sh")


def _guest_bin():
    if not C.GUEST_BIN.exists():
        raise RuntimeError("guest agent binary missing - run ./install.sh (needs gcc) or `make -C guest`")
    return str(C.GUEST_BIN)


def ext4_size_mb(used_bytes, extra_mb=64):
    """Return a right-sized ext4 capacity for the extracted rootfs.

    Keep 25% growth headroom, 48 MiB for filesystem/runtime overhead, and the
    configurable extra free space. Round up to 16 MiB so image sizes stay tidy.
    """
    mib = 1048576
    required = int((used_bytes * 1.25 + mib - 1) // mib) + 48 + extra_mb
    return max(96, ((required + 15) // 16) * 16)


def make_ext4(fk, rootfs, out_path, used_bytes, files, extra_mb, log):
    mb = ext4_size_mb(used_bytes, extra_mb)
    inodes = int(files * 1.3) + 4096
    tmp = Path(str(out_path) + ".tmp")
    tmp.unlink(missing_ok=True)
    log(f"mke2fs: {mb} MiB, {files} files")
    fk.run(["mke2fs", "-q", "-t", "ext4", "-F", "-m", "0", "-L", "rootfs", "-N", inodes, "-d", rootfs, tmp, f"{mb}M"], write=False)
    os.replace(tmp, out_path)
    return mb


def _finalize_spec(fk, work, rootfs, spec, log):
    spec = {**spec, "dest": str(rootfs), "guest_bin": _guest_bin(), "dns": C.DNS}
    sp = Path(work) / "spec.json"
    sp.write_text(json.dumps(spec))
    r = fk.run(["python3", TOOL, "finalize", sp])
    info = json.loads(r.stdout.strip().splitlines()[-1])
    for w in info.get("warnings", []):
        log(f"WARN: {w}")
    return info


def _register(name, meta, disk, log):
    out_dir = C.IMG / name
    log("sha256")
    meta.update(name=name, arch=C.ARCH, created=now(), sha256=sha256_file(disk),
                guest_sha256=sha256_file(C.GUEST_BIN), agent=1)
    (out_dir / "meta.json").write_text(json.dumps(meta, indent=1))
    db.upsert("images", meta, pk="name")
    log(f"image '{name}' ready ({meta['size_mb']} MiB)")
    return name


# ---------------------------------------------------------------- OCI
def pull(ref, refresh, log):
    d = C.OCI / slug(ref, 100)
    if refresh or not (d / "index.json").exists():
        shutil.rmtree(d, ignore_errors=True)
        log(f"pull {ref} ({C.ARCH})")
        run(["skopeo", "copy", "--override-os", "linux", "--override-arch", C.ARCH, f"docker://{ref}", f"oci:{d}:img"], timeout=1800)
    else:
        log("using cached pull (tick 'refresh' to re-pull)")
    return d


def _oci_meta(d):
    cfg = json.loads(run(["skopeo", "inspect", "--config", f"oci:{d}:img"]).stdout)
    man = json.loads(run(["skopeo", "inspect", f"oci:{d}:img"]).stdout)
    return cfg, man


def build_oci(name, ref, log, env=None, entrypoint=None, cmd=None, hostname=None, refresh=False, force=False,
              extra_mb=64, source="oci", from_dir=None, ip_hint=None, from_archive=None, extra_files=None):
    name = name if name.startswith("_") else valid_name(name, "image name")
    if db.get("images", name, "name") and not force:
        raise Conflict(f"image '{name}' already exists - delete it first or tick 'rebuild'")
    _need("mke2fs", "python3") if from_dir else _need("skopeo", "mke2fs", "python3")
    archive_dir = None
    if from_archive:                       # OCI archive exported by BuildKit
        archive_dir = Path(tempfile.mkdtemp(dir=C.BUILD))
        run(["skopeo", "copy", f"oci-archive:{from_archive}", f"oci:{archive_dir}:img"], timeout=1800)
        d = archive_dir
    else:
        d = Path(from_dir) if from_dir else pull(ref, refresh, log)
    cfg, man = _oci_meta(d)
    c = cfg.get("config") or {}
    ep = entrypoint if entrypoint is not None else (c.get("Entrypoint") or [])
    cm = cmd if cmd is not None else ([] if entrypoint is not None else (c.get("Cmd") or []))
    args = list(ep) + list(cm)
    envd = _envdict(c.get("Env"))
    envd.update(env or {})
    work = Path(tempfile.mkdtemp(dir=C.BUILD))
    try:
        fk = Fake(work, log)
        rootfs = work / "rootfs"
        log("unpack layers (nothing from the image is executed)")
        r = fk.run(["python3", TOOL, "extract-oci", d, "img", rootfs])
        log(f"unpacked: {r.stdout.strip().splitlines()[-1] if r.stdout.strip() else ''}")
        info = _finalize_spec(fk, work, rootfs, {"hostname": hostname or name, "args": args, "env": envd,
                                                 "workdir": c.get("WorkingDir") or "/", "files": extra_files or []}, log)
        out_dir = C.IMG / name
        out_dir.mkdir(parents=True, exist_ok=True)
        mb = make_ext4(fk, rootfs, out_dir / "rootfs.ext4", info["bytes"], info["files"], extra_mb, log)
        meta = {"ref": ref, "source": source, "digest": man.get("Digest"), "size_mb": mb, "args": args,
                "workdir": c.get("WorkingDir") or "/", "user": c.get("User") or "root",
                "ports": sorted((c.get("ExposedPorts") or {}).keys()), "env": envd, "hostname": hostname or name,
                "notes": "" if info["has_sh"] else "no /bin/sh in image: Exec and the workload start script are unavailable"}
        if c.get("User") and c["User"] not in ("root", "0"):
            meta["notes"] = (meta["notes"] + f" Image sets USER {c['User']}; the workload runs as root inside the microVM.").strip()
        return _register(name, meta, out_dir / "rootfs.ext4", log)
    finally:
        shutil.rmtree(work, ignore_errors=True)
        if archive_dir:
            shutil.rmtree(archive_dir, ignore_errors=True)


def _envdict(v):
    if not v:
        return {}
    if isinstance(v, dict):
        return {str(k): str(x) for k, x in v.items() if x is not None}
    return dict(str(e).split("=", 1) for e in v if "=" in str(e))


def _listify(v):
    if v is None:
        return None
    return shlex.split(v) if isinstance(v, str) else [str(x) for x in v]


def build_compose(text, project, refresh, log):
    project = valid_name(project or "stack", "project")
    doc = yaml.safe_load(text)
    svcs = doc.get("services") if isinstance(doc, dict) else None
    if not isinstance(svcs, dict) or not svcs:
        raise UserError("compose file has no services")
    bad = [n for n, s in svcs.items() if not isinstance(s, dict) or "image" not in s]
    if bad:
        raise UserError(f"services without 'image': {bad} (use the Dockerfile tab to build from source)")
    for n in svcs:
        if not re.fullmatch(r"[A-Za-z0-9_.-]+", str(n)):
            raise UserError(f"bad service name: {n}")
    names = []
    for n, s in svcs.items():
        name = slug(f"{project}-{n}")
        if db.get("images", name, "name"):
            log(f"service {n}: skipped (image '{name}' exists)")
            continue
        log(f"service {n} -> image {name}")
        names.append(build_oci(name, norm_ref(str(s["image"])), log, env=_envdict(s.get("environment")),
                               entrypoint=_listify(s.get("entrypoint")), cmd=_listify(s.get("command")),
                               hostname=str(n), refresh=refresh, source=f"compose:{project}"))
    return names


# ---------------------------------------------------------------- tarball / official rootfs
def build_from_tree(name, extract_argv_fn, log, source, ref="", hostname=None, extra_mb=1024, notes=""):
    """Generic: extract_argv_fn(fk, rootfs) fills the tree, then finalize + mke2fs (bare VM, no workload)."""
    name = valid_name(name, "image name")
    if db.get("images", name, "name"):
        raise Conflict(f"image '{name}' already exists")
    _need("mke2fs", "python3")
    work = Path(tempfile.mkdtemp(dir=C.BUILD))
    try:
        fk = Fake(work, log)
        rootfs = work / "rootfs"
        extract_argv_fn(fk, rootfs)
        info = _finalize_spec(fk, work, rootfs, {"hostname": hostname or name, "args": [], "env": {}, "workdir": "/"}, log)
        out_dir = C.IMG / name
        out_dir.mkdir(parents=True, exist_ok=True)
        mb = make_ext4(fk, rootfs, out_dir / "rootfs.ext4", info["bytes"], info["files"], extra_mb, log)
        meta = {"ref": ref, "source": source, "size_mb": mb, "args": [], "workdir": "/", "user": "root", "ports": [],
                "env": {}, "hostname": hostname or name, "notes": notes}
        return _register(name, meta, out_dir / "rootfs.ext4", log)
    finally:
        shutil.rmtree(work, ignore_errors=True)


def import_tar(name, tar_path, log):
    def ex(fk, rootfs):
        log("unpack tarball")
        fk.run(["python3", TOOL, "extract-tar", tar_path, rootfs])
    return build_from_tree(name, ex, log, "tar", notes="bare microVM: no workload, use Exec")


def official_rootfs(name, log):
    """The Ubuntu rootfs the Firecracker project publishes for its CI and getting-started guide."""
    _need("unsquashfs")
    url = host.resolve_rootfs_url(log)
    work = Path(tempfile.mkdtemp(dir=C.BUILD))
    sq = work / "rootfs.squashfs"
    try:
        host.download(url, sq, log, min_size=16 << 20)

        def ex(fk, rootfs):
            log("unsquashfs")
            fk.run(["unsquashfs", "-no-progress", "-d", rootfs, sq])
        return build_from_tree(name, ex, log, "official", ref=url, extra_mb=2048,
                               notes="Official Firecracker CI Ubuntu rootfs, booted with mvm-init (no systemd)")
    finally:
        shutil.rmtree(work, ignore_errors=True)


# ---------------------------------------------------------------- Dockerfile / Railpack -> BuildKit in a builder microVM
def default_image_name(repo_or_name):
    """owner/repo (or a URL) -> a readable image name like 'owner-repo'. Never a random internal name."""
    s = re.sub(r"^https?://[^/]+/", "", str(repo_or_name or "").strip()).removesuffix(".git").strip("/")
    return valid_name(slug(s.replace("/", "-"), 48) or "app", "image name")


def _tmp_name():
    return f"_t{uuid.uuid4().hex[:6]}"


def build_dockerfile(name, text, ctx_dir, log, build_args=None, refresh=False, extra_mb=64, *, dockerfile=None, target=None,
                     replace=False, frontend="dockerfile", source="dockerfile", no_cache=False):
    """THE entry point for every Dockerfile / Railpack build (UI, GitHub sources, compose services).

    Either `text` (a Dockerfile typed in the UI) or `dockerfile` (path inside ctx_dir) is given. The build runs in a
    temporary builder microVM with BuildKit; the exported OCI image then takes the normal importer path.
    With replace=True an existing image of that name is swapped atomically once the new one is ready."""
    from . import buildkit
    name = valid_name(name, "image name")
    if db.get("images", name, "name") and not replace:
        raise Conflict(f"image '{name}' already exists - choose another name, or tick 'replace if it exists'")
    own_ctx = None
    if ctx_dir is None:
        own_ctx = tempfile.mkdtemp(dir=C.BUILD)
        ctx_dir = own_ctx
    ctx_dir = str(ctx_dir)
    archive = None
    try:
        if frontend == "dockerfile":
            if text is not None:
                if len(text) > 500_000:
                    raise UserError("Dockerfile too large")
                rel = dockerfile or ("Dockerfile" if not (Path(ctx_dir) / "Dockerfile").exists() else ".mvm.Dockerfile")
                (Path(ctx_dir) / rel).write_text(text)
                dockerfile = rel
            dockerfile = dockerfile or "Dockerfile"
        log(f"building image '{name}' ({'Railpack auto-detect' if frontend == 'railpack' else dockerfile}) with BuildKit")
        archive, info = buildkit.build(ctx_dir, log, frontend=frontend, dockerfile=dockerfile or "Dockerfile", target=target,
                                       build_args=build_args, no_cache=no_cache)
        tmp = _tmp_name()
        try:
            build_oci(tmp, f"build:{name}", log, from_archive=str(archive), extra_mb=extra_mb, source=source)
            replace_image(tmp, name)
        except Exception:
            delete_image_force(tmp)
            raise
        return name
    finally:
        if archive:
            shutil.rmtree(Path(archive).parent, ignore_errors=True)
        if own_ctx:
            shutil.rmtree(own_ctx, ignore_errors=True)


def rename_image(old, new):
    """Rename an image everywhere it is referenced (VMs, replica sets, GitHub sources, fleet services)."""
    new = valid_name(new, "image name")
    row = get_image(old)
    if old.startswith("_"):
        raise UserError("internal images cannot be renamed")
    if db.get("images", new, "name"):
        raise Conflict(f"an image named '{new}' already exists")
    os.rename(C.IMG / old, C.IMG / new)
    row["name"] = new
    (C.IMG / new / "meta.json").write_text(json.dumps(row, indent=1))
    db.delete("images", old, "name")
    db.upsert("images", row, pk="name")
    for v in db.list_("vms", "WHERE image=?", (old,)):
        db.update("vms", v["id"], {"image": new})
    for r in db.list_("replicasets", "WHERE image=?", (old,)):
        db.update("replicasets", r["id"], {"image": new})
    for g in db.list_("gitsources", "WHERE image_name=?", (old,)):
        db.update("gitsources", g["id"], {"image_name": new})
    return get_image(new)


def gc_internal(log=lambda m: None):
    """Remove leftovers of interrupted builds: temp images (_t…/_g…/_b…) and hidden builder VMs. Keeps the builder image."""
    n = 0
    for i in db.list_("images"):
        if i["name"].startswith("_") and i["name"] != C.BUILDER_IMAGE:
            delete_image_force(i["name"])
            n += 1
    for d in C.IMG.iterdir() if C.IMG.exists() else []:
        if d.name.startswith("_") and d.name != C.BUILDER_IMAGE or d.name.endswith(".old"):
            shutil.rmtree(d, ignore_errors=True)
    for d in C.BUILD.iterdir() if C.BUILD.exists() else []:
        shutil.rmtree(d, ignore_errors=True)
    if n:
        log(f"removed {n} temporary image(s) left by an interrupted build")
    return n


def delete_image_force(name):
    shutil.rmtree(C.IMG / name, ignore_errors=True)
    db.delete("images", name, "name")


# ---------------------------------------------------------------- git
def git_clone(repo, branch, dest, log):
    _need("git")
    if not re.fullmatch(r"https://[A-Za-z0-9._~:/?#@!$&()*+,;=%-]+", repo or ""):
        raise UserError("repository must be an https:// URL")
    if branch and not re.fullmatch(r"[A-Za-z0-9._/-]{1,100}", branch):
        raise UserError("bad branch name")
    argv = ["git", "-c", "protocol.file.allow=never", "-c", "protocol.ext.allow=never", "-c", "core.hooksPath=/dev/null",
            "clone", "--depth", "1", "--no-tags", "--single-branch"]
    if branch:
        argv += ["--branch", branch]
    argv += ["--", repo, str(dest)]
    log(f"git clone {repo}" + (f" ({branch})" if branch else ""))
    run(argv, timeout=600, env={"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "GIT_TERMINAL_PROMPT": "0",
                                "GIT_CONFIG_NOSYSTEM": "1", "HOME": str(C.BUILD)})
    commit = run(["git", "-C", str(dest), "rev-parse", "HEAD"], timeout=30).stdout.strip()
    shutil.rmtree(Path(dest) / ".git", ignore_errors=True)
    return commit


def replace_image(tmp, final):
    """Atomically swap a freshly built hidden image into the public name (existing VMs keep their own disk copies)."""
    row = db.get("images", tmp, "name")
    if not row:
        raise NotFound(tmp)
    dst, bak = C.IMG / final, C.IMG / (final + ".old")
    shutil.rmtree(bak, ignore_errors=True)
    if dst.exists():
        os.rename(dst, bak)
    os.rename(C.IMG / tmp, dst)
    row["name"] = final
    (dst / "meta.json").write_text(json.dumps(row, indent=1))
    db.delete("images", tmp, "name")
    db.upsert("images", row, pk="name")
    shutil.rmtree(bak, ignore_errors=True)


def build_git(name, repo, branch, dockerfile, log, refresh=False, mode="dockerfile", replace=False, target=None, build_args=None):
    """One-off build from a Git repo (the saved, repeatable version is gitsources)."""
    work = Path(tempfile.mkdtemp(dir=C.BUILD))
    try:
        git_clone(repo, branch, work / "src", log)
        if mode == "railpack":
            return build_dockerfile(name, None, work / "src", log, build_args=build_args, replace=replace, frontend="railpack", source="github")
        df = _safe_ctx(work / "src", dockerfile or "Dockerfile")
        if not df.is_file():
            raise UserError(f"{dockerfile or 'Dockerfile'} not found in the repository")
        return build_dockerfile(name, None, work / "src", log, build_args=build_args, dockerfile=str(df.relative_to((work / "src").resolve())),
                                target=target, replace=replace, source="github")
    finally:
        shutil.rmtree(work, ignore_errors=True)


def _safe_ctx(ctx, rel):
    p = (Path(ctx) / rel.lstrip("/")).resolve()
    if not (p == Path(ctx).resolve() or p.is_relative_to(Path(ctx).resolve())):
        raise UserError(f"path escapes the build context: {rel}")
    return p


# ---------------------------------------------------------------- preload
def preload(log=print):
    from . import jobs
    env = os.environ.get("MVM_PRELOAD", "")
    refs = [x.strip() for x in env.split(",") if x.strip()]
    for ref in refs:
        try:
            full = norm_ref(ref)
        except UserError:
            continue
        name = short_ref(full)
        if not db.get("images", name, "name"):
            jobs.submit(full, lambda lg, full=full, name=name: [build_oci(name, full, lg)])

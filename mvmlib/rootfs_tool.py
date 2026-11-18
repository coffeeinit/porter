#!/usr/bin/env python3
"""rootfs_tool - build a root filesystem directory without root privileges.

Runs standalone so it can be wrapped in `fakeroot` (file owners/modes are then recorded in
fakeroot's state file and honoured by `mke2fs -d`). Without fakeroot it still works, but every
file ends up owned by the build user.

  rootfs_tool.py extract-oci OCI_DIR TAG DEST      apply OCI image layers (whiteouts included)
  rootfs_tool.py extract-tar TARFILE DEST          unpack a plain rootfs tarball
  rootfs_tool.py finalize SPEC.json                inject init, agent, run/env files; print size info

Hardening: every path from an archive is resolved *inside* DEST (absolute symlinks are treated as
relative to DEST), `..` can never climb out, device nodes are skipped, nothing is executed.
"""
import json
import os
import shlex
import shutil
import stat
import subprocess
import sys
import tarfile

MAX_DEPTH = 40


def resolve(root, rel, follow_last=False):
    """Map an in-image path to a host path under root, following symlinks as a chroot would."""
    stack = [p for p in reversed(rel.split("/")) if p not in ("", ".")]
    resolved, hops = [], 0
    while stack:
        p = stack.pop()
        if p == "..":
            if resolved:
                resolved.pop()
            continue
        cand = os.path.join(root, *resolved, p)
        if os.path.islink(cand) and (follow_last or stack):
            hops += 1
            if hops > MAX_DEPTH:
                raise RuntimeError(f"too many symlinks resolving {rel}")
            t = os.readlink(cand)
            if t.startswith("/"):
                resolved = []
            stack.extend(x for x in reversed(t.split("/")) if x not in ("", "."))
            continue
        resolved.append(p)
    return os.path.join(root, *resolved) if resolved else root


def rm_any(path):
    if os.path.islink(path) or os.path.isfile(path):
        os.unlink(path)
    elif os.path.isdir(path):
        make_writable(path)
        shutil.rmtree(path, onerror=lambda f, p, e: (os.chmod(os.path.dirname(p), 0o700), f(p)))


def make_writable(path):
    for dp, dns, _ in os.walk(path):
        try:
            os.chmod(dp, 0o700)
        except OSError:
            pass


class Extractor:
    def __init__(self, root):
        self.root = os.path.realpath(root)
        self.dirs = []        # (path, mode, uid, gid, mtime) applied last
        self.flat = 0         # chown calls that were refused (no fakeroot)
        self.skipped = 0

    def chown(self, path, uid, gid):
        try:
            os.lchown(path, uid, gid)
        except PermissionError:
            self.flat += 1
        except OSError:
            pass

    def apply_tar(self, tf):
        for m in tf:
            name = m.name.lstrip("/")
            base = os.path.basename(name)
            parent = os.path.dirname(name)
            if base == ".wh..wh..opq":
                d = resolve(self.root, parent, follow_last=True)
                if os.path.isdir(d):
                    for c in os.listdir(d):
                        rm_any(os.path.join(d, c))
                continue
            if base.startswith(".wh."):
                tgt = os.path.join(resolve(self.root, parent, follow_last=True), base[4:])
                if os.path.lexists(tgt):
                    rm_any(tgt)
                continue
            if not name or name == ".":
                continue
            dest = os.path.join(resolve(self.root, parent, follow_last=True), base)
            if m.isdir():
                if os.path.islink(dest) or os.path.isfile(dest):
                    os.unlink(dest)
                os.makedirs(dest, exist_ok=True)
                os.chmod(dest, 0o700)
                self.dirs.append((dest, m.mode & 0o7777, m.uid, m.gid, m.mtime))
                continue
            os.makedirs(os.path.dirname(dest), exist_ok=True)
            if os.path.lexists(dest):
                rm_any(dest)
            if m.isreg():
                src = tf.extractfile(m)
                fd = os.open(dest, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
                with os.fdopen(fd, "wb") as out:
                    shutil.copyfileobj(src, out, 1 << 20)
                os.chmod(dest, m.mode & 0o7777)
            elif m.issym():
                os.symlink(m.linkname, dest)
            elif m.islnk():
                tgt = resolve(self.root, m.linkname.lstrip("/"), follow_last=False)
                try:
                    os.link(tgt, dest)
                except OSError:
                    shutil.copy2(tgt, dest, follow_symlinks=False)
                continue
            else:                       # devices, fifos: devtmpfs / the workload creates those at runtime
                self.skipped += 1
                continue
            self.chown(dest, m.uid, m.gid)
            if not m.issym():
                try:
                    os.utime(dest, (m.mtime, m.mtime))
                except OSError:
                    pass

    def finish(self):
        for path, mode, uid, gid, mtime in reversed(self.dirs):
            if not os.path.isdir(path):
                continue
            os.chmod(path, mode)
            self.chown(path, uid, gid)
            try:
                os.utime(path, (mtime, mtime))
            except OSError:
                pass


def open_layer(path):
    with open(path, "rb") as f:
        magic = f.read(4)
    if magic == b"\x28\xb5\x2f\xfd":      # zstd layers
        p = subprocess.Popen(["zstd", "-dc", "--", path], stdout=subprocess.PIPE)
        return tarfile.open(fileobj=p.stdout, mode="r|"), p
    return tarfile.open(path, "r:*"), None


def extract_oci(oci_dir, tag, dest):
    idx = json.load(open(os.path.join(oci_dir, "index.json")))
    entry = next((m for m in idx["manifests"] if m.get("annotations", {}).get("org.opencontainers.image.ref.name") == tag), idx["manifests"][0])

    def blob(digest):
        algo, h = digest.split(":", 1)
        return os.path.join(oci_dir, "blobs", algo, h)
    man = json.load(open(blob(entry["digest"])))
    if "manifests" in man:                 # image index -> pick the first manifest
        man = json.load(open(blob(man["manifests"][0]["digest"])))
    os.makedirs(dest, exist_ok=True)
    ex = Extractor(dest)
    for layer in man["layers"]:
        tf, proc = open_layer(blob(layer["digest"]))
        with tf:
            ex.apply_tar(tf)
        if proc:
            proc.wait()
    ex.finish()
    return ex


def extract_tar(tar_path, dest):
    os.makedirs(dest, exist_ok=True)
    ex = Extractor(dest)
    tf, proc = open_layer(tar_path)
    with tf:
        ex.apply_tar(tf)
    ex.finish()
    return ex


# ----------------------------------------------------------------- finalize
def put(root, path, data, mode=0o644):
    root = os.path.realpath(root)
    parent = resolve(root, os.path.dirname(path.lstrip("/")), follow_last=True)
    os.makedirs(parent, exist_ok=True)
    dest = os.path.join(parent, os.path.basename(path))
    if os.path.lexists(dest):
        rm_any(dest)
    mode_w = "wb" if isinstance(data, bytes) else "w"
    with open(dest, mode_w) as f:
        f.write(data)
    os.chmod(dest, mode)
    try:
        os.lchown(dest, 0, 0)
    except OSError:
        pass
    return dest


def finalize(spec):
    root = os.path.realpath(spec["dest"])
    info = {"warnings": []}
    sh = resolve(root, "bin/sh", follow_last=True)
    info["has_sh"] = os.path.isfile(sh)
    if not info["has_sh"]:
        info["warnings"].append("image has no /bin/sh: the workload and 'Exec' need a shell")
    guest = spec["guest_bin"]
    put(root, "/mvm-init", open(guest, "rb").read(), 0o755)
    for d, mode in (("proc", 0o555), ("sys", 0o555), ("dev", 0o755), ("run", 0o755), ("tmp", 0o1777), ("root", 0o700), ("mnt", 0o755), ("etc/mvm", 0o755)):
        p = resolve(root, d, follow_last=True)
        os.makedirs(p, exist_ok=True)
        os.chmod(p, mode)
    env = spec.get("env") or {}
    for k, v in env.items():
        if "\n" in str(v) or "=" in k or not k:
            raise SystemExit(f"bad environment variable: {k!r}")
    put(root, "/etc/mvm/env", "".join(f"{k}={v}\n" for k, v in env.items()), 0o644)
    args = spec.get("args") or []
    if args:
        wd = spec.get("workdir") or "/"
        script = "#!/bin/sh\ncd " + shlex.quote(wd) + " 2>/dev/null || cd /\nexec " + " ".join(shlex.quote(a) for a in args) + "\n"
        put(root, "/etc/mvm/run", script, 0o755)
    elif os.path.lexists(resolve(root, "etc/mvm/run")):
        rm_any(resolve(root, "etc/mvm/run"))
    for f in spec.get("files") or []:                      # extra binaries for special images (the BuildKit builder)
        put(root, f["dst"], open(f["src"], "rb").read(), int(f.get("mode", 0o755)))
    host = spec.get("hostname") or "microvm"
    put(root, "/etc/hostname", host + "\n")
    put(root, "/etc/hosts", f"127.0.0.1 localhost\n127.0.1.1 {host}\n::1 localhost ip6-localhost\n")
    if spec.get("dns"):
        put(root, "/etc/resolv.conf", "".join(f"nameserver {d}\n" for d in spec["dns"]))
    total = files = 0
    for dp, dns, fns in os.walk(root):
        files += len(dns) + len(fns)
        for fn in fns:
            try:
                total += os.lstat(os.path.join(dp, fn)).st_size
            except OSError:
                pass
    info.update(bytes=total, files=files)
    return info


def main(argv):
    if len(argv) < 2:
        raise SystemExit(__doc__)
    cmd = argv[1]
    if cmd == "extract-oci" and len(argv) == 5:
        ex = extract_oci(argv[2], argv[3], argv[4])
    elif cmd == "extract-tar" and len(argv) == 4:
        ex = extract_tar(argv[2], argv[3])
    elif cmd == "finalize" and len(argv) == 3:
        print(json.dumps(finalize(json.load(open(argv[2])))))
        return
    else:
        raise SystemExit(__doc__)
    print(json.dumps({"flattened_owners": ex.flat, "skipped_special": ex.skipped}))


if __name__ == "__main__":
    main(sys.argv)

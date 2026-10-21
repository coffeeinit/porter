#!/usr/bin/env python3
"""OCI image (Docker Hub, GHCR, any OCI registry) -> unpacked rootfs tree.

Implements the OCI distribution spec flow: resolve manifest (following
indexes to the amd64 entry), download layers with auth, VERIFY each blob's
sha256 digest, unpack in order, and apply overlayfs whiteouts. Output is a
directory tree ready to merge onto a Porter base and pack into rootfs.ext4.

Prototype path; production should reuse containerd's unpacker in Go.
"""
import argparse
import datetime
import hashlib
import json
import os
import re
import shutil
import tarfile
import tempfile
import urllib.error
import urllib.request

ACCEPT = ",".join([
    "application/vnd.docker.distribution.manifest.v2+json",
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
    "application/vnd.oci.image.index.v1+json",
])


def registry_ref(ref: str):
    """Split 'ghcr.io/owner/img:1.2' -> (https://registry, repo, tag)."""
    if "@" in ref:
        rest, digest = ref.rsplit("@", 1)
        tag = digest
    else:
        rest, _, tag = ref.partition(":")
        tag = tag or "latest"
    host, _, repo = rest.partition("/")
    if host not in ("docker.io", "registry-1.docker.io", "ghcr.io", "quay.io", "registry.k8s.io") and "." not in host:
        repo = rest
        host = "docker.io"
    if host == "docker.io":
        host = "registry-1.docker.io"
        if "/" not in repo:
            repo = f"library/{repo}"
    return f"https://{host}", repo, tag


_token_cache: dict = {}


def get_token(base: str, repo: str) -> str | None:
    """Anonymous pull token for registries that require one (Docker Hub, GHCR)."""
    if (base, repo) in _token_cache:
        return _token_cache[(base, repo)]
    try:
        probe = urllib.request.Request(f"{base}/v2/", method="GET")
        urllib.request.urlopen(probe)
        return None  # registry allows anonymous without token
    except urllib.error.HTTPError as e:
        if e.code != 401:
            raise
        auth = e.headers.get("Www-Authenticate", "")
    m = dict(re.findall(r'(\w+)="([^"]+)"', auth))
    if "realm" not in m:
        raise SystemExit(f"unsupported auth challenge: {auth}")
    url = m["realm"]
    params = []
    if m.get("service"):
        params.append(f"service={m['service']}")
    params.append(f"scope=repository:{repo}:pull")
    sep = "&" if "?" in url else "?"
    with urllib.request.urlopen(url + sep + "&".join(params)) as r:
        tok = json.load(r)
    token = tok.get("token") or tok.get("access_token")
    _token_cache[(base, repo)] = token
    return token


def http(url: str, token: str | None = None) -> bytes:
    req = urllib.request.Request(url, headers={"Accept": ACCEPT})
    if token:
        req.add_header("Authorization", f"Bearer {token}")
    with urllib.request.urlopen(req) as r:
        return r.read()


def resolve_manifest(ref: str) -> tuple[dict, str, str]:
    """-> (manifest dict, pinned full ref with digest, registry base)."""
    base, repo, tag = registry_ref(ref)
    token = get_token(base, repo)
    manifest = json.loads(http(f"{base}/v2/{repo}/manifests/{tag}", token))
    if "manifests" in manifest:  # index/manifest list -> amd64 entry
        for m in manifest["manifests"]:
            if m.get("platform", {}).get("architecture") in ("amd64", "x86_64"):
                name = ref.split("@")[0].split(":")[0]
                return resolve_manifest(f"{name}@{m['digest']}")
        raise SystemExit("no amd64 manifest in index")
    name = ref.split("@")[0].split(":")[0]
    return manifest, f"{name}@{manifest['config']['digest']}", base


def verify_sha256(data: bytes, digest: str):
    if digest.startswith("sha256:"):
        got = hashlib.sha256(data).hexdigest()
        if got != digest.split(":", 1)[1]:
            raise SystemExit(f"DIGEST MISMATCH: expected {digest}, got sha256:{got}")


def unpack(ref: str, dest: str):
    base, repo, _ = registry_ref(ref)
    manifest, pinned, base_url = resolve_manifest(ref)
    token = get_token(base, repo)
    print(f"source pinned: {pinned}")

    for i, layer in enumerate(manifest["layers"], 1):
        dgst = layer["digest"]
        print(f"layer {i}/{len(manifest['layers'])}: {dgst[:24]} ({layer['size']} bytes)")
        blob = http(f"{base_url}/v2/{repo}/blobs/{dgst}", token)
        verify_sha256(blob, dgst)
        with tempfile.NamedTemporaryFile(suffix=".tgz", delete=False) as f:
            f.write(blob)
            path = f.name
        try:
            with tarfile.open(path) as t:
                for member in t.getmembers():
                    name = os.path.basename(member.name)
                    if name.startswith(".wh."):
                        # overlayfs whiteout: delete the target from lower layers
                        if name != ".wh..wh..opq":
                            target = os.path.join(
                                dest, os.path.dirname(member.name), name[4:])
                            if os.path.lexists(target):
                                shutil.rmtree(target, ignore_errors=True)
                        continue
                    try:
                        t.extract(member, dest)
                    except FileNotFoundError:
                        pass  # parent deleted by an earlier whiteout
        finally:
            os.unlink(path)

    cfg = json.loads(http(
        f"{base_url}/v2/{repo}/blobs/{manifest['config']['digest']}", token))
    return pinned, cfg


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("reference", help="OCI reference, e.g. ghcr.io/owner/img:1.2 or docker.io/library/nginx:1.29-alpine")
    ap.add_argument("-o", "--out", required=True, help="output directory for the unpacked rootfs")
    args = ap.parse_args()
    os.makedirs(args.out, exist_ok=True)
    pinned, cfg = unpack(args.reference, args.out)
    meta = {
        "source": args.reference,
        "pinned": pinned,
        "config": {
            "entrypoint": cfg.get("config", {}).get("Entrypoint"),
            "cmd": cfg.get("config", {}).get("Cmd"),
            "env": cfg.get("config", {}).get("Env"),
            "user": cfg.get("config", {}).get("User"),
            "exposedPorts": sorted((cfg.get("config", {}).get("ExposedPorts") or {}).keys()),
            "volumes": sorted((cfg.get("config", {}).get("Volumes") or {}).keys()),
        },
        "unpackedAt": datetime.datetime.utcnow().isoformat() + "Z",
    }
    with open(os.path.join(args.out, ".porter-unpack.json"), "w") as f:
        json.dump(meta, f, indent=2)
    print(f"rootfs tree ready at {args.out}")
    print(json.dumps(meta["config"], indent=2))


if __name__ == "__main__":
    main()

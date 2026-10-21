# Porter Bases

A base = Linux kernel (`vmlinux`) + bootable rootfs (`rootfs.ext4`) shared by
all application images built on it. Recipe files (`base.json`, `build.sh`,
`manifest.json`) are committed; binaries are built into `out/` (gitignored)
and published to GitHub Releases in CI.

## Layout

```
bases/alpine/3.21/
├── base.json       inputs + metadata
├── build.sh        minirootfs -> rootfs.ext4 (Firecracker docs method, mke2fs -d)
├── manifest.json   checksums + artifact locations for the last build
└── out/            rootfs.ext4, vmlinux, SHA256SUMS (not committed)
```

## Build locally (WSL or any Linux)

```bash
cd bases/alpine/3.21
sudo ./build.sh \
  --rootfs-tar alpine-minirootfs-3.21.2-x86_64.tar.gz \
  --kernel vmlinux-6.18.48 \
  --out out
```

Verify: `mount -o loop,ro out/rootfs.ext4 /mnt/vfy` (WSL: `wsl -u root`).
Boot-test with Firecracker when KVM is available (not inside WSL).

## Boot verification (WSL with KVM works)

```bash
wsl -u root -e bash -c 'rm -f /tmp/fc/boot.sock && timeout 25 /tmp/fc/firecracker \
  --api-sock /tmp/fc/boot.sock --config-file /tmp/fc/boot.json'
```

Expected serial output ends with `Welcome to Alpine Linux 3.21` + login prompt
(~1.3 s kernel -> init). Verified 2026-10-07 with Firecracker v1.10.1,
kernel 6.18.48, OpenRC 0.55.1.

#!/usr/bin/env bash
# Build a Firecracker-bootable Alpine base rootfs.ext4 — follows the official
# Firecracker docs recipe: unpack minirootfs, enable a serial console on ttyS0,
# set up /etc/fstab + hostname, then pack with `mke2fs -d` (no loop devices,
# no root-owned image mount needed).
#
# Usage:
#   sudo ./build.sh --rootfs-tar alpine-minirootfs-3.21.2-x86_64.tar.gz \
#                   --kernel vmlinux-6.18.48 --out .
#
# Outputs (in --out):
#   rootfs.ext4  — bootable base rootfs
#   vmlinux      — kernel copied in so this dir is a complete base
#   SHA256SUMS
set -euo pipefail

ROOTFS_TAR=""
KERNEL=""
OUT="."
SIZE="256M"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --rootfs-tar) ROOTFS_TAR="$2"; shift 2 ;;
    --kernel)     KERNEL="$2"; shift 2 ;;
    --out)        OUT="$2"; shift 2 ;;
    --size)       SIZE="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

[[ -f "$ROOTFS_TAR" ]] || { echo "missing --rootfs-tar" >&2; exit 1; }
[[ -f "$KERNEL" ]]     || { echo "missing --kernel" >&2; exit 1; }

for tool in mke2fs sha256sum; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool (apt install e2fsprogs)" >&2; exit 1; }
done

WORK="$(mktemp -d)"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
trap 'rm -rf "$WORK"' EXIT

echo "==> unpacking minirootfs"
mkdir -p "$WORK/rootfs"
tar -xzf "$ROOTFS_TAR" -C "$WORK/rootfs"

echo "==> configuring serial console + boot basics"
# minirootfs lacks OpenRC; install it from local .apk files in ./pkgs/
# (apk is musl-linked and the build host is glibc, so run it via the musl
# loader shipped inside the rootfs). Falls back to the network if pkgs/ is
# empty.
if [[ ! -e "$WORK/rootfs/sbin/openrc" ]]; then
  echo "==> installing openrc"
  musl_apk() {
    "$WORK/rootfs/lib/ld-musl-x86_64.so.1" \
      --library-path "$WORK/rootfs/lib:$WORK/rootfs/usr/lib" \
      "$WORK/rootfs/sbin/apk" --root "$WORK/rootfs" "$@"
  }
  local_apks=( "$SCRIPT_DIR"/pkgs/*.apk )
  if [[ -e "${local_apks[0]}" ]]; then
    # Offline install: an .apk is a gzipped tar of the filesystem tree plus
    # control files. Extract the payload directly (no apk db needed for boot)
    # and register the packages in /etc/apk/world so `apk fix` reconciles
    # the database on first network access.
    for apk in "${local_apks[@]}"; do
      tar -xzf "$apk" -C "$WORK/rootfs" \
        --exclude='./.PKGINFO' --exclude='./.SIGN.*' \
        --exclude='./.pre-install*' --exclude='./.post-install*' \
        --exclude='./.pre-deinstall*' --exclude='./.post-deinstall*' \
        --exclude='./.trigger*' 2>/dev/null
      . "$apk" 2>/dev/null || true
      grep -q '^P:' /dev/null 2>/dev/null || true
    done
    # record package names in world (from .PKGINFO) for apk db reconciliation
    for apk in "${local_apks[@]}"; do
      tar -xOf "$apk" .PKGINFO 2>/dev/null | grep '^pkgname = ' | \
        cut -d' ' -f3 >> "$WORK/rootfs/etc/apk/world" || true
    done
    sort -u -o "$WORK/rootfs/etc/apk/world" "$WORK/rootfs/etc/apk/world"
    # enable the services OpenRC needs at boot on a headless microvm
    mkdir -p "$WORK/rootfs/etc/runlevels/sysinit" \
             "$WORK/rootfs/etc/runlevels/boot" \
             "$WORK/rootfs/etc/runlevels/shutdown"
    for svc in devfs mdev dmesg; do
      ln -sf /etc/init.d/$svc "$WORK/rootfs/etc/runlevels/sysinit/$svc"
    done
    for svc in hostname bootmisc syslog; do
      ln -sf /etc/init.d/$svc "$WORK/rootfs/etc/runlevels/boot/$svc"
    done
    for svc in mount-ro killprocs savecache; do
      [[ -e "$WORK/rootfs/etc/init.d/$svc" ]] && \
        ln -sf /etc/init.d/$svc "$WORK/rootfs/etc/runlevels/shutdown/$svc"
    done
  else
    musl_apk --no-cache add openrc >/dev/null 2>&1 || true
  fi
fi

# serial console on ttyS0 (Firecracker has no VGA): respawn getty
cat > "$WORK/rootfs/etc/inittab" <<'EOF'
# /etc/inittab — Firecracker microvm: serial console only
::sysinit:/sbin/openrc sysinit
::sysinit:/sbin/openrc boot
::wait:/sbin/openrc default
ttyS0::respawn:/sbin/getty -L 115200 ttyS0 vt100
::shutdown:/sbin/openrc shutdown
EOF

cat > "$WORK/rootfs/etc/fstab" <<'EOF'
# root is virtio-blk vda (Firecracker drive-is-rootfs)
/dev/vda  /       ext4  defaults  1 1
tmpfs     /tmp    tmpfs defaults,noatime,mode=1777 0 0
EOF

echo "porter-base" > "$WORK/rootfs/etc/hostname"
cat > "$WORK/rootfs/etc/network/interfaces" <<'EOF'
# guest networking is configured by the porter init from kernel cmdline;
# this fallback brings up lo only.
auto lo
iface lo inet loopback
EOF
mkdir -p "$WORK/rootfs/dev/pts" "$WORK/rootfs/dev/shm" "$WORK/rootfs/proc" \
         "$WORK/rootfs/sys" "$WORK/rootfs/tmp" "$WORK/rootfs/run"

echo "==> packing rootfs.ext4 ($SIZE)"
# -d populates the filesystem from a directory: the Firecracker docs method
mke2fs -q -t ext4 -d "$WORK/rootfs" -F "$OUT/rootfs.ext4" "$SIZE"

echo "==> kernel"
cp "$KERNEL" "$OUT/vmlinux"
chmod 644 "$OUT/vmlinux"

cd "$OUT"
sha256sum rootfs.ext4 vmlinux > SHA256SUMS
echo "==> done:"
cat SHA256SUMS

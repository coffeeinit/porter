#!/usr/bin/env bash
# Porter kernel build (PVE-13): Firecracker base + Porter overlay, with the
# olddefconfig survival gate. Usage:
#   scripts/kernel/build.sh --version 6.18 [--overlay pve-microvm-overlay.config] [--out out/]
# The overlay carries Porter's requirements (VLAN, cgroups, TUN, nftables,
# XFS, loop, watchdog, 9P, BPF_SYSCALL/JIT for runc). Missing BPF_JIT
# silently disables CGROUP_BPF and breaks containers — hence the gate.
set -euo pipefail

VERSION="6.18"
OVERLAY=""
OUT="out"

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --overlay) OVERLAY="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    *) echo "unknown flag $1" >&2; exit 2 ;;
  esac
done

# Survival gate: every symbol here must be =y or =m after olddefconfig,
# or the kernel cannot run Porter guests (virtio discovery, net, BPF).
REQUIRED="CONFIG_VIRTIO_NET CONFIG_VIRTIO_BLK CONFIG_VIRTIO_CONSOLE
CONFIG_VIRTIO_BALLOON CONFIG_VIRTIO_MMIO CONFIG_MODULES CONFIG_NET
CONFIG_TUN CONFIG_NFTABLES CONFIG_NFT_NAT CONFIG_NFT_MASQ
CONFIG_BRIDGE_NETFILTER CONFIG_BPF_SYSCALL CONFIG_BPF_JIT"

echo "porter kernel build: version=${VERSION} overlay=${OVERLAY:-<none>} out=${OUT}"
mkdir -p "${OUT}"

if [ ! -d "linux-${VERSION}" ]; then
  echo "fetch a Firecracker-base tree first (docs/firecracker-manual: kernel policy)" >&2
  exit 1
fi

cd "linux-${VERSION}"
make defconfig
if [ -n "${OVERLAY}" ]; then
  ./scripts/kconfig/merge_config.sh .config "${OVERLAY}"
fi
make olddefconfig

missing=0
for sym in ${REQUIRED}; do
  if ! grep -Eq "^${sym}=[ym]$" .config; then
    echo "GATE FAIL: ${sym} not =y/=m" >&2
    missing=1
  fi
done
if [ "$missing" -ne 0 ]; then
  echo "kernel config survival gate failed" >&2
  exit 1
fi

make -j"$(nproc)" bzImage modules
./scripts/clang-tools/gen_initramfs_list.sh 2>/dev/null || true
cp arch/x86/boot/bzImage "../${OUT}/vmlinuz-${VERSION}-porter"
echo "build ok: ${OUT}/vmlinuz-${VERSION}-porter"
echo "verify: nm vmlinux | grep -E 'virtnet_probe|virtblk_probe|virtio_balloon'"

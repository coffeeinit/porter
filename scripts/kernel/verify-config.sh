#!/bin/sh
# verify-config.sh — fail-closed kernel .config check (PVE-20/21).
# Usage: verify-config.sh <path-to-.config>
# Mirrors internal/kernel VerifyConfig: every required family must be =y or =m.
set -eu
CFG="${1:-}"
if [ -z "$CFG" ] || [ ! -f "$CFG" ]; then
  echo "usage: $0 <kernel .config>" >&2
  exit 2
fi
REQUIRED="CONFIG_VIRTIO_NET CONFIG_VIRTIO_BLK CONFIG_VIRTIO_CONSOLE CONFIG_VIRTIO_BALLOON CONFIG_VIRTIO_MMIO_CMDLINE CONFIG_VIRTIO_VSOCK CONFIG_TUN CONFIG_BRIDGE_NETFILTER CONFIG_NF_TABLES CONFIG_CGROUPS CONFIG_MEMCG CONFIG_BPF_SYSCALL CONFIG_OVERLAY_FS CONFIG_EXT4_FS CONFIG_9P_FS"
MISSING=""
for opt in $REQUIRED; do
  if ! grep -Eq "^${opt}=(y|m)$" "$CFG"; then
    MISSING="$MISSING $opt"
  fi
done
if [ -n "$MISSING" ]; then
  echo "missing kernel options:$MISSING" >&2
  exit 1
fi
echo "kernel config OK ($CFG)"

#!/usr/bin/env bash
# Stage a BuildKit OCI archive as a Firecracker builder MicroVM image.
# No Docker or Podman daemon is used by this script.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OCI=""
KERNEL=""
GUEST_BASES_DIR="${PORTER_GUEST_BASES_DIR:-${ROOT}/data/bases}"
IMAGES_DIR="${PORTER_IMAGES_DIR:-${ROOT}/data/images}"
SIZE_MIB="${PORTER_BUILDER_SIZE_MIB:-2048}"

usage() {
  cat <<EOF
Usage: $0 --oci /path/to/buildkit.oci.tar --kernel /path/to/vmlinux [options]

Stages a supplied OCI-layout or docker-save archive into a Firecracker rootfs.
The archive may be produced by an OCI registry/CI exporter; this script never
invokes Docker or Podman. Runtime builds execute BuildKit inside Firecracker.

Options:
  --oci FILE              BuildKit OCI-layout or docker-save archive
  --kernel FILE           Firecracker-compatible vmlinux
  --guest-bases-dir DIR   Output rootfs/kernel directory
  --images-dir DIR        Output image catalog directory
  --size-mib N            Rootfs size (default: ${SIZE_MIB})
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --oci) OCI="$2"; shift 2 ;;
    --kernel) KERNEL="$2"; shift 2 ;;
    --guest-bases-dir) GUEST_BASES_DIR="$2"; shift 2 ;;
    --images-dir) IMAGES_DIR="$2"; shift 2 ;;
    --size-mib) SIZE_MIB="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ -s "$OCI" ]] || { echo "--oci is required and must be non-empty: $OCI" >&2; exit 2; }
[[ -s "$KERNEL" ]] || { echo "--kernel is required and must be non-empty: $KERNEL" >&2; exit 2; }

mkdir -p "$GUEST_BASES_DIR" "$IMAGES_DIR"
(
  cd "$ROOT"
  go run ./cmd/porter-builder-image \
    --oci "$OCI" \
    --kernel "$KERNEL" \
    --guest-bases-dir "$GUEST_BASES_DIR" \
    --images-dir "$IMAGES_DIR" \
    --size-mib "$SIZE_MIB" \
    --buildkit-image "moby/buildkit:oci-archive"
)

echo
echo "BuildKit builder image staged without Docker/Podman."
echo "Set this in porter.toml:"
echo "[build]"
echo 'builder_vm_image = "custom://porter-builder"'
echo "guest_bases_dir = \"${GUEST_BASES_DIR}\""
echo "[firecracker]"
echo "kernel_image = \"${KERNEL}\""

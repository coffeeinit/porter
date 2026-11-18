#!/usr/bin/env bash
# MicroVM Maker installer (Ubuntu/Debian; other distros: install the listed packages yourself).
#   ./install.sh               install everything for the current user
#   ./install.sh --systemd     ...and run the web UI + network rules as services at boot
#   FC_VERSION=v1.18.0 ./install.sh     pin a Firecracker release
set -euo pipefail
cd "$(dirname "$0")"
say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }
[ "$(id -u)" -ne 0 ] || die "run this as your normal user (it calls sudo where root is needed)"
command -v sudo >/dev/null || die "sudo is required"
SYSTEMD=0; for a in "$@"; do [ "$a" = "--systemd" ] && SYSTEMD=1; done
ME="$(id -un)"; MYUID="$(id -u)"; MYGID="$(id -g)"; HERE="$(pwd)"
case "$(uname -m)" in x86_64) FCARCH=x86_64;; aarch64) FCARCH=aarch64;; *) die "unsupported CPU: $(uname -m)";; esac

say "System packages"
PKGS="python3 python3-venv python3-pip skopeo umoci e2fsprogs fakeroot squashfs-tools git gcc libc6-dev make iproute2 iptables curl ca-certificates zstd"
if command -v apt-get >/dev/null; then
  sudo apt-get update -qq && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq $PKGS
else
  echo "   not an apt system - make sure these exist: $PKGS"
fi

say "Firecracker + jailer"
if [ -z "${FC_VERSION:-}" ]; then
  FC_VERSION="$(curl -sSI https://github.com/firecracker-microvm/firecracker/releases/latest | tr -d '\r' | sed -n 's#^[Ll]ocation: .*/tag/##p')"
fi
[ -n "$FC_VERSION" ] || die "could not determine the latest Firecracker release (set FC_VERSION=v1.x.y)"
HAVE="$(firecracker --version 2>/dev/null | sed -n '1s/[^0-9]*//p' || true)"
if [ "v$HAVE" = "$FC_VERSION" ] && command -v jailer >/dev/null; then
  echo "   Firecracker $FC_VERSION already installed"
else
  T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
  URL="https://github.com/firecracker-microvm/firecracker/releases/download/$FC_VERSION/firecracker-$FC_VERSION-$FCARCH.tgz"
  curl -fsSL "$URL" -o "$T/fc.tgz"
  curl -fsSL "$URL.sha256.txt" -o "$T/fc.sha256" && (cd "$T" && sed "s#  .*#  fc.tgz#" fc.sha256 | sha256sum -c -) || die "checksum verification failed"
  tar -xzf "$T/fc.tgz" -C "$T"
  D="$T/release-$FC_VERSION-$FCARCH"
  sudo install -m 0755 "$D/firecracker-$FC_VERSION-$FCARCH" /usr/local/bin/firecracker
  sudo install -m 0755 "$D/jailer-$FC_VERSION-$FCARCH" /usr/local/bin/jailer
  echo "   installed $(firecracker --version | head -1) (sha256 verified)"
fi

say "Guest init + agent (static binary)"
make -C guest

say "Python environment"
python3 -m venv .venv
.venv/bin/pip install -q --upgrade pip
.venv/bin/pip install -q -r requirements.txt

say "KVM access"
if [ ! -e /dev/kvm ]; then
  echo "   WARNING: /dev/kvm missing. Enable virtualisation in BIOS, or nested virtualisation on your cloud VM."
elif ! id -nG "$ME" | tr ' ' '\n' | grep -qx kvm; then
  sudo usermod -aG kvm "$ME"; echo "   added $ME to group 'kvm' - LOG OUT AND IN AGAIN before starting VMs"
else
  echo "   ok"
fi

say "Root helper (the only privileged component) + sudoers"
sudo install -d -m 0755 -o root -g root /etc/mvm /srv/jailer
sudo install -m 0755 -o root -g root deploy/mvm-priv /usr/local/sbin/mvm-priv
sudo tee /etc/mvm/priv.conf >/dev/null <<CONF
{"fc": "/usr/local/bin/firecracker", "jailer": "/usr/local/bin/jailer", "chroot_base": "/srv/jailer",
 "uid": $MYUID, "gid": $MYGID, "bridge": "mvmbr0", "net": "172.30.0", "isolate": true, "block_private": true, "pf_min_port": 1024}
CONF
sudo chmod 0644 /etc/mvm/priv.conf
echo "$ME ALL=(root) NOPASSWD: /usr/local/sbin/mvm-priv" | sudo tee /etc/sudoers.d/mvm-priv >/dev/null
sudo chmod 0440 /etc/sudoers.d/mvm-priv
sudo visudo -cf /etc/sudoers.d/mvm-priv >/dev/null || { sudo rm -f /etc/sudoers.d/mvm-priv; die "sudoers validation failed"; }

say "Network: bridge mvmbr0 (172.30.0.1/24), NAT, guest firewall"
echo "net.ipv4.ip_forward=1" | sudo tee /etc/sysctl.d/90-mvm.conf >/dev/null
sudo /usr/local/sbin/mvm-priv fw-apply
sudo -n /usr/local/sbin/mvm-priv info >/dev/null && echo "   helper reachable through sudo"

if [ "$SYSTEMD" = 1 ]; then
  say "systemd services"
  sudo install -m 0644 deploy/mvm-net.service /etc/systemd/system/mvm-net.service
  sed -e "s#@USER@#$ME#g" -e "s#@GROUP@#$(id -gn)#g" -e "s#@DIR@#$HERE#g" deploy/mvm.service.in | sudo tee /etc/systemd/system/mvm.service >/dev/null
  sudo systemctl daemon-reload
  sudo systemctl enable --now mvm-net.service
  echo "   start the UI with: sudo systemctl enable --now mvm   (token: journalctl -u mvm | grep mvm_)"
else
  echo "   tip: ./install.sh --systemd re-applies the firewall at every boot and runs the UI as a service"
fi

cat <<DONE

Done. Next:
  1. (log out/in once if you were just added to the kvm group)
  2. ./run.sh            -> prints your ACCESS TOKEN once, then open http://127.0.0.1:8080
  3. In the UI: Images -> "Get kernel", then pull nginx:alpine and launch it.
DONE

#!/usr/bin/env bash
# Removes the root helper, sudoers rule, services and network rules. Your data (mvm-data/) is left alone.
set -u
[ "$(id -u)" -ne 0 ] || { echo "run as your normal user"; exit 1; }
sudo systemctl disable --now mvm.service mvm-net.service 2>/dev/null
sudo rm -f /etc/systemd/system/mvm.service /etc/systemd/system/mvm-net.service /etc/sudoers.d/mvm-priv /etc/sysctl.d/90-mvm.conf
sudo pkill -x firecracker 2>/dev/null
for t in $(ip -o link | sed -n 's/^[0-9]*: \(tap[a-f0-9]\{8\}\).*/\1/p'); do sudo ip link del "$t" 2>/dev/null; done
sudo ip link del mvmbr0 2>/dev/null
SUBNET=172.30.0.0/24
sudo iptables -t nat -D POSTROUTING -s $SUBNET ! -o mvmbr0 -j MASQUERADE 2>/dev/null
for c in FORWARD:"-i mvmbr0 -j MVM-FWD" FORWARD:"-o mvmbr0 -j MVM-FWD" INPUT:"-i mvmbr0 -j MVM-IN"; do sudo iptables -D "${c%%:*}" ${c#*:} 2>/dev/null; done
for c in PREROUTING OUTPUT; do sudo iptables -t nat -D $c -m addrtype --dst-type LOCAL -j MVM-PF 2>/dev/null; done
for ch in MVM-FWD MVM-IN; do sudo iptables -F $ch 2>/dev/null; sudo iptables -X $ch 2>/dev/null; done
sudo iptables -t nat -F MVM-PF 2>/dev/null; sudo iptables -t nat -X MVM-PF 2>/dev/null
sudo rm -rf /usr/local/sbin/mvm-priv /etc/mvm /srv/jailer
echo "Removed. Firecracker binaries in /usr/local/bin and your mvm-data/ folder were kept."

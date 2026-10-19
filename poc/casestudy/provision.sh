#!/usr/bin/env bash
# provision.sh — make the case-study node, or bring an existing one up to date. Runs on
# the host (the Mac), not on the node.
#
# Usage: ./provision.sh [vm-name]          (default di-node)
#
# Idempotent: each step checks before it acts.
#   1. the VM: Ubuntu 24.04, 4 vCPU, 8 GB, 20 GB, launched only if absent;
#   2. k0s, pinned, through ../scripts/02-k0s.sh, and Netdata through ../scripts/03-netdata.sh;
#   3. the tunnel key: ~/.ssh/<vm>-tunnel on the host, created if missing, its public half
#      in the VM's ~ubuntu/.ssh/authorized_keys once. The agent's review models run on the
#      host, not on the node — a 4-vCPU node running a decision model takes the CPU the
#      workloads it models need — and tunnel.sh carries them across with this key;
#   4. this directory copied to /home/ubuntu/casestudy on the node.
# The agent binary is built and copied separately (README).
set -euo pipefail
cd "$(dirname "$0")"
VM="${1:-di-node}"
KEY="$HOME/.ssh/$VM-tunnel"

if ! multipass info "$VM" >/dev/null 2>&1; then
    echo "[provision] launching $VM"
    multipass launch 24.04 --name "$VM" --cpus 4 --memory 8G --disk 20G
    multipass exec "$VM" -- cloud-init status --wait
fi
multipass exec "$VM" -- bash -c 'command -v k0s >/dev/null' || bash ../scripts/02-k0s.sh "$VM"
multipass exec "$VM" -- bash -c 'command -v netdata >/dev/null || [ -x /opt/netdata/bin/netdata ]' ||
    bash ../scripts/03-netdata.sh "$VM"

[ -f "$KEY" ] || ssh-keygen -q -t ed25519 -N '' -C "$VM-ollama-tunnel" -f "$KEY"
multipass transfer "$KEY.pub" "$VM:/tmp/tunnel.pub"
multipass exec "$VM" -- bash -c 'mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys &&
    { grep -qxF "$(cat /tmp/tunnel.pub)" ~/.ssh/authorized_keys || cat /tmp/tunnel.pub >>~/.ssh/authorized_keys; } &&
    chmod 600 ~/.ssh/authorized_keys && rm /tmp/tunnel.pub'
echo "[provision] tunnel key $KEY authorised on $VM"

multipass exec "$VM" -- sudo mkdir -p /home/ubuntu/casestudy
for f in *.sh *.yaml README.md; do multipass transfer "$f" "$VM:/tmp/$f"; done
multipass exec "$VM" -- bash -c 'cd /tmp && sudo cp *.sh *.yaml README.md /home/ubuntu/casestudy/ && sudo chmod +x /home/ubuntu/casestudy/*.sh'
multipass transfer -r workloads "$VM:/tmp/"
multipass exec "$VM" -- sudo cp -r /tmp/workloads /home/ubuntu/casestudy/
echo "[provision] $VM ready: build the agent (README), then ./tunnel.sh $VM"

#!/usr/bin/env bash
# tunnel.sh — carry the host's Ollama to the node: the node's localhost:11435 is the
# host's localhost:11434, for as long as this runs. Nothing else is exposed; Ollama on the
# host keeps listening on 127.0.0.1 only. Runs on the host; reconnects if the link drops.
#
# Usage: ./tunnel.sh [vm-name]             (default di-node; key from provision.sh)
set -uo pipefail
VM="${1:-di-node}"
KEY="$HOME/.ssh/$VM-tunnel"
IP=$(multipass info "$VM" | awk '/IPv4/ {print $2; exit}')
[ -n "$IP" ] && [ -f "$KEY" ] || { echo "no IP for $VM or no key $KEY: run provision.sh" >&2; exit 2; }
while :; do
    ssh -i "$KEY" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o ExitOnForwardFailure=yes \
        -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -N -R 11435:127.0.0.1:11434 "ubuntu@$IP"
    echo "[tunnel] dropped ($?); reconnecting in 5 s" >&2
    sleep 5
done

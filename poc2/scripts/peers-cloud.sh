#!/usr/bin/env bash
# peers-cloud.sh — register every di-agent pod as a peer of every other pod.
#
# Cloud-agnostic counterpart to scripts/04-peers.sh: on AKS/EKS the di-agent
# pod network is private to the cluster's VPC/VNet and isn't reachable
# directly from the operator's machine, so we use `kubectl port-forward`
# (one local port per pod) purely to issue the HTTP registration calls.
# The peer URLs registered between agents are each pod's in-cluster IP, so
# the agents themselves still talk to each other directly over the cluster
# network — only our own registration calls go through the forwarded ports.
#
# Usage: ./peers-cloud.sh

set -euo pipefail

GREEN=$(tput setaf 2 2>/dev/null || echo "")
YELLOW=$(tput setaf 3 2>/dev/null || echo "")
RED=$(tput setaf 1 2>/dev/null || echo "")
RESET=$(tput sgr0 2>/dev/null || echo "")

info() { echo "${YELLOW}[peers-cloud] $*${RESET}"; }
ok()   { echo "${GREEN}[peers-cloud] $*${RESET}"; }
err()  { echo "${RED}[peers-cloud] $*${RESET}" >&2; }

NAMESPACE="${NAMESPACE:-default}"
TRUST="${TRUST:-0.8}"
BASE_PORT="${BASE_PORT:-19090}"

command -v kubectl >/dev/null 2>&1 || { err "kubectl not found"; exit 1; }

mapfile -t POD_LINES < <(kubectl -n "$NAMESPACE" get pods -l app=di-agent \
    -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.podIP}{"\n"}{end}')
if [ "${#POD_LINES[@]}" -eq 0 ]; then
    err "No di-agent pods found in namespace $NAMESPACE. Run agent-cloud.sh first."
    exit 1
fi

NAMES=()
IPS=()
for line in "${POD_LINES[@]}"; do
    NAMES+=("$(awk '{print $1}' <<< "$line")")
    IPS+=("$(awk '{print $2}' <<< "$line")")
done
info "Found ${#NAMES[@]} di-agent pod(s): ${NAMES[*]}"

# ── port-forward each pod so registration calls can reach it ───────────────
PF_PIDS=()
LOCAL_PORTS=()
cleanup() {
    for pid in "${PF_PIDS[@]:-}"; do
        kill "$pid" >/dev/null 2>&1 || true
    done
}
trap cleanup EXIT

for i in "${!NAMES[@]}"; do
    port=$((BASE_PORT + i))
    kubectl -n "$NAMESPACE" port-forward "pod/${NAMES[$i]}" "${port}:9090" >/dev/null 2>&1 &
    PF_PIDS+=($!)
    LOCAL_PORTS+=("$port")
done

info "Waiting for port-forwards to come up ..."
sleep 3

register_peer() {
    local local_port="$1" peer_ip="$2" trust="$3"
    local add_resp
    add_resp=$(curl -sf -X POST -H "Content-Type: application/json" \
        -d "{\"url\": \"http://${peer_ip}:9090\"}" \
        "http://127.0.0.1:${local_port}/peers" 2>&1) || { err "POST /peers failed: $add_resp"; return 1; }
    local id
    id=$(python3 -c "import sys,json; print(json.load(sys.stdin)['id'])" <<< "$add_resp" 2>/dev/null || echo "")
    [ -n "$id" ] || { err "Could not extract peer id from response: $add_resp"; return 1; }
    curl -sf -X POST -H "Content-Type: application/json" \
        -d "{\"value\": ${trust}}" \
        "http://127.0.0.1:${local_port}/peers/${id}/trust" >/dev/null 2>&1 || { err "Failed to set trust for $id"; return 1; }
}

info "Registering full peer mesh (trust=$TRUST) ..."
for i in "${!NAMES[@]}"; do
    for j in "${!NAMES[@]}"; do
        [ "$i" -eq "$j" ] && continue
        if register_peer "${LOCAL_PORTS[$i]}" "${IPS[$j]}" "$TRUST"; then
            ok "  ${NAMES[$i]} <- peer ${NAMES[$j]} (${IPS[$j]})"
        fi
    done
done

ok "Peer registration complete."

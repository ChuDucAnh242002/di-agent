#!/usr/bin/env bash
# coordinator-cloud.sh — trust-weighted peer routing demonstration against
#                        di-agent pods on a managed cluster (AKS/EKS).
#
# Cloud-agnostic counterpart to scripts/coordinator.sh: instead of a
# control-plane VM plus worker VMs reachable directly over a libvirt
# network, every di-agent pod here is a peer node and is only reachable via
# `kubectl port-forward` from the operator's machine (pod IPs are private
# to the cluster's VPC/VNet). All /cost, /recommend, and trust-drain calls
# go through per-pod forwarded local ports; the peer-to-peer traffic
# between agents themselves still happens over the cluster network
# (registered by peers-cloud.sh).
#
# Usage: ./coordinator-cloud.sh
#   ROUNDS=8    — number of rounds (default 8)
#   INTERVAL=10 — seconds between rounds (default 10)
#   NAMESPACE=default

set -euo pipefail

GREEN=$(tput setaf 2 2>/dev/null || echo "")
YELLOW=$(tput setaf 3 2>/dev/null || echo "")
RED=$(tput setaf 1 2>/dev/null || echo "")
CYAN=$(tput setaf 6 2>/dev/null || echo "")
BOLD=$(tput bold 2>/dev/null || echo "")
RESET=$(tput sgr0 2>/dev/null || echo "")

info()    { echo "${YELLOW}[coord-cloud] $*${RESET}"; }
ok()      { echo "${GREEN}[coord-cloud] $*${RESET}"; }
err()     { echo "${RED}[coord-cloud] $*${RESET}" >&2; }
header()  { echo "${BOLD}${CYAN}$*${RESET}"; }
announce(){ echo "${BOLD}${GREEN}-> $*${RESET}"; }

json_get() {
    local json="$1" key="$2"
    if command -v python3 >/dev/null 2>&1; then
        echo "$json" | python3 -c \
            "import sys,json; d=json.load(sys.stdin); print(d.get('${key}',''))" 2>/dev/null || echo ""
    else
        echo ""
    fi
}

max_key() {
    local max_k="" max_v="-9999"
    while [ "$#" -ge 2 ]; do
        local k="$1" v="$2"; shift 2
        if python3 -c 'import sys; exit(0 if float(sys.argv[1]) > float(sys.argv[2]) else 1)' -- "$v" "$max_v" 2>/dev/null; then
            max_k="$k"; max_v="$v"
        fi
    done
    echo "$max_k $max_v"
}

NAMESPACE="${NAMESPACE:-default}"
ROUNDS="${ROUNDS:-8}"
INTERVAL="${INTERVAL:-10}"
BASE_PORT="${BASE_PORT:-19090}"
DRAIN_ROUND=$(( ROUNDS / 2 ))

command -v kubectl >/dev/null 2>&1 || { err "kubectl not found"; exit 1; }

mapfile -t POD_LINES < <(kubectl -n "$NAMESPACE" get pods -l app=di-agent \
    -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.podIP}{"\n"}{end}')
if [ "${#POD_LINES[@]}" -lt 2 ]; then
    err "At least two di-agent pods are required (found ${#POD_LINES[@]}). Run agent-cloud.sh and peers-cloud.sh first."
    exit 1
fi

NAMES=()
POD_IPS=()
for line in "${POD_LINES[@]}"; do
    NAMES+=("$(awk '{print $1}' <<< "$line")")
    POD_IPS+=("$(awk '{print $2}' <<< "$line")")
done

# ── port-forward each pod for the duration of the demo ──────────────────────
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

port_for() {
    local target="$1"
    for idx in "${!NAMES[@]}"; do
        [ "${NAMES[$idx]}" = "$target" ] && { echo "${LOCAL_PORTS[$idx]}"; return 0; }
    done
}
ip_for() {
    local target="$1"
    for idx in "${!NAMES[@]}"; do
        [ "${NAMES[$idx]}" = "$target" ] && { echo "${POD_IPS[$idx]}"; return 0; }
    done
}

info "Pod map:"
for idx in "${!NAMES[@]}"; do
    echo "  ${NAMES[$idx]}  ->  ${POD_IPS[$idx]} (local port ${LOCAL_PORTS[$idx]})"
done
echo ""

DRAIN_DONE=false

for round in $(seq 1 "$ROUNDS"); do
    header "======================================================"
    header "  Round $round / $ROUNDS"
    header "======================================================"

    printf "  %-30s  %-16s  %-14s  %-12s\n" "Pod" "IP" "ResourceCost" "Confidence"
    printf "  %-30s  %-16s  %-14s  %-12s\n" "------------------------------" "----------------" "--------------" "------------"

    COST_ARGS=()
    for name in "${NAMES[@]}"; do
        port=$(port_for "$name")
        ip=$(ip_for "$name")
        raw=$(curl -sf "http://127.0.0.1:${port}/cost?task=pod-scheduling&node=master" 2>/dev/null || echo "{}")
        rc=$(json_get "$raw" "ResourceCost")
        conf=$(json_get "$raw" "Confidence")
        rc="${rc:-0.000}"
        conf="${conf:-0.000}"
        COST_ARGS+=("$name" "$rc")
        printf "  %-30s  %-16s  %-14s  %-12s\n" "$name" "$ip" "$rc" "$conf"
    done
    echo ""

    read -r busiest_name busiest_cost < <(max_key "${COST_ARGS[@]}")
    info "Highest ResourceCost: $busiest_name (cost=$busiest_cost)"

    busiest_port=$(port_for "$busiest_name")
    rec_raw=$(curl -sf -X POST \
        -H "Content-Type: application/json" \
        -d "{\"TaskType\":\"pod-scheduling\",\"SourceNodeID\":\"master\"}" \
        "http://127.0.0.1:${busiest_port}/recommend" 2>/dev/null || echo "{}")

    peer_id=$(json_get "$rec_raw" "PeerID")
    savings=$(json_get "$rec_raw" "ExpectedSavings")
    rationale=$(json_get "$rec_raw" "Rationale")
    error_msg=$(json_get "$rec_raw" "error")

    if [ -n "$error_msg" ]; then
        err "  /recommend error on $busiest_name: $error_msg"
        if echo "$error_msg" | grep -qi "trust"; then
            info "  (ErrInsufficientTrust - all known peers below min-trust threshold)"
        fi
    elif [ -n "$peer_id" ]; then
        announce "$busiest_name recommends: $peer_id (savings=$savings)"
        [ -n "$rationale" ] && info "  rationale: $rationale"
    else
        info "  /recommend returned no peer (possibly no peers registered)"
    fi
    echo ""

    # ── mid-point trust drain: drain the second pod's trust as seen by the first ──
    if [ "$round" -eq "$DRAIN_ROUND" ] && [ "$DRAIN_DONE" = "false" ]; then
        if [ "${#NAMES[@]}" -lt 3 ]; then
            info "Skipping trust drain: need at least 3 di-agent pods"
            DRAIN_DONE=true
            continue
        fi

        DRAIN_HOST="${NAMES[0]}"
        DRAIN_TARGET="${NAMES[1]}"
        host_port=$(port_for "$DRAIN_HOST")
        target_ip=$(ip_for "$DRAIN_TARGET")

        header "  *** Trust drain event at round $round ***"
        peers_raw=$(curl -sf "http://127.0.0.1:${host_port}/peers" 2>/dev/null || echo "[]")
        drain_peer_id=$(python3 -c "
import sys, json
peers = json.load(sys.stdin)
target_url = 'http://${target_ip}:9090'
for p in peers:
    if p.get('url','').rstrip('/') == target_url.rstrip('/'):
        print(p['id'])
        break
" <<< "$peers_raw" 2>/dev/null || echo "")

        if [ -z "$drain_peer_id" ]; then
            err "  Could not find peer ID for $DRAIN_TARGET on $DRAIN_HOST -- skipping drain"
        else
            info "Setting trust for $DRAIN_TARGET (id=$drain_peer_id) on $DRAIN_HOST to 0.15 ..."
            if curl -sf -X POST -H "Content-Type: application/json" \
                    -d '{"value": 0.15}' \
                    "http://127.0.0.1:${host_port}/peers/${drain_peer_id}/trust" >/dev/null 2>&1; then
                ok "  Trust drain applied: $DRAIN_TARGET trust=0.15 on $DRAIN_HOST"
                announce "Trust drain: $DRAIN_TARGET trust=0.15 (< min-trust 0.5) -> expect rerouting next rounds"
            else
                err "  Trust drain request failed for $DRAIN_TARGET on $DRAIN_HOST"
            fi
        fi
        DRAIN_DONE=true
        echo ""
    fi

    [ "$round" -lt "$ROUNDS" ] && sleep "$INTERVAL"
done

ok "Demo complete."

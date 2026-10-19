#!/usr/bin/env bash
# repeat-p2.sh — the counterfactual episode on its own, against the running agent: apply
# mem-wave, wait for discovery to propose its memory against node memory, give the map
# ten minutes of history, then run P2 (confirm, wait for an observation, estimate,
# delete) and keep capturing for the ten minutes the analysis compares against.
# Runs on the node, as root, from this directory.
#
# Usage: sudo ./repeat-p2.sh <out-dir>
# Analyse: python3 analyse.py --counterfactual <out-dir>
set -euo pipefail
cd "$(dirname "$0")"
OUT="$(realpath -m "${1:?usage: $0 <out-dir>}")"
API="${API:-http://localhost:9090}"
mkdir -p "$OUT"
setsid nohup ./capture.sh "$OUT" 30 </dev/null >"$OUT/capture.err" 2>&1 &
k0s kubectl apply -f workloads/mem-wave.yaml >/dev/null
k0s kubectl wait --for=condition=Ready pod/mem-wave --timeout=120s >/dev/null
uid=$(k0s kubectl get pod mem-wave -o jsonpath='{.metadata.uid}')
jq -cn --argjson t "$(date +%s.%N)" --arg s "pod:$uid" \
    '{t: $t, phase: "P2b", event: "applied mem-wave", subject: $s}' >>"$OUT/phases.jsonl"
echo "[$(date +%T)] applied mem-wave pod:$uid"
n=0
for _ in $(seq 120); do
    n=$(curl -sf "$API/candidates?status=all" | jq --arg f "memory_utilization@pod:$uid" \
        '[.[] | select(.FromID == $f and .ToID == "memory_utilization")] | length' || echo 0)
    [ "${n:-0}" -gt 0 ] && break
    sleep 15
done
echo "[$(date +%T)] candidates pending: $n"
sleep 600
status=0
./schedule.sh "$OUT" --phase P2 || status=$?
echo "[$(date +%T)] P2 exit $status"
sleep 780
pkill -f "[c]apture.sh $OUT" || true
echo done

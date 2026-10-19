#!/usr/bin/env bash
# capture.sh — record the agent's API as JSON lines until killed. Runs on the node.
#
# Usage: ./capture.sh <out-dir> [interval-seconds]
#
# Every line of <out-dir>/capture.jsonl is {"t", "endpoint", "status", "body"}, t in the
# node's epoch seconds. A poll that fails (the agent restarting, say) is written with
# status 0 and body null, so a gap is recorded rather than skipped.

set -uo pipefail

OUT="${1:?usage: $0 <out-dir> [interval-seconds]}"
INTERVAL="${2:-30}"
API="${API:-http://localhost:9090}"
mkdir -p "$OUT"
FILE="$OUT/capture.jsonl"

EVERY_TICK=(/state "/candidates?status=all" /state/relationships /relief /relief/actions /state/journal)
EVERY_TENTH=(/state/decisions)
ONCE=(/version /config)

poll() {
    local endpoint="$1" t body status
    t=$(date +%s.%N)
    body=$(curl -s --max-time 10 -w '\n%{http_code}' "$API$endpoint") || body=$'\n0'
    status="${body##*$'\n'}"
    status=$((10#${status:-0}))
    body="${body%$'\n'*}"
    # The body goes through stdin: /state can be larger than one argument may be.
    if [ "$status" -ne 200 ] || ! jq -c --argjson t "$t" --arg e "$endpoint" \
        '{t: $t, endpoint: $e, status: 200, body: .}' <<<"$body" >>"$FILE" 2>/dev/null; then
        jq -cn --argjson t "$t" --arg e "$endpoint" --argjson s "$status" \
            '{t: $t, endpoint: $e, status: $s, body: null}' >>"$FILE"
        return 1
    fi
}

# Each verify decision, once, when an action first reports it: whether the outcome taught
# the map is recorded there, not on the action.
verify_decisions() {
    local id
    for id in $(tail -n 50 "$FILE" | jq -r 'select(.endpoint == "/relief/actions" and .status == 200)
            | .body[]?.verify_id // empty' | sort -u); do
        grep -qxF "$id" "$OUT/.fetched" 2>/dev/null && continue
        poll "/state/decisions/$id" && echo "$id" >>"$OUT/.fetched"
    done
}

jq -cn --argjson t "$(date +%s.%N)" --arg v "$(k0s version 2>/dev/null)" \
    '{t: $t, endpoint: "k0s version", status: 200, body: $v}' >>"$FILE"
for e in "${ONCE[@]}"; do poll "$e"; done

tick=0
while true; do
    for e in "${EVERY_TICK[@]}"; do poll "$e"; done
    verify_decisions
    if [ $((tick % 10)) -eq 0 ]; then
        for e in "${EVERY_TENTH[@]}"; do poll "$e"; done
    fi
    tick=$((tick + 1))
    sleep "$INTERVAL"
done

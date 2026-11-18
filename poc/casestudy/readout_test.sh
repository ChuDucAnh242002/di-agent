#!/usr/bin/env bash
# readout_test.sh — readout.sh against two hand-written runs, one that passes and one that
# fails a representative set of criteria (journal lines digested as the agent writes them).
# Usage: ./readout_test.sh   (exit 0 when every expectation holds)
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT

# journal <dir> <json lines...>: write journal.ndjson lines with the agent's digest.
journal() {
    local dir="$1"; shift
    printf '%s\n' "$@" | python3 -c '
import sys, json, hashlib
for l in sys.stdin:
    b = json.dumps(json.loads(l), separators=(",", ":"))
    print(b[:-1] + ",\"digest\":\"" + hashlib.sha256(b.encode()).hexdigest() + "\"}")' >"$dir/journal.ndjson"
}
ev() { # ev <inc> <seq> <at-seconds> <kind> <target> [detail-json] [decision-json]
    jq -cn --argjson i "$1" --argjson s "$2" --argjson at "$3" --arg k "$4" --arg tg "$5" --argjson d "${6:-null}" --argjson dec "${7:-null}" \
        '{owner: "n", inc: $i, seq: $s, revision: $s, at: ($at | todate | sub("Z$"; "+00:00")), kind: $k, target: $tg, actor: "x"}
         + (if $d then {detail: $d} else {} end) + (if $dec then {decision: $dec} else {} end)'
}
mark() { jq -cn --argjson t "$1" --arg p "$2" --arg e "$3" --argjson i "$4" --argjson s "$5" '{t: $t, mono: $t, phase: $p, event: $e, inc: $i, seq: $s}'; }

T=1800000000
for c in pass fail; do mkdir -p "$TMP/$c"; done
twin() { mark "$1" M1 "applied twin-$2" 1 1 | jq -c --arg s "pod:$2" '. + {subject: $s}'; }
printf '%s\n' "$(mark $T M0 start 1 1)" "$(twin $((T + 600)) a)" "$(twin $((T + 640)) b)" "$(mark $((T + 3000)) M1 end 1 4)" \
    "$(mark $((T + 4800)) M4 start 1 5)" "$(mark $((T + 5160)) M4 end 1 9)" \
    "$(mark $((T + 5220)) M5 start 1 9)" | tee "$TMP/pass/phases.jsonl" >"$TMP/fail/phases.jsonl"
jq -cn --argjson t $T '{t: $t, endpoint: "/state", status: 200, body: {pace: "base", counts: {}}}' >"$TMP/pass/capture.jsonl"
jq -cn --argjson t $T '{t: $t, endpoint: "/state", status: 200, body: {pace: "slow", counts: {}}}' >"$TMP/fail/capture.jsonl"
journal "$TMP/pass" \
    "$(ev 1 1 $T fact.recorded hostname)" \
    "$(ev 1 2 $T property.declared cpu_pressure_ratio)" \
    "$(ev 1 3 $((T + 10)) property.admitted cpu_utilization@unit:di-agent)" \
    "$(ev 1 4 $((T + 60)) decision review-1.4 null '{"id":"review-1.4","kind":"review","answer":{"by":"rules","action":"confirm"}}')" \
    "$(ev 1 5 $((T + 2400)) pace.changed attention '{"from":"base","to":"slow","reason":"calm for 10m0s"}')" \
    "$(ev 1 6 $((T + 4830)) clock.stepped "" '{"wall_delta_seconds":7265,"monotonic_delta_seconds":60}')" \
    "$(ev 1 7 $((T + 5070)) clock.stepped "" '{"wall_delta_seconds":-7130,"monotonic_delta_seconds":60}')" \
    "$(ev 1 8 $((T + 3600)) relationship.declared x '{"association":true,"from":"cpu_utilization@pod:b","to":"cpu_utilization@pod:a"}')"
journal "$TMP/fail" \
    "$(ev 1 1 $T fact.recorded hostname)" \
    "$(ev 1 2 $T restore.failed /var/lib/di-agent/state.json)" \
    "$(ev 1 3 $((T + 300)) property.admitted cpu_utilization@unit:di-agent)" \
    "$(ev 1 4 $((T + 60)) decision review-1.4 null '{"id":"review-1.4","kind":"review","answer":{"by":"systemone","action":"confirm"}}')" \
    "$(ev 1 6 $((T + 4830)) clock.stepped "" '{"wall_delta_seconds":7265,"monotonic_delta_seconds":60}')"

# svc: the self subject from the unit glob fallback, unit:di-agent.service.
# rf-ok / rf-bad: a cold start in incarnation 2 whose restore.failed line verifies, or was
# altered after the agent digested it.
for c in svc rf-ok rf-bad; do mkdir -p "$TMP/$c"; cp "$TMP/pass/phases.jsonl" "$TMP/pass/capture.jsonl" "$TMP/$c/"; done
journal "$TMP/svc" \
    "$(ev 1 1 $T fact.recorded hostname)" \
    "$(ev 1 2 $((T + 10)) property.admitted cpu_utilization@unit:di-agent.service)"
for c in rf-ok rf-bad; do
    echo '{"incarnation": 2}' >"$TMP/$c/after-cold-start-journal.json"
    journal "$TMP/$c" \
        "$(ev 1 1 $T fact.recorded hostname)" \
        "$(ev 2 1 $((T + 6000)) restore.failed /var/lib/di-agent/state.json '{"error":"x","prev_error":"y"}')"
done
sed -i.bak 's#/var/lib/di-agent/state.json#/tampered#' "$TMP/rf-bad/journal.ndjson"

fails=0
expect() { # expect <case> <criterion-substring> <verdict>
    local got
    got=$(grep -F "$2" "$TMP/$1/results-readout.md" | head -1 | awk -F'|' '{gsub(/[ *]/, "", $5); print $5}')
    if [ "$got" = "$3" ]; then echo "ok   $1: $2 -> $3"; else echo "FAIL $1: $2 -> got '${got:-missing}', want $3"; fails=$((fails + 1)); fi
}
for c in pass fail; do "$HERE/readout.sh" "$TMP/$c" >/dev/null 2>&1; done
for c in pass fail; do
    expect "$c" "cold start journalled: first non-fact event" "$c"
    expect "$c" "no review decision names a model" "$c"
    expect "$c" "pace base at start" "$c"
    expect "$c" "self observed" "$c"
    expect "$c" "two clock.stepped" "$c"
    expect "$c" "twins associated by persistent-correlation" "$c"
done
grep -F "twins associated" "$TMP/pass/results-readout.md" | grep -q '"minute":60,"by_m1_end":false,"pace":"slow"' \
    && echo "ok   pass: twins reading has the minute and the pace then" \
    || { echo "FAIL pass: twins reading lacks minute 60 / pace slow"; fails=$((fails + 1)); }
expect pass "interval: sensitivity_low" unverifiable
for c in svc rf-ok rf-bad; do "$HERE/readout.sh" "$TMP/$c" >/dev/null 2>&1; done
expect svc "self observed" pass
expect rf-ok "restore.failed reaches the durable log" pass
expect rf-bad "restore.failed reaches the durable log" fail
exit $((fails > 0))

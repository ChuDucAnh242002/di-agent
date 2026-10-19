#!/usr/bin/env bash
# schedule.sh — run the case study's phases. Runs on the node, as root.
#
# Usage: sudo ./schedule.sh <out-dir>              the whole run, about 2 h 10 min
#        sudo ./schedule.sh <out-dir> --phase P2   one phase, against the running agent
#
# Starts the agent itself (from ./agent, ./agent.yaml, ./agent-relief.yaml) on a fresh map:
# a snapshot already on disk is moved aside, never deleted. Every event is a JSON line in
# <out-dir>/phases.jsonl: {"t", "phase", "event", ...ids}. SCALE=0.1 runs the timetable at
# a tenth of its length, for trying the script out.

set -euo pipefail

OUT="${1:?usage: $0 <out-dir> [--phase P0..P5]}"
ONLY="${3:-}"
[ "${2:-}" = "--phase" ] || [ -z "${2:-}" ] || { echo "unknown argument $2" >&2; exit 2; }

HERE="$(cd "$(dirname "$0")" && pwd)"
API="${API:-http://localhost:9090}"
SCALE="${SCALE:-1}"
STATE=/var/lib/di-agent/state.json
KUBECTL=(k0s kubectl)
DISCOVERY=(cycler mem-wave mem-squeeze io-wave twin-a twin-b noise)

mkdir -p "$OUT"
PHASES="$OUT/phases.jsonl"

# mark <phase> <event> [jq-args producing extra fields as $x]
mark() {
    local phase="$1" event="$2"; shift 2
    jq -cn --argjson t "$(date +%s.%N)" --arg p "$phase" --arg e "$event" "$@" \
        '{t: $t, phase: $p, event: $e} + ($ARGS.named | del(.t, .p, .e))' >>"$PHASES"
    echo "[$(date +%T)] $phase: $event"
}

# at <minutes>: sleep until that many (scaled) minutes after the agent started.
at() {
    local target now
    target=$(awk -v t0="$T0" -v m="$1" -v s="$SCALE" 'BEGIN { printf "%d", t0 + m * 60 * s }')
    now=$(date +%s)
    [ "$target" -gt "$now" ] && sleep $((target - now))
    return 0
}

agent_start() {
    local config="$1"
    (cd "$HERE" && setsid nohup ./agent -config "$config" >>"$OUT/agent.log" 2>&1 </dev/null &)
    for _ in $(seq 60); do
        curl -sf "$API/healthz" >/dev/null && return 0
        sleep 1
    done
    echo "agent did not come up on $config" >&2
    exit 1
}

agent_stop() {
    local pid
    pid=$(pgrep -x agent || true)
    [ -z "$pid" ] && return 0
    kill -TERM $pid
    while pgrep -x agent >/dev/null; do sleep 1; done
}

revision() { curl -sf "$API/state" | jq '.revision'; }

uid_of() { "${KUBECTL[@]}" get pod "$1" -o jsonpath='{.metadata.uid}' 2>/dev/null || true; }

apply() { "${KUBECTL[@]}" apply -f "$HERE/workloads/$1.yaml" >/dev/null; }

remove() { "${KUBECTL[@]}" delete pod "$@" --ignore-not-found --wait=true >/dev/null; }

phase_prepare() {
    agent_stop
    if [ -e "$STATE" ]; then
        local aside="$STATE.pre-casestudy-$(date +%Y%m%dT%H%M%S)"
        mv "$STATE" "$aside"
        mark prep "snapshot moved aside" --arg path "$aside"
    fi
    remove "${DISCOVERY[@]}" hog
    mark prep "planted pods removed"
}

phase_P0() {
    agent_start agent.yaml
    T0=$(date +%s)
    mark P0 "agent started, fresh map" --argjson revision "$(revision)"
}

phase_P1() {
    at 10
    for pod in "${DISCOVERY[@]}"; do
        apply "$pod"
        mark P1 "applied $pod" --arg subject "pod:$(uid_of "$pod")"
        sleep 41
    done
}

phase_P2() {
    [ -n "${T0:-}" ] && at 50
    local uid subject ids id estimate status decision
    uid=$(uid_of mem-wave)
    if [ -z "$uid" ]; then
        mark P2 "refused: mem-wave is not running, nothing deleted"
        exit 1
    fi
    subject="pod:$uid"

    # The operator confirms what discovery proposed about mem-wave and node memory, so the
    # estimate has a relationship to reason from. With review off nothing else confirms it.
    ids=$(curl -sf "$API/candidates?status=pending" | jq -r --arg s "$subject" \
        '.[] | select(.FromID == "memory_utilization@" + $s and .ToID == "memory_utilization") | .CandidateID')
    if [ -z "$ids" ]; then
        mark P2 "no pending candidate from mem-wave memory to node memory" --arg subject "$subject"
    fi
    for id in $ids; do
        curl -sf -X POST "$API/candidates/$(jq -rn --arg i "$id" '$i | @uri')/confirm" >/dev/null
        mark P2 "operator confirmed candidate" --arg candidate "$id"
    done

    estimate=$(curl -s -w '\n%{http_code}' "$API/state/estimate?target=memory_utilization&without=$subject")
    status="${estimate##*$'\n'}"
    estimate="${estimate%$'\n'*}"
    decision=$(jq -r '.decision_id // empty' <<<"$estimate" 2>/dev/null || true)
    # A 200 alone proves nothing: the map answers for a subject it does not hold, and
    # says so only by leaving it out of "excluded".
    if [ "$status" != 200 ] || [ -z "$decision" ] ||
        ! jq -e --arg s "$subject" '.excluded | index($s)' >/dev/null <<<"$estimate"; then
        mark P2 "refused: the estimate did not exclude mem-wave, nothing deleted" \
            --arg subject "$subject" --arg status "$status"
        exit 1
    fi
    mark P2 "estimate without mem-wave" --arg subject "$subject" --arg decision_id "$decision" \
        --argjson level "$(jq '.answer.level' <<<"$estimate")" \
        --argjson projected "$(jq '.hypothetical.projected_level' <<<"$estimate")"

    remove mem-wave
    mark P2 "deleted mem-wave" --arg subject "$subject"
}

phase_P3() {
    at 65
    remove cycler mem-squeeze io-wave twin-a twin-b noise
    mark P3 "remaining planted pods removed"
    local before after
    before=$(revision)
    agent_stop
    mark P3 "agent stopped" --argjson revision "$before"
    agent_start agent-relief.yaml
    after=$(revision)
    mark P3 "agent restarted from snapshot, relief on" --argjson revision "$after"
}

phase_P4() {
    at 72
    apply hog
    mark P4 "applied hog" --arg subject "pod:$(uid_of hog)"
    at 77
    if [ "$(curl -sf "$API/relief" | jq -r '.phase')" != relieving ]; then
        mark P4 "relief not active five minutes after hog"
        echo "relief not active" >&2
    fi
}

phase_P5() {
    at 117
    remove hog
    mark P5 "deleted hog"
    at 127
    mark P5 "run ends" --argjson revision "$(revision)"
}

if [ -n "$ONLY" ]; then
    "phase_$ONLY"
    exit 0
fi

phase_prepare
phase_P0
phase_P1
phase_P2
phase_P3
phase_P4
phase_P5

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
# --phase with no name would otherwise run the whole timetable, whose first step stops
# the running agent and moves its snapshot aside.
[ "${2:-}" != "--phase" ] || [ -n "$ONLY" ] || { echo "--phase needs a phase name (P0..P5)" >&2; exit 2; }

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

# agent_stop asks the agent to stop and waits up to a minute for it to flush its
# snapshot; one that has not gone by then is killed, and the mark says so.
agent_stop() {
    local pid
    pid=$(pgrep -x agent || true)
    [ -z "$pid" ] && return 0
    kill -TERM $pid
    for _ in $(seq 60); do
        pgrep -x agent >/dev/null || return 0
        sleep 1
    done
    pkill -KILL -x agent || true
    mark agent "did not stop within 60 s; killed"
    while pgrep -x agent >/dev/null; do sleep 1; done
}

# The map's revision, or null when the agent does not answer: a mark records the gap
# rather than set -e ending the timetable mid-phase.
revision() { curl -sf "$API/state" | jq '.revision' 2>/dev/null || echo null; }

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
    remove "${DISCOVERY[@]}" hog victim
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
    at 50
    local uid subject ids id estimate status decision
    uid=$(uid_of mem-wave)
    if [ -z "$uid" ]; then
        mark P2 "refused: mem-wave is not running, nothing deleted"
        return 1
    fi
    subject="pod:$uid"

    # The operator confirms what discovery proposed about mem-wave and node memory, so the
    # estimate has a relationship to reason from. With review off nothing else confirms it.
    ids=$(curl -sf "$API/candidates?status=pending" | jq -r --arg s "$subject" \
        '.[] | select(.FromID == "memory_utilization@" + $s and .ToID == "memory_utilization") | .CandidateID')
    if [ -z "$ids" ]; then
        mark P2 "refused: no pending candidate from mem-wave memory to node memory, nothing deleted" \
            --arg subject "$subject"
        return 1
    fi
    for id in $ids; do
        if ! curl -sf -X POST "$API/candidates/$(jq -rn --arg i "$id" '$i | @uri')/confirm" >/dev/null; then
            mark P2 "refused: confirming $id failed, nothing deleted" --arg subject "$subject"
            return 1
        fi
        mark P2 "operator confirmed candidate" --arg candidate "$id"
    done

    # A relationship confirmed a moment ago has no strength yet, and the estimate leaves
    # it out of the projection (saying so in a caveat). Ask once it has been observed.
    local observed=0
    for _ in $(seq 60); do
        observed=$(curl -sf "$API/state/relationships" | jq --arg f "memory_utilization@$subject" \
            '[.relationships[] | select(.from == $f and .to == "memory_utilization") | .n_observations] | max // 0' || echo 0)
        [ "$observed" -gt 0 ] && break
        sleep 5
    done
    if [ "$observed" -eq 0 ]; then
        # Asked now, the estimate would leave the relationship out and project no change.
        mark P2 "refused: the confirmed relationship has no observation after 5 min, nothing deleted" \
            --arg subject "$subject"
        return 1
    fi
    mark P2 "relationship observed" --arg subject "$subject" --argjson n_observations "$observed"

    estimate=$(curl -s -w '\n%{http_code}' "$API/state/estimate?target=memory_utilization&without=$subject") || estimate=$'\n0'
    status="${estimate##*$'\n'}"
    estimate="${estimate%$'\n'*}"
    decision=$(jq -r '.decision_id // empty' <<<"$estimate" 2>/dev/null || true)
    # A 200 alone proves nothing: the map answers for a subject it does not hold, and
    # says so only by leaving it out of "excluded".
    if [ "$status" != 200 ] || [ -z "$decision" ] ||
        ! jq -e --arg s "$subject" '.excluded | index($s)' >/dev/null <<<"$estimate"; then
        mark P2 "refused: the estimate did not exclude mem-wave, nothing deleted" \
            --arg subject "$subject" --arg status "$status"
        return 1
    fi
    mark P2 "estimate without mem-wave" --arg subject "$subject" --arg decision_id "$decision" \
        --argjson level "$(jq '.answer.level' <<<"$estimate")" \
        --argjson projected "$(jq '.hypothetical.projected_level' <<<"$estimate")"

    if ! remove mem-wave; then
        mark P2 "deleting mem-wave failed" --arg subject "$subject"
        return 1
    fi
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
    local snapshot=false
    [ -e "$STATE" ] && snapshot=true
    agent_start agent-relief.yaml
    after=$(revision)
    mark P3 "agent restarted, relief on" --argjson revision "$after" --argjson from_snapshot "$snapshot"
}

# Two relief episodes, because a relationship needs two agreeing outcomes and the
# policy cools a workload down for ten minutes after it is touched. The victim leaving
# ends the first (its cap is released); it comes back after its properties have gone
# stale, so its earlier self is not counted as a workload that left mid-outcome.
phase_P4() {
    at 72
    apply hog
    mark P4 "applied hog" --arg subject "pod:$(uid_of hog)"
    apply victim
    mark P4 "applied victim" --arg subject "pod:$(uid_of victim)"
    at 77
    relief_active "first episode"
    at 88
    remove victim
    mark P4 "deleted victim"
    at 95
    apply victim
    mark P4 "applied victim" --arg subject "pod:$(uid_of victim)"
    at 100
    relief_active "second episode"
}

relief_active() {
    if [ "$(curl -sf "$API/relief" | jq -r '.phase')" != relieving ]; then
        mark P4 "relief not active five minutes into the $1"
        echo "relief not active ($1)" >&2
    fi
}

phase_P5() {
    at 117
    remove hog victim
    mark P5 "deleted hog and victim"
    at 127
    mark P5 "run ends" --argjson revision "$(revision)"
}

# One phase against the running agent, at its real pace: the clock is set as though
# the run had started that phase's offset ago.
if [ -n "$ONLY" ]; then
    declare -A START=([P0]=0 [P1]=10 [P2]=50 [P3]=65 [P4]=72 [P5]=117)
    T0=$(awk -v n="$(date +%s)" -v m="${START[$ONLY]:?unknown phase $ONLY}" -v s="$SCALE" 'BEGIN { printf "%d", n - m * 60 * s }')
    "phase_$ONLY"
    exit $?
fi

phase_prepare
phase_P0
phase_P1
# A refused P2 is marked and skipped; the rest of the timetable still runs, so the
# planted pods are removed and relief is still exercised.
phase_P2 || mark P2 "skipped; the timetable continues"
phase_P3
phase_P4
phase_P5

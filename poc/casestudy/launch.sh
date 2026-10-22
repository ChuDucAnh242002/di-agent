#!/usr/bin/env bash
# launch.sh — start the capture and the whole timetable, detached, so they outlive the
# shell that started them. Runs on the node, as root, from this directory.
#
# Usage: sudo ./launch.sh <out-dir>
#
# capture.sh polls every 30 s into <out-dir>/capture.jsonl until it is stopped by hand
# after schedule.out says "done exit=0" (its pid is in <out-dir>/capture.pid);
# schedule.sh writes <out-dir>/phases.jsonl.
set -euo pipefail
cd "$(dirname "$0")"
OUT="$(realpath -m "${1:?usage: $0 <out-dir>}")"
mkdir -p "$OUT"
setsid nohup ./capture.sh "$OUT" 30 </dev/null >"$OUT/capture.err" 2>&1 &
echo $! >"$OUT/capture.pid"
# "done" carries the timetable's exit status, so a run that stopped early does not
# read as one that finished.
setsid bash -c "./schedule.sh '$OUT'; echo \"done exit=\$?\"" </dev/null >"$OUT/schedule.out" 2>&1 &
echo "capture and schedule started into $OUT"

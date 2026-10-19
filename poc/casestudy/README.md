# End-to-end case study

One machine, one run: workloads arrive, change and leave, and the agent's map, discovery,
counterfactual estimate, relief loop and learning from its own actions are all exercised and traced
back to map state. It is a demonstration, not a measurement: one run, no repetitions.

## The machine

A Multipass VM (Ubuntu 24.04, arm64, 4 vCPU, 8 GB, cgroup v2) with k0s **v1.36.4+k0s.1** as a single
node, installed by `poc/scripts/02-k0s.sh`. The agent runs on the VM as root from
`agent.yaml`, then `agent-relief.yaml`.

## The workloads and what each should produce

Each moves one resource on its own period, on cycles that never line up, so pods should not
correlate with each other — except the twins, which share a wall-clock schedule.

| Pod | Moves | Period | Planted relationship |
|---|---|---|---|
| `cycler` | CPU, 45 s busy / 45 s idle, limit 1 core | 90 s | its CPU → node CPU; its CPU → node CPU pressure |
| `mem-wave` | memory, 400 MB held / freed | 131 s | its memory → node memory |
| `mem-squeeze` | memory pressure, buffered writes through a 64 Mi limit | 173 s | its memory pressure → node memory pressure |
| `io-wave` | direct-I/O writes | 97 s | its I/O → node I/O pressure |
| `twin-a`, `twin-b` | CPU on one wall clock, throttled at 200m | 113 s | twin-a CPU ↔ twin-b CPU |
| `noise` | CPU in random bursts | none | nothing |
| `hog` | CPU, steady, four busy loops, limit 3 cores | none | relief phase only: its CPU → node CPU contention, learned by intervention |
| `victim` | CPU, two busy loops, limit 4 cores, never throttled, frozen by policy | none | relief phase only: what relief protects; the workload whose waiting is the contention |

The relationships marked with a node quantity that has several drivers (node CPU pressure, node I/O
pressure) were missed in earlier runs: no single pair reaches the threshold.

## The run

`schedule.sh` drives it; `capture.sh` records the agent's API alongside. Times are from agent start.

| Phase | t (min) | What happens |
|---|---|---|
| P0 baseline | 0–10 | k0s system pods only |
| P1 arrival | 10–50 | the seven discovery pods applied 41 s apart |
| P2 counterfactual + departure | 50 | the agent is asked for node memory without `mem-wave`, and the decision id kept; then `mem-wave` is deleted |
| P2 observe | 50–65 | `mem-wave`'s properties go stale (2 min) and retire (10 min) |
| P3 quiesce + restart | 65–72 | the other planted pods are deleted; the agent restarts from its snapshot on `agent-relief.yaml` |
| P4 relief | 72–117 | `hog` and `victim` applied; relief caps hog and verifies (first episode); `victim` deleted at 88, so contention ends and the cap is released; `victim` back at 95 (second episode) |
| P5 release | 117–127 | `hog` and `victim` deleted |

Relief runs on its own pair because a result counts as evidence only when no other workload moved
during its window, and the planted pods move continuously. Its signal is node CPU contention, the
share of time some workload waited for a CPU another was using, so it needs a victim: `hog` alone
at its limit is throttled, not contended. A relationship needs two agreeing results and the policy
cools a workload down for 10 minutes after it is touched, hence two episodes.

```bash
OUT=run-$(date +%F)
./capture.sh di-node "$OUT" &      # polls every 30 s until killed
./schedule.sh di-node "$OUT"       # about 2 h 10 min
kill %1
```

Both write JSON lines stamped with the VM's clock: `capture.jsonl` (`t`, `endpoint`, `status`,
`body`; a failed poll has `status: 0` and `body: null`) and `phases.jsonl` (`t`, `phase`, `event`, and
any ids).

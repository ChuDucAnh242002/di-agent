# Memory relief with caps kept until expiry, 2026-11-02

This run used `memrelief.sh 45 10` on the same node and workload as [run c](../run-2026-10-30c-memrelief/results.md).
- **Agent:** `99378ac` (`/version` shows it). It has the memory lever at `enter` 0.08 / `exit` 0.03 / `min_effect` 0.01, no further step below `enter`, and caps kept until expiry.
- **Raw files:** on the node, in `/home/ubuntu/casestudy/run-2026-11-02-memrelief/`.
- **Times:** EET.

**Outcome: one cap relieved the victim for the rest of phase 1 with no flapping. But it relieved it by stalling page-hog outright, not by trimming its cache.**

## Timeline

| time | what happened |
|---|---|
| 22:23–22:28 | page-hog takes 6 GiB anonymous memory and grows to 5 files. mem-victim refaults at 0.31–0.47/s. |
| 22:29–22:37 | `memory_contention_ratio` 0.056–0.106. |
| 22:37:51 | **One step:** page-hog `memory.high` set to 5600 MiB (contention at act 0.102). |
| 22:40–23:09 | Contention at 0 throughout. The step was **kept**. The lever went to `releasing` at 22:41 and kept the cap: no give-back and no return of contention, unlike run c. 1 action, 0 reverted, 0 refused. |
| 23:09 | page-hog and mem-victim deleted. page-hog's ledger entry stays until its expiry at 23:37, capping nothing. |
| 23:14–23:25 | Phase 2: mem-squeeze alone. Contention stayed at 0, and no action was taken. |

## The cap stalled page-hog

From 22:38, page-hog read `cpu_utilization` 0 and `memory_pressure_ratio` 0.87–0.91, with about 68 `memory_events_high` per window, until it was deleted. Its log has no line after 22:37.

The cap (5600 MiB) sat below its anonymous memory (6 GiB = 6144 MiB). The node has no swap, so reclaim cannot bring the pod under its cap, and the kernel throttles every allocation it makes: the pod is frozen, not trimmed. Run c's caps (5780 and 5972 MiB) were below 6144 MiB too.

The plan starts from `memory.current`, page cache included, and steps a quarter below it. It never asks how much of that reclaim can reach.

**What this means:** `memory.high` is meant as a soft cap that trims what can be reclaimed. A cap below the pod's unreclaimable memory turns it into a pause. The plan should not set `memory.high` below what reclaim cannot take: on a node with no swap, that is the pod's anonymous memory.

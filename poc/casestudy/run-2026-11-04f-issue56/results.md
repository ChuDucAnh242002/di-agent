# Memory relief, bounded discovery checkpoint, 2026-11-04 (f)

This run used `memrelief.sh 45 0` on the same node and workload as [run 2026-11-03c](../run-2026-11-03c-memrelief/results.md). It checks issue #56: since the restart-safe discovery checkpoint (#52), the agent's memory and its state snapshot grew without bound.
- **Agent:** `25a2aaa`: the checkpoint keeps only the pair windows of pairs with a candidate or a group link (`f3e83f3`), and `Map.Snapshot` deep-copies properties, so `Save` encoding after the lock is released no longer races label merges.
- **Node:** 16 GiB, 12 CPUs, 5 s tick; pair budget 60 000.
- **Sampling:** every 30 s, the `di-agent.service` cgroup's `anon` memory, the size of `/var/lib/di-agent/state.json`, and `/state`'s discovery counts.
- **Raw files:** on the node, in `/home/ubuntu/casestudy/run-2026-11-04f-issue56/` (`mem.jsonl` holds the samples).
- **Times:** EET.

**Outcome: memory level from minute 10 at 238–267 MiB, the snapshot under 8 MB, no crash in 54 minutes; one cap, kept.**

## Memory and snapshot

| minutes after start | anon (MiB) | snapshot (MB) | tested pairs | properties |
|---|---|---|---|---|
| 3 | 99 | 0.4 | 32 385 | 255 |
| 6 | 177 | 1.8 | 35 781 | 269 |
| 9 | 235 | 3.8 | 35 781 | 269 |
| 12 | 260 | 5.0 | 36 046 | 269 |
| 18 | 256 | 5.8 | 35 781 | 269 |
| 24 | 251 | 6.1 | 36 046 | 269 |
| 30 | 248 | 6.3 | 36 046 | 269 |
| 36 | 265 | 7.0 | 36 046 | 269 |
| 42 | 265 | 7.4 | 36 046 | 269 |
| 48 | 266 | 7.5 | 35 781 | 269 |
| 54 | 248 | 7.8 | 28 925 | 243 |

For comparison, on the same node:

| agent | anon | snapshot |
|---|---|---|
| `main` with #52 (issue #56) | 1081 MiB at 6 minutes, still climbing | 103 MB at 5 minutes |
| `ff1a4f9`, before #52 | level at about 233 MiB | none of discovery |
| this run | level at 238–267 MiB from minute 10 | 7.8 MB at 54 minutes |

The snapshot still grows slowly, with candidates and journal entries, not with the pairs tested. After the pods were deleted at 22:07 their properties retired (269 → 243) and the tested pairs fell with them.

## Timeline

| time | what happened |
|---|---|
| 21:17:53 | Agent started. |
| 21:19:53 | Baseline; mem-victim applied. |
| 21:20:59 | page-hog applied. |
| 21:26:37 | **One step:** page-hog `memory.high` 7210 → 6189 MiB. Predicted drop in `memory_contention_ratio` 0.1150, the whole of it at act. |
| 21:28:52 | Verify: observed drop 0.1146; **kept**. |
| 21:28–22:07 | The lever in `releasing` with the cap kept: 1 action, 1 kept, 0 reverted, 0 refused. |
| 22:07:02 | page-hog and mem-victim deleted. |
| 22:12:16 | Finished; agent active, no `fatal` or `panic` in its log. |

## What it shows

- Saving only the windows something rests on keeps the agent's memory at the level it had before the checkpoint existed, where saving every tested pair's window grew it past a gigabyte in minutes.
- The run before this one (`f3e83f3`, without the deep copy) died at minute 39 with `fatal error: concurrent map iteration and map write` in `Map.Save`: the snapshot shared each property's `Labels` with the live map. With the deep copy it ran to the end.
- Relief is unchanged by either fix: one cap, predicted and observed alike to remove the contention.

# Memory relief, floored cap and floored prediction, 2026-11-03 (c)

This run used `memrelief.sh 45 10` on the same node and workload as [run 2026-11-03b](../run-2026-11-03b-memrelief/results.md).
- **Agent:** `ff1a4f9`: `memory.high` never below `memory.current − file + shmem`, and a floored usage-basis step predicted as pressure × (from − to) / (from − floor).
- **Raw files:** on the node, in `/home/ubuntu/casestudy/run-2026-11-03c-memrelief/`.
- **Times:** EET.

**Outcome: one cap at the floor, predicted and observed alike to remove all the contention; kept, never given back. page-hog kept running under it, its stall about 0.01.**

## Timeline

| time | what happened |
|---|---|
| 14:34–14:45 | page-hog takes 6 GiB anonymous memory and grows its page cache to 1.0–1.4 GiB (memory.current up to 7540 MiB). mem-victim's cache falls to 665–697 MiB of its 1024. |
| 14:45:07 | **One step:** page-hog `memory.high` 7479 → 6175 MiB, the floor (usage basis). Predicted drop 0.0857, the whole of the contention at act (0.0857). |
| 14:47 | Verify: observed drop 0.0854; **kept**. mem-victim's cache back at 1024 MiB from 14:46 to the end. |
| 14:47–15:19 | The lever in `releasing` with the cap kept: 1 action, 1 kept, 0 reverted, 0 refused. |
| 15:24–15:35 | Phase 2: mem-squeeze alone. No action. |

## page-hog under the cap

Its `memory.current` sat at 6174–6183 MiB against the 6175 MiB cap, its page cache 0–9 MiB. The agent's `memory_pressure_ratio` for it was 0.003–0.014 (0.85–0.93 in the frozen run 2026-11-02), and its CPU fell from 0.06–0.07 of the node to 0.008–0.020. Its log kept advancing, to 38 files by 15:18: it writes on, each file reclaimed as it goes.

## What it shows

- The floor keeps the cap where reclaim can meet it: the pod is trimmed of its cache, not frozen.
- The floored prediction matched the observed drop (0.0857 against 0.0854) where the share-of-usage one said 0.008 and refused (run 2026-11-03b).
- With caps kept until expiry, one step held the victim relieved for the 34 minutes left in phase 1.

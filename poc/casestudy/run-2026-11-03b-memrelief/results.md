# Memory relief with the reclaimable-cache floor, 2026-11-03 (b)

This run used `memrelief.sh 45 10` on the same node and workload as [run 2026-11-03](../run-2026-11-03-memrelief/results.md).
- **Agent:** `a3cdf54`: `memory.high` never below `memory.current − file + shmem`.
- **Raw files:** on the node, in `/home/ubuntu/casestudy/run-2026-11-03b-memrelief/`.
- **Times:** EET.

**Outcome: no action. The lever was relieving from 13:26 to 14:07 at contention 0.08–0.12, and refused 19 plans: the floored step's usage-basis prediction (about 0.008) fell under `min_effect` (0.01).**

## Timeline

| time | what happened |
|---|---|
| 13:21–13:26 | page-hog takes 6 GiB anonymous memory and grows to 4 files (memory.current 7208 MiB, 1024 MiB of it page cache). It then holds at 4 files: mem-victim refaults at 0.24–0.33/s, over page-hog's 0.05/s threshold. |
| 13:26–14:07 | `memory_contention_ratio` 0.08–0.12. The lever is relieving and refuses a plan about every two minutes, 19 in all. mem-victim's cache sits at 770–880 MiB of its 1024. |
| 14:07 | page-hog and mem-victim deleted; contention 0. |
| 14:12–14:23 | Phase 2: mem-squeeze alone. Contention 0, no action. |

## Why every plan was refused

The poll does not carry the refusal text; the numbers it does carry reproduce it. page-hog's share was 0.452 of `memory_total` (7209 MiB), node `memory_utilization` 0.852, contention about 0.105.

- Step: 7209 × 0.75 = 5407 MiB. Floor: 7208 − 1024 + 0 = 6184 MiB. The floor holds, so the cut is 1 − 6184/7209 = 0.142.
- Usage-basis prediction: 0.452 × 0.142 × 0.105 / 0.852 ≈ 0.008, under `min_effect` 0.01.

## What this means

The floor is right: the cap it allows takes only page cache. But the usage-basis prediction under-predicts what taking a culprit's cache does for the victim. Over the memory runs it predicted 0.010–0.025 and observed drops of 0.086–0.107 (run 2026-10-30c twice, run 2026-11-03 once). The prediction treats pressure as spread over all memory in use, anon included; the pressure here comes from the competing cache alone. With the floor making cuts smaller, the first step no longer clears `min_effect`, and without a first step the learned effect, which would correct the prediction, never gets a sample.

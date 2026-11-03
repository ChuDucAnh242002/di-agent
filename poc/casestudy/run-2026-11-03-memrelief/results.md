# Memory relief with the anon floor, 2026-11-03

This run used `memrelief.sh 45 10` on the same node and workload as [run 2026-11-02](../run-2026-11-02-memrelief/results.md).
- **Agent:** `084bf0d`: as `99378ac`, plus `memory.high` never below the pod's `anon` from `memory.stat`.
- **Raw files:** on the node, in `/home/ubuntu/casestudy/run-2026-11-03-memrelief/`.
- **Times:** EET.

**Outcome: one cap, at the anon floor, relieved the victim for the rest of phase 1, and page-hog kept running. It ran heavily throttled, though: the floor leaves out the pod's kernel memory, so the cap still sat just under what reclaim could reach.**

## Timeline

| time | what happened |
|---|---|
| 11:57–12:04 | page-hog takes 6 GiB anonymous memory and grows to 5 files (memory.current 7473 MiB). mem-victim's page cache falls from 1024 to 693 MiB. |
| 12:02–12:07 | `memory_contention_ratio` 0.091–0.111. Two plans refused before the act. |
| 12:07:46 | **One step:** page-hog `memory.high` 7474 → 6146 MiB (6444064768 bytes, the anon floor; usage basis, predicted drop 0.010). A step without the floor would have gone to 5605 MiB. |
| 12:09 | Contention 0.004, then about 0 for the rest of the run. Verify: observed drop 0.107; **kept**. mem-victim's cache back at 1024 MiB from 12:07 until the end. |
| 12:09–12:42 | 1 action, 1 kept, 0 reverted. The cap stayed in force; no give-back, no return of contention. |
| 12:48–12:59 | Phase 2: mem-squeeze alone. Contention stayed at 0, and no action was taken. |

## page-hog under the cap

Unlike run 2026-11-02, page-hog was not frozen: its log kept advancing, from 29 to 36 files between 12:33 and 12:42 (one 256 MiB file every 1–1.5 min, against one a minute before the cap). Its page cache stayed at 0 throughout, so every file it wrote was reclaimed at once.

But it ran under heavy throttling: its memory PSI `some` avg60 was 0.33–0.90 for most of the time under the cap, and `memory.events high` rose by about 40,000 a minute. Its `memory.current` sat at 6163–6180 MiB, 17–34 MiB over the cap: kernel memory (page tables, slab) that is charged to the pod, is not `anon`, and reclaim cannot fully take back. With the page cache at 0 there was nothing left to reclaim, so the pod stayed over its cap and was throttled on every allocation.

**What this means:** `anon` alone puts the cap at the edge of what reclaim can reach, not inside it. A floor of everything but the pod's reclaimable page cache (`memory.current` less `file`, plus `shmem`, which needs swap to reclaim) would have left the kernel memory above the cap too. Even then, a cap that takes all of a pod's page cache leaves it no room to do file I/O without throttling.

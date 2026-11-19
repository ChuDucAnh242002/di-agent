# M6 alone, three runs, 2026-11-19: the squeeze holds past the lever's hold

Master run 4 failed M6: `memory_contention_ratio` reached 0.144 but stayed above the memory lever's
enter threshold (0.08) for only 33–88 s, short of its 1-minute hold, so relief never acted.

## Why the squeeze decayed

The poll of run 4 shows when it ended. mem-victim refaulted 21:05–21:07 (VM 23:05–23:07), when
page-hog's set reached 8 files. From 21:07:10, before the hog wrote file 9, the victim's refault ratio
fell to about 0.002/s and the hog's own rose to 0.13–0.15/s. The kernel had moved eviction onto the
hog's cache. The hog then kept growing, one file a minute, and every added file only thrashed its own
set. The victim reads its 1 GiB every second, and the kernel kept it cached.

Which side loses depends on how much room page cache has. A standalone check (victim and hog, no
agent) shows this:

| anonymous held | room left (MemAvailable) | victim squeezed? |
| --- | --- | --- |
| 11 GiB (as in master runs 3–4) | ~3.4 GiB | never. The hog thrashed itself from 7 files and the victim did not refault in 11 min |
| until 2 GiB left | ~2.1 GiB | yes. Held at 5 files, victim refaults 0.48–0.58/s for 19 min, stall around 0.08 |
| until 1.5 GiB left | ~1.6 GiB | yes. Held at 2 files, victim refaults 0.44–0.48/s for 15 min, stall 0.08–0.11 |

The memrelief run 2026-11-04d also held steady, at 14% MemAvailable (about 2.1 GiB). The fixed 11 GiB
left about 3 GiB, and how much that was depended on whatever else the node ran.

## Change

- `page-hog` takes anonymous memory in 256 MiB pieces until MemAvailable is `ROOM_MIB` (1536). It no
  longer holds a fixed `ANON_GIB`. `master.sh` no longer overrides the amount.
- `PHASES=M6 master.sh <out-dir>` runs M6 alone on a fresh map: prepare, the agent started, M6, M9's
  reads, then teardown, about 45 min. To do this, M9's reads moved into `m9_read`.

The agent and the lever thresholds are unchanged. The build is origin/main 7f3d3e1.

## Three runs, `PHASES=M6`, one after another, a fresh map each time

| Run | Hog held at | Peak contention (map) | Above 0.08 (map) | Relief act | Verify citing it |
| --- | --- | --- | --- | --- | --- |
| m6-1 | 12.5 GiB anon, 3 files | 0.090 | 70 s, then the cap relieved it | `relief-act-memory.high-1.329` | `relief-verify-memory.high-1.340`, kept |
| m6-2 | 12.5 GiB anon, 3 files | 0.100 | 71 s, then the cap relieved it | `relief-act-memory.high-1.326` | `relief-verify-memory.high-1.337`, kept |
| m6-3 | 12.5 GiB anon, 3 files | 0.096 | 66 s, then the cap relieved it | `relief-act-memory.high-1.335` | `relief-verify-memory.high-1.337`, kept |

"Above 0.08" ends when relief acts. The cap drops contention to about 0 within 4–8 min, and the lever goes
calm→relieving→releasing. Without the agent, the same squeeze stayed up for 15–19 min (table above).
The peak is lower than in runs 2–4 (0.09–0.10 against 0.14–0.15), because the squeeze is now
sustained rather than a spike. Its margin over 0.08 is small. There were no OOM kills, and no kill
criterion fired.

## M6 readout rows (`readout.sh`)

| Criterion | m6-1 | m6-2 | m6-3 |
| --- | --- | --- | --- |
| relief acts and verifies | pass | pass | pass |
| relief.phase calm→relieving→releasing | pass | pass | pass |
| chain resolves (?chain=1) | pass | pass | pass |
| calibration fed (relief.memory.high n=1, hit_rate 1) | pass | pass | pass |
| intervention learning | not exercised | not exercised | not exercised |
| narrative (explain) | fail | fail | pass |
| durable log, evicted id answers from: log | fail | fail | fail |
| no operator | pass | pass | pass |

Neither fail is a relief defect:

- **Narrative:** a readout rounding artefact. The readout rounds predicted and observed to 3 decimals
  (0.0896 → 0.09, 0.0955 → 0.096), and the explain prints 4 (0.0896, 0.0955). It passes only when
  the 3-decimal string is a prefix of the 4-decimal one.
- **Durable log:** a short-run artefact. In 45 min the run's first decision is still in the in-memory
  journal (`from: null`), so there is no evicted id to answer from the log. Every captured id was on a
  digest-verified line.

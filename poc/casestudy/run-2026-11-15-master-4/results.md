# Master run 4, 2026-11-15: twins any time, cycler stopped before M7

This is the same scenario as runs 1–3 (plan 07). It is one run, so the numbers below are single readings, not means.

- **Build 2d8b5b0:** a local, never-pushed integration of `fix/sm-master-findings` at **9d4f377** with the four `fix/sm-defects` review fixes at **bfff56f**, cherry-picked with the same trivial resolutions as in run 3. `go build`, `go vet` and `go test -race -count=1 ./...` pass. Since run 3 it adds the Due fix (36c76d6), the twins criterion that passes at any time, and `cycler` deleted at the start of M7.
- **Run:** 2026-11-15 21:16:36 to 2026-11-16 00:38:09 VM time, `teardown: end`. No kill criterion fired. M7's calm wait took 0 min, because relief never acted (see M6).
- **Readout:** `readout.sh` at ea5b3a8. Runs 2 and 3 were re-read with it, and their `results-readout.md` files are refreshed. Two of their rows changed verdict on re-reading: twins (any time) and the sign row (reported).

**Verdicts: 36 pass, 4 fail, 7 not exercised, 5 reported, 1 not built.** With the current readout, run 2 reads 39 / 3 / 5 / 5 / 1 and run 3 reads 41 / 2 / 4 / 5 / 1. Run 1 is as committed: 31 / 9 / 7 / 5 / 1.

## Every row, runs 1–4 (← marks a row that changed)

| Phase | Plan | Criterion | Run 1 | Run 2 | Run 3 | Run 4 |
| --- | --- | --- | --- | --- | --- | --- |
| M0 | 02 | cold start journalled: first non-fact event property.admitted/declared, no restore.failed (inc 1) | pass | pass | pass | pass |
| M0 | 02 | incarnation 1 ids match -1.N; seq monotonic across captures and in the log | pass | pass | pass | pass |
| M0 | 01 | no review decision names a model (answer.by in rules, intervention) | pass | pass | pass | pass |
| M0 | 05 | pace base at start (GET /state .pace) | pass | pass | pass | pass |
| M0 | 04 | self observed: cpu_utilization@unit:di-agent admitted within 1 min | pass | pass | pass | pass |
| M0 | none | healthz reports last tick per loop and last save | not built | not built | not built | not built |
| M1 | 01 | composition cpu_utilization@pod:* -> cpu_utilization confirmed by rules within 2 review cycles | pass | pass | pass | pass |
| M1 | 01 | twins associated by persistent-correlation | pass | pass | pass | pass |
| M1 | 01 | nothing waits on a model (no model configured; autonomy errors and waiting 0) | pass | pass | pass | pass |
| M1 | 01 | out-of-order and future readings counted (census fields present) | pass | pass | pass | pass |
| M2 | 02 | estimate traceable: kind estimate, no cites, inputs.pace | pass | pass | pass | pass |
| M2 | 06 | interval: sensitivity_low <= sensitivity <= sensitivity_high | pass | pass | pass | pass |
| M2 | 02 | departure: mem-wave stale ~+2 min, retired ~+10 min, its edges retired by cascade | pass | pass | pass | pass |
| M2 | 03 | regime caveat on the next estimate after a relationship.regime | reported | reported | reported | reported |
| M3 | 04 | exactly one collector.down and one collector.up for netdata in 65-80 | fail | pass | pass | pass ← |
| M3 | 04 | Netdata properties unobservable 68-78, none retired; cgroup node properties stay active | fail | pass | pass | pass ← |
| M3 | 04 | relationships with a Netdata end stay active through the outage | not exercised | pass | pass | pass ← |
| M3 | 04 | admission at 72 with Netdata down (decline naming an unobservable reading, or accept when no domain reads Netdata) | reported | reported | reported | reported |
| M3 | 04 | Netdata properties active again within 2 ticks (10 s) of netdata start | fail | pass | pass | pass ← |
| M4 | 01 | two clock.stepped, step (wall_delta - monotonic_delta) ~ +7200 then ~ -7200 | pass | pass | pass | pass |
| M4 | 01 | nothing retires across the steps (80-86) | pass | pass | pass | pass |
| M4 | 01 | noise deleted at 86 goes stale ~2 min later (monotonic sweep) | pass | pass | pass | pass |
| M4 | 01 | relief freshness unaffected: no relief.phase change across the steps | pass | pass | pass | pass |
| M5 | 02 | at most one save interval lost at kill -9 | pass | pass | pass | pass |
| M5 | 02 | incarnation advances 2, 3, 4 and decision ids never collide | pass | pass | pass | pass |
| M5 | 01 | grace: nothing retires in the first retire_after (10 min) after each restart | pass | pass | pass | pass |
| M5 | 02 | learned state survives kill -9 (relief counters, calibration, peers, admission errors) | pass | pass | pass | pass |
| M5 | 02 | pending accept at the kill closes confounded "agent restarted" | not exercised | not exercised | not exercised | not exercised |
| M5 | 04 | restart at 94 restores state.json.prev (agent log), no restore.failed in inc 3 | pass | pass | pass | pass |
| M5 | 04 | cold start at 97 journalled: first event restore.failed with both errors (GET /state/journal); ledger reconciled | pass | pass | pass | pass |
| M5 | 06 | the cold start's restore.failed reaches the durable log | fail | pass | pass | pass ← |
| M5 | 02 | a cap in force at the kill is resumed, not duplicated | not exercised | not exercised | not exercised | not exercised |
| M6 | 02 | relief acts and verifies: >= 1 act, each with a later verify citing it | pass | pass | pass | fail ← |
| M6 | 02 | relief.phase calm->relieving before the first act; relieving->releasing after | pass | pass | pass | fail ← |
| M6 | 02 | chain resolves (?chain=1) | — | — | — | not exercised ← |
| M6 | 03 | calibration fed: relief.memory.high entry n >= 1, hit_rate in [0,1]; verify carries inputs.calibration | pass | pass | pass | fail ← |
| M6 | 03 | intervention learning: page-hog memory -> memory_contention_ratio provenance intervened | not exercised | not exercised | not exercised | not exercised |
| M6 | 06 | narrative (explain) | — | — | — | not exercised ← |
| M6 | 06 | durable log: every captured decision id on a digest-verified line; an evicted id answers from: log | pass | pass | pass | pass |
| M6 | 01 | no operator: zero relief.paused, zero relationship.asserted by operator | pass | pass | pass | pass |
| M7 | 02 | each accept has an outcome citing it, admission.arrived between | not exercised | pass | pass | pass ← |
| M7 | 03 | both bases recorded: usage_rise always, map_rise when a measured effect exists | not exercised | not exercised | not exercised | not exercised |
| M7 | 03 | calibration per domain: admission.memory/cpu_contention_ratio entries, n = scored outcomes | not exercised | pass | pass | pass ← |
| M7 | 03 | 6th accept: basis_chosen_by calibration once both bases have n >= 5, else default | reported | reported | reported | reported |
| M7 | 02 | declines traceable: kind admission, reason named, gate cited | fail | not exercised | pass | not exercised ← |
| M8 | 05 | one base->slow at 175 + 10 min (+- 1 tick), reason names calm | fail | fail | fail | fail |
| M8 | 05 | loops slowed: collection rate under slow ~1/4 of base (cgroup samples per second) | fail | pass | pass | pass ← |
| M8 | 05 | an estimate at 188 records inputs.pace slow | fail | fail | pass | pass ← |
| M8 | 05 | wake on arrival: slow->base within a tick of noise admitted, reason property admitted | fail | fail | fail | pass ← |
| M8 | 05 | staleness does not follow the pace: deleted pods stale ~+2 min, retired ~+10 min | pass | pass | pass | pass |
| M9 | 03 | sign contradiction acted on (relationship.retired sign-contradicted) | reported | reported | reported | reported |
| M9 | 03 | regime events | reported | reported | reported | reported |
| M9 | 04 | compaction: no property.forgotten in 3 h; snapshot properties = active + stale + unobservable + retired | pass | pass | pass | pass |

## Every row that is not a pass, checked against its evidence

Categories: (a) agent defect, (b) harness, timing, workload or readout artefact, (c) a case this run cannot produce.

| Row | Verdict | Category | Evidence |
| --- | --- | --- | --- |
| M6 relief acts and verifies | fail | **(b) workload** | `memory_contention_ratio` peaked at 0.144, as in runs 2 and 3 (0.147, 0.151), but stayed above enter (0.08) only briefly. The 30 s captures read 0.07 at 23:05:36, then 0.122 and 0.144, then 0.051 at 23:07:04: between 33 and 88 s. The lever needs 1 min held. In run 3 it stayed above for about 2 min (21:08:01 → 21:09:33+), and the act came at 21:09:44. page-hog logged the same file growth both times, so how long the squeeze lasts is the VM's reclaim timing, not the agent. |
| M6 relief.phase changes | fail | (b) | Follows from the row above: no `relief.phase` event in the whole run. |
| M6 calibration fed | fail | (b) | Follows from the row above: no verify, so no `relief.memory.high` entry. |
| M6 chain, narrative | not exercised | (b) | Follows from the row above: no verify for M9 to read. |
| M6 intervention edge | not exercised | (b)/(c) | `interventions: 0`, since relief never acted. One act would not build the edge anyway; it needs two agreeing outcomes. |
| M8 slow at 175 + 10 min | fail | **(b)** | `base→slow "calm for 10m0s"` came 3.5 min after the M8 deletion. Calm had begun before it (the last admission came at the end of M7, and deleting pods is not an attention event). Same as run 3. |
| M5 pending accept / cap at the kill | not exercised | (c) | Nothing was pending at 87: `offer-m3` closed `not_arrived` (`admit-1.460`) before the kill. Relief acts only in M6, after the kill. |
| M7 map_rise | not exercised | (c) | No measured effect: there is no intervention edge, and every accept has `MapPred` null. |
| M7 declines cite their gate | not exercised | (c) | No decline in the run (one accept at 72, six accepts in M7). Run 3 passed it, and so does `TestReliefActiveDeclines`. |
| M7 6th accept `basis_chosen_by` | reported | (c) | `default`. The calibration n read at probe-6's accept was 4 (it reached 5 afterwards), and the map basis has no record at all, so `calibration` cannot be chosen. |
| M3 admission at 72 | reported | by design | Accepted: no domain reads Netdata. The criterion accepts either verdict. |
| M9 sign contradiction | reported | by design | 0 retirements; at most 3 relationships `sign_suspect`. The one at the end, `cpu.cpu.iowait@cpu:cpu2 → system.cpu.iowait` (45 / 125), had 170 pairs against the 180 (3 × 60) that retirement needs. With no relief cap there was no reclaim, and so none of the page-hog composition retirements of runs 1–3 (#81). |
| M2/M9 regime (35 events) | reported | by design | Reported only. |
| M0 healthz liveness | not built | (c) | No plan in 01–06 includes it. |

## The M8 wake fix (36c76d6), end to end

- `pace.changed slow→base` came at 00:27:45, 0.13 s after `property.admitted memory_utilization@pod:4959e592…` (noise), with the reason `property admitted: …`. The wake row **passes**.
- The estimate at 188 records `pace: slow` (`est-4.966`), so that row passes too.
- Both rows failed in runs 1–3. In run 3 the pace had already gone back to base for "review due" on a candidate the same cycle had settled. Run 4 has no "review due" wake at all.

## M7 outcomes and calibration

- With `cycler` gone, the six M7 accepts give **5 held, 1 confounded**. Runs 2 and 3 gave 2 held, 4 confounded.
- probe-1 was confounded by the two pods deleted a minute earlier: `pod:5d646d21…` is page-hog and `pod:db2e1707…` is cycler. Because relief had never acted, there was nothing to calm, so the probe followed the deletions at once. `master.sh` (ea5b3a8) now waits 3 min after the deletions before the first probe. That change has not had a full run.
- Calibration at the end: `admission.cpu_contention_ratio/usage` n=5, mean_abs_err 0.0195, hit_rate 1. `admission.memory_contention_ratio/usage` n=5, hit_rate 1. Runs 2 and 3 reached n=2.

## Passing rows read real evidence (spot checks)

- **M3:** `collector.down` at inc 1 seq 432 (22:21:57) and `collector.up` at seq 463 (22:34:39).
- **M4:** `readings_future` stayed **0** for the whole run.
- **M5:** `restore.failed` is at `inc 4 seq 1` of `journal.ndjson`.
- **M7:** probe-2 was accepted as `admit-4.444` (citing `admit-4.444-gate`), then `admission.arrived probe-2` (seq 460), then outcome `admit-4.478 held` citing it, which fed calibration in both domains.
- **M1:** the twins associated at minute 80, with the pace at base. It was slow 21:41–22:28, then went back to base on M3's `POST /tasks`.

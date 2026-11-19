# Master run readout

Build: 7f3d3e1; features: 01,02,03,04,05,06; config: agent-autonomous.yaml (+ ledger in the run directory).
Timetable as run (phase starts, from phases.jsonl): M0 12:56, M6 12:56, teardown 13:42.

Verdicts: fail 17, not built 1, not exercised 7, pass 16, reported 5, unverifiable 7

| Phase | Plan | Criterion | Verdict | Reading |
| --- | --- | --- | --- | --- |
| M0 | 02 | cold start journalled: first non-fact event property.admitted/declared, no restore.failed (inc 1) | **pass** | first non-fact event "property.declared"; restore.failed in inc 1: 0  |
| M0 | 02 | incarnation 1 ids match -1.N; seq monotonic across captures and in the log | **pass** | inc-1 ids not matching: []; capture seq decreases within an incarnation: 0; log seq non-increasing within an incarnation: 0  |
| M0 | 01 | no review decision names a model (answer.by in rules, intervention) | **pass** | reviews by: {"rules":26}; undecided: 0  |
| M0 | 05 | pace base at start (GET /state .pace) | **pass** | first captured pace: base  |
| M0 | 04 | self observed: cpu_utilization@unit:di-agent (or the unit-glob fallback unit:di-agent.service) admitted within 1 min | **pass** | {"target":"cpu_utilization@unit:di-agent","after_s":9.817090034484863}  |
| M0 | none | healthz reports last tick per loop and last save | **not built** | GET /healthz answers {"ok":true}: liveness detail is in none of plans 01-06  |
| M1 | 01 | composition cpu_utilization@pod:* -> cpu_utilization confirmed by rules within 2 review cycles | **unverifiable** | []  |
| M1 | 01 | twins associated by persistent-correlation (any time in the run) | **fail** | declared: null  |
| M1 | 01 | nothing waits on a model (no model configured; autonomy errors and waiting 0) | **fail** | GET /autonomy has no models_down field; adapted to errors/waiting: {"captures":0,"errors":null,"waiting":null}; config review backends: {"decision":"none","reasoning":"none"}  |
| M1 | 01 | out-of-order and future readings counted (census fields present) | **pass** | last census: {"readings_out_of_order":0,"readings_future":0}  |
| M2 | 02 | estimate traceable: kind estimate, no cites, inputs.pace | **fail** | : null  |
| M2 | 06 | interval: sensitivity_low <= sensitivity <= sensitivity_high | **unverifiable** | {"sensitivity_low":null,"sensitivity":null,"sensitivity_high":null}  |
| M2 | 02 | departure: mem-wave stale ~+2 min, retired ~+10 min, its edges retired by cascade | **fail** | {"stale_min":null,"retired_min":null,"cascade":{"sign-contradicted":3}}  |
| M2 | 03 | regime caveat on the next estimate after a relationship.regime | **reported** | relationship.regime events in the run: 2  |
| M3 | 04 | exactly one collector.down and one collector.up for netdata in 65-80 | **fail** | []  |
| M3 | 04 | Netdata properties unobservable 68-78, none retired; cgroup node properties stay active | **fail** | {   "netdata_props": 0,   "captures": 0,   "not_unobservable": null,   "retired": null,   "cgroup_not_active": null }  |
| M3 | 04 | relationships with a Netdata end stay active through the outage | **unverifiable** | {   "captures": 0,   "statuses": null }  |
| M3 | 04 | admission at 72 with Netdata down (decline naming an unobservable reading, or accept when no domain reads Netdata) | **reported** | {"accepted":false,"reason":null}  |
| M3 | 04 | Netdata properties active again within 2 ticks (10 s) of netdata start | **fail** | null;   |
| M4 | 01 | two clock.stepped, step (wall_delta - monotonic_delta) ~ +7200 then ~ -7200 | **fail** | step seconds (wall_delta_seconds less the sweep interval's monotonic_delta_seconds): []  |
| M4 | 01 | nothing retires across the steps (80-86) | **pass** | {"retired":0,"cascade":0}  |
| M4 | 01 | noise deleted at 86 goes stale ~2 min later (monotonic sweep) | **fail** | null (the kill -9 at 87 restarts the sweep)  |
| M4 | 01 | relief freshness unaffected: no relief.phase change across the steps | **pass** | []  |
| M5 | 02 | at most one save interval lost at kill -9 | **unverifiable** | ; journal dropped values seen: [0]  |
| M5 | 02 | incarnation advances 2, 3, 4 and decision ids never collide | **fail** | {"incs":[1],"bad":[],"dups":[]}  |
| M5 | 01 | grace: nothing retires in the first retire_after (10 min) after each restart | **pass** | [{"inc":2,"retired_in_first_10min":0},{"inc":3,"retired_in_first_10min":0},{"inc":4,"retired_in_first_10min":0}]  |
| M5 | 02 | learned state survives kill -9 | **unverifiable** | before/after-kill.json missing  |
| M5 | 02 | pending accept at the kill closes confounded "agent restarted" | **not exercised** | pending at kill: null; outcomes: []  |
| M5 | 04 | restart at 94 restores state.json.prev (agent log), no restore.failed in inc 3 | **fail** | log lines:   |
| M5 | 04 | cold start at 97 journalled: first event restore.failed with both errors (GET /state/journal); ledger reconciled | **unverifiable** | incarnation null: null; reconcile_error values after: []  |
| M5 | 06 | the cold start's restore.failed reaches the durable log | **unverifiable** | digest-verified restore.failed lines in journal.ndjson for incarnation null: 0; first logged line of it: {"seq":null,"kind":null}  |
| M5 | 02 | a cap in force at the kill is resumed, not duplicated | **not exercised** | caps in force at the kill: unread  |
| M6 | 02 | relief acts and verifies: >= 1 act, each with a later verify citing it | **pass** | [{"act":"relief-act-memory.high-1.335","verify":["relief-verify-memory.high-1.337"]}]  |
| M6 | 02 | relief.phase calm->relieving before the first act; relieving->releasing after | **pass** | ["calm->relieving","relieving->releasing"]  |
| M6 | 02 | chain resolves: ?chain=1 on the last verify, cites[0] is its act | **pass** | {"verify":"relief-verify-memory.high-1.337","cites0":"relief-act-memory.high-1.335","act_cites":null}  |
| M6 | 03 | calibration fed: relief.memory.high entry n >= 1, hit_rate in [0,1]; verify carries inputs.calibration | **pass** | final: [{"kind":"relief.memory.high","basis":"usage","n":1,"mean_err":0.0002912025268833329,"mean_abs_err":0.0002912025268833329,"hit_rate":1}]; last verify inputs.calibration: {"relief.memory.high/usage":{"n":1,"mean_err":0.0002912025268833329,"mean_abs_err":0.0002912025268833329,"hit_rate":1}}  |
| M6 | 03 | intervention learning: page-hog memory -> memory_contention_ratio provenance intervened | **not exercised** | {"interventions":1}; relationship: []; reviews of the pair: []  |
| M6 | 06 | narrative: explain is text/plain, names the subject and the record's predicted and observed numbers | **pass** | Content-Type: text/plain; charset=utf-8; verify relief-verify-memory.high-1.337 on pod:e2a1a1c3-90fa-48e0-b2a5-4282017dd037 (page-hog pod:e2a1a1c3-90fa-48e0-b2a5-4282017dd037); predicted/observed rounded: 0.084 0.084; relief-verify-memory.high-1.337 [relief.verify] at 2026-11-19T11:06:10Z: capped pod:e2a1a1c3-90fa-48e0-b2a5-4282017dd037 (memory.high to 1.35e+10 bytes) because memory_contention_ratio stayed above 0.08 for 1m0s (at 0.0843); predicted a drop of 0.0843 from the workload's share of usage (effect 1); o  |
| M6 | 06 | durable log: every captured decision id on a digest-verified line; an evicted id answers from: log | **fail** | {"captured":29,"missing_or_bad":[],"n_missing":0}; first decision read at M9: {"id":"review-1.278-1e9d63c0","from":null}; log lines failing digest: 0+0  |
| M6 | 01 | no operator: zero relief.paused, zero relationship.asserted by operator | **pass** | {"paused":0,"asserted_by_operator":0}  |
| M7 | 02 | each accept has an outcome citing it, admission.arrived between | **not exercised** | accepts: []; probe verdicts: []  |
| M7 | 03 | both bases recorded on scored outcomes: usage_rise always, map_rise when a measured effect exists | **not exercised** | [] (JSON names map_rise/usage_rise)  |
| M7 | 03 | calibration per domain: admission.memory/cpu_contention_ratio entries, n = scored outcomes | **not exercised** | entries: []; scored outcome lines by domain: null  |
| M7 | 03 | 6th accept: basis_chosen_by calibration once both bases have n >= 5, else default | **reported** | {"name":null,"basis_chosen_by":null,"n":null}  |
| M7 | 02 | declines traceable: kind admission, reason named, gate cited | **not exercised** | []  |
| M8 | 05 | one base->slow at 175 + 10 min (+- 1 tick), reason names calm | **fail** | []  |
| M8 | 05 | loops slowed: collection rate under slow ~1/4 of base (cgroup samples per second) | **fail** | null (adapted from last_success_unix spacing to the samples counter rate)  |
| M8 | 05 | an estimate at 188 records inputs.pace slow | **fail** | : null  |
| M8 | 05 | wake on arrival: slow->base within a tick of noise admitted, reason property admitted | **fail** | {"admitted_ts":null,"wake":null}  |
| M8 | 05 | staleness does not follow the pace: deleted pods stale ~+2 min, retired ~+10 min | **fail** | [{"s":"7be3fe86","stale_min":null,"retired_min":null},{"s":"e2a1a1c3","stale_min":null,"retired_min":null}]  |
| M9 | 03 | sign contradiction acted on (relationship.retired sign-contradicted) | **reported** | sign-contradicted retirements: 3; max relationships_sign_suspect: 3  |
| M9 | 03 | regime events | **reported** | {"n":2,"rels":["cpu.cpu.user@cpu:cpu2->system.cpu.user:discovered"]}  |
| M9 | 04 | compaction: no property.forgotten in 3 h; snapshot properties = active + stale + unobservable + retired | **pass** | forgotten: 0; snapshot properties: 265; final census: {"properties_total":265,"properties_active":265,"properties_stale":0,"properties_unobservable":0,"properties_retired":0}; snapshot bytes at M0 end ?, M9 end ?  |

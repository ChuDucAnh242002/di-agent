# Testing di-agent

For someone trying the Semantic Map for the first time and reporting what they find. You need nothing from the dissertation: no dataset, no cluster, no LLM.

If you are changing code, read [`DEVELOPING.md`](DEVELOPING.md). If you are deploying it on real nodes, read [`OPERATING.md`](OPERATING.md) first.

---

## Before you start

- **It is a research prototype.** It is stable enough to run unattended on a lab cluster, but it is not production software.
- **There is no authentication.** Anyone who can reach the port can read the whole model and change it. Bind to `127.0.0.1` (as every command below does) or to a private interface. See [`OPERATING.md`](OPERATING.md#read-this-first).
- **Requirements:** Go 1.22+, `curl` and a POSIX shell. The examples pipe through [`jq`](https://jqlang.github.io/jq/); without it, read the raw JSON. Tested on macOS (arm64), and on Linux (amd64) in CI.

---

## 1. Verify the checkout (2 minutes)

```bash
git clone https://github.com/DiyazY/di-agent && cd di-agent/semantic-map/go
go build ./... && go vet ./... && go test ./...
```

Every package should report `ok` or `[no test files]`. A failure here is worth reporting on its own: include `go version`, your OS and architecture, and the failing test's output.

## 2. The guided demo (3 minutes)

```bash
./dev.sh demo            # pauses between steps and opens the console at the end
./dev.sh demo --quick    # no pauses; add --no-ui to skip the browser
```

The demo builds and starts a daemon, prints the shipped domain model, then runs the scenario and evolution tests with their narration. The narration shows learning from cold start, a regime change, deprecation, propose-then-confirm, and a subject arriving, going stale, retiring and coming back.

Things to notice:

- Every relationship starts with `BASIS unknown` and `CONF 0.00`. The agent ships with no magnitudes. It reports that it does not know until it has seen paired observations.
- The domain model is small: two constructs, two propositions. That is deliberate. `domain_spec.json` keeps only the part of the Di-Select backbone that a running node can actually observe. The reasoning is in its `scope_rationale` field.

## 3. Drive a system with a known answer (10 minutes)

The scripted collector plays a scenario file as if it were a real machine, and the file states the true answer. That lets you check what the map says against ground truth. Five scenarios ship in `semantic-map/scenarios/`: `linear`, `saturation`, `decoupled`, `confounded`, `churn`.

The scenarios tick every 10 s, so discovery takes about 5–20 minutes. To see it in under a minute, make a 1-second copy of `linear`:

```bash
# from semantic-map/go
mkdir -p /tmp/di-agent
go build -o /tmp/di-agent/ ./cmd/agent ./cmd/mapctl
sed -e 's/"tick_seconds": 10/"tick_seconds": 1/' -e 's/"period": 600/"period": 60/' \
    ../scenarios/linear.json > /tmp/di-agent/linear-fast.json

cat > /tmp/di-agent/agent.yaml <<'SPEC'
node:
  addr: 127.0.0.1:8080
sources:
  interval: 1s
  collectors:
    - name: script
      type: script
      config: {path: /tmp/di-agent/linear-fast.json}
map:
  stale_after: 20s
  retire_after: 60s
SPEC
/tmp/di-agent/agent -config /tmp/di-agent/agent.yaml
```

Run the agent from `semantic-map/go`: it finds `domain_spec.json` by searching upward from the working directory, or you can set `domain.path`. Without one it still runs, with no domain model: properties, subjects, discovery and estimates work, while cost and tuning answer `501`.

In `linear`, one pod (`pod:a`) runs a sine-wave CPU load between 0.10 and 0.60. The node's `pressure` is exactly `0.05 + pod:a's cpu`. Leave the agent running and use a second terminal:

```bash
alias mapctl=/tmp/di-agent/mapctl     # talks to http://localhost:8080 by default

curl -s localhost:8080/state | jq .counts             # census: properties, subjects, relationships
curl -s 'localhost:8080/state?subject=pod:a' | jq '.properties[].id'
```

**Discovery.** After about 40 seconds the proposer will have found that the pod's CPU co-varies with node-level properties nobody declared a relationship for:

```bash
mapctl candidates list
mapctl candidates confirm 'cpu_utilization@pod:a->pressure'
```

**Counterfactuals.** Wait ~10 s after confirming, then ask the map what happens if the pod grows, or leaves:

```bash
mapctl estimate pressure --assume 'cpu_utilization@pod:a=0.55'
mapctl estimate pressure --without pod:a
```

The true answers are `0.60` and `0.05`. The scenario's own tolerance is ±0.08. On a reference run the map projected `0.579` and `0.064`. Each answer states its caveats (for example, that strength is a correlation magnitude, not a fitted slope) and ends with a decision id:

```bash
curl -s localhost:8080/state/decisions/<decision-id> | jq   # the exact inputs that answer read
```

**Lifecycle.** Push a metric for a workload the agent has never seen, then let it go silent. The `node_id` must be the agent's own identity, which `/state` reports as `owner`:

```bash
NODE=$(curl -s localhost:8080/state | jq -r .owner)
curl -s -XPOST -H 'Content-Type: application/json' localhost:8080/ingest-sample -d "{
  \"node_id\":\"$NODE\",\"metric_type\":\"queue_depth\",\"value\":12,
  \"unit\":\"items\",\"range\":[0,100],\"subject\":\"app:checkout\",
  \"timestamp_unix\":$(date +%s),\"event_id\":\"tester-1\"}"

curl -s 'localhost:8080/state?subject=app:checkout' | jq '.properties[] | {id, status}'   # active
# ~30 s later: stale.  ~75 s later: retired, and then visible only via
curl -s 'localhost:8080/state?status=retired' | jq '.properties[] | {id, status}'
```

The push returns `202` with `"routed": false`. Any `metric_type` is admitted as a property of its subject, so you do not need to edit Go to add one.

**The operator console.** Open <http://localhost:8080/ui/>. It loads nothing from a CDN. Watch the force graph while the pod's load cycles. The drawer tabs (Journal, Decisions, Discovery, Subjects, Backbone) show the same state as the commands above.

Try the other scenarios at their native tick: the script entry's `config.path: ../scenarios/<name>.json` with `sources.interval: 10s`. The `sed` above only rescales `linear`, because the others set noise periods and arrival times in seconds as well. Allow up to the `within_seconds` in each file's `expect` block (20–25 minutes). That block states what a correct agent does:

| Scenario | A correct agent… |
| -------- | ---------------- |
| `decoupled` | discovers `pod:a`, but proposes **nothing** from `pod:noise`, whose load is unrelated to the node |
| `confounded` | finds both real contributors, `pod:a`'s CPU and `pod:b`'s I/O wait |
| `saturation` | discovers the relationship, but its linear projection at high load is **wrong by at least 0.1**. The point is that the caveat says so. |
| `churn` | lets `pod:a` go stale and retire when it leaves, and proposes it again when it returns |

`confounded` and `saturation` are where the method's limits show, so they are the most useful to report on.

## 4. Optional: ask the map in words

`POST /explain` and the console's **Ask** tab need a local OpenAI-compatible LLM. Without one they return `501`, which is expected.

```bash
brew install ollama && brew services start ollama        # or run `ollama serve` in another terminal
ollama pull qwen2.5:7b-instruct                           # or use any OpenAI-compatible server
cat > /tmp/di-agent/agent.yaml <<'SPEC'
node:
  addr: 127.0.0.1:8080
sources:
  interval: 1s
  collectors:
    - name: script
      type: script
      config: {path: /tmp/di-agent/linear-fast.json}
explain:
  provider: openai-compatible
SPEC
/tmp/di-agent/agent -config /tmp/di-agent/agent.yaml
```

Answers are checked before they leave the daemon. If the model cites a value the map does not hold, the daemon returns `422` and lists the mismatches. A `422` is the validator doing its job, not a crash.

## 5. Optional: on a real Linux node

On Linux with cgroup v2, drop `sources.collectors` (the default list is one cgroup entry) and the agent walks `/sys/fs/cgroup`, modelling each pod (and any systemd unit you name in the cgroup entry's `config.units`) as a subject. For Netdata's node-level telemetry too, list a cgroup entry and then `{name: netdata, type: netdata, config: {url: http://localhost:19999, collapse: false}}`. `curl localhost:8080/collectors` shows what each collector is doing. [`OPERATING.md`](OPERATING.md) covers systemd, multi-node peers and what the agent touches on your system.

---

## Out of scope for testing

- **Replay of the dissertation dataset.** `./dev.sh replay …` needs the Netdata parquets, which are not part of this repository. Without them it exits with an error asking for `--data-dir`.
- **`cloud-full` profile.** It is planned and does not exist yet. Only `edge-minimal` ships.
- **Production hardening.** Authentication, TLS and signed peer identities are recorded as out of scope for this stage.
- **Cross-distribution comparisons.** The agent models the one machine it runs on. It is not a benchmark of Kubernetes distributions.

## Known rough edges

- `sources.interval` must equal the scenario's `tick_seconds`. The daemon refuses to start otherwise, and says so.

## Reporting

Open an issue with the **Tester report** template at <https://github.com/DiyazY/di-agent/issues/new/choose>. Include:

1. What you ran (commands and flags) and on what (`go version`, OS, architecture).
2. What you expected, and what happened. For a wrong answer, include the decision id and the output of `GET /state/decisions/<id>`.
3. The agent's log lines around the event.

The most useful reports are the ones where the map **says something it cannot support**: a confident answer with no observations behind it, a relationship discovered in `decoupled`, a property that stays `active` after its producer stopped, or a caveat that is missing. Crashes and confusing docs are welcome too.

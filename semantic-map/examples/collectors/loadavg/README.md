# loadavg — an exec collector plugin in Python

Node load and pressure for di-agent, written against the Python SDK
(`sdk/python/di_collector.py`) and run by the agent as an `exec` plugin.

| Metric type | Unit | Range | Source |
| --- | --- | --- | --- |
| `load1`, `load5`, `load15` | `load_per_cpu` | none | `/proc/loadavg`, divided by the CPU count |
| `psi_cpu_some_avg10`, `psi_memory_some_avg10`, `psi_io_some_avg10` | `share` | `[0, 1]` | `some avg10` of `/proc/pressure/{cpu,memory,io}`, divided by 100 |

All are node-level (no subject), timestamped with the agent's `now`. A kernel
without PSI has no `/proc/pressure`; the plugin then emits only the load types.
An unreadable `/proc/loadavg` is a fault: the tick is an error reply, which the
agent shows in `GET /collectors`, rather than an empty batch that would look
healthy. Without `/proc` (macOS) every tick is that error, so it does not certify.

## Use it

```yaml
# agent.yaml
sources:
  collectors:
    - name: loadavg
      exec: {command: /opt/di-agent/examples/collectors/loadavg/loadavg.py}
```

The plugin finds the SDK at `../../../sdk/python` relative to itself, so keep the
`semantic-map/` layout or copy `di_collector.py` beside it.

## Certify it

```bash
cd semantic-map/go
go run ./cmd/mapctl collector check --exec ../examples/collectors/loadavg/loadavg.py
```

CI runs exactly this on Linux. To try it by hand against a fake tree, set
`DI_PROC_ROOT` (default `/proc`) and pipe requests in:

```bash
printf '%s\n' '{"op":"hello","protocol":1,"name":"loadavg","node_id":"n1","interval_ms":10000,"config":{}}' \
              '{"op":"collect","seq":1,"now_unix":1761160000}' \
  | DI_PROC_ROOT=/tmp/fakeproc ./loadavg.py
```

The agent starts plugins with a minimal environment, so `DI_PROC_ROOT` does not
reach a plugin the agent runs; wrap the plugin in a script that sets it if needed.

#!/usr/bin/env python3
"""loadavg: node load and pressure, as a di-agent exec collector plugin.

Emits, node-level (no subject):

- load1, load5, load15 from /proc/loadavg, divided by the CPU count so a value of
  1.0 means one runnable task per CPU on average. Unit ``load_per_cpu``, no range:
  load has no ceiling.
- psi_cpu_some_avg10, psi_memory_some_avg10, psi_io_some_avg10 from the ``some``
  line of /proc/pressure/{cpu,memory,io}: the share of the last ten seconds in which
  at least one task waited on that resource. Unit ``share`` on [0, 1]. A kernel
  without PSI has no such files; the plugin then emits only the load types.

An unreadable /proc/loadavg is a fault (an error reply), not an empty batch.

DI_PROC_ROOT (default /proc) points the plugin at another tree, so it can be run
against a fake /proc by hand. The agent starts plugins with a minimal environment,
so under the agent it is always /proc.
"""

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", "sdk", "python"))

import di_collector  # noqa: E402

PROC = os.environ.get("DI_PROC_ROOT", "/proc")
CPUS = os.cpu_count() or 1


def read_load(now):
    # Every Linux has /proc/loadavg, so failing to read it is a fault, not "no
    # data": raising makes the tick an error reply the operator sees in
    # GET /collectors, where an empty list would look like a healthy plugin.
    with open(os.path.join(PROC, "loadavg")) as f:
        fields = f.read().split()
    if len(fields) < 3:
        raise ValueError("%s/loadavg has %d fields, want at least 3" % (PROC, len(fields)))
    return [
        {"metric_type": name, "value": float(v) / CPUS, "timestamp_unix": now,
         "unit": "load_per_cpu", "chart": "loadavg"}
        for name, v in zip(("load1", "load5", "load15"), fields[:3])
    ]


def read_pressure(resource, now):
    # A kernel without PSI has no /proc/pressure: that is no data, not a fault.
    try:
        with open(os.path.join(PROC, "pressure", resource)) as f:
            lines = f.read().splitlines()
    except OSError:
        return []
    for line in lines:
        kind, _, rest = line.partition(" ")
        if kind != "some":
            continue
        fields = dict(kv.split("=", 1) for kv in rest.split() if "=" in kv)
        if "avg10" not in fields:
            return []
        return [{"metric_type": "psi_%s_some_avg10" % resource, "value": float(fields["avg10"]) / 100.0,
                 "timestamp_unix": now, "unit": "share", "range": [0, 1], "chart": "psi"}]
    return []


def setup(hello):
    def collect(now):
        samples = read_load(now)
        for resource in ("cpu", "memory", "io"):
            samples += read_pressure(resource, now)
        return samples
    return collect


if __name__ == "__main__":
    di_collector.serve(setup)

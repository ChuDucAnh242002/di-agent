"""Write a di-agent collector plugin in Python.

The agent starts the plugin once and speaks wire protocol v1 to it: one JSON
request per line on stdin, one JSON reply per line on stdout. ``serve`` runs that
loop so a plugin only says what to measure::

    import di_collector

    def setup(hello):
        # hello: {"op", "protocol", "name", "node_id", "interval_s", "config"}
        def collect(seq, now):
            return [{"metric_type": "queue_depth", "value": 3.0,
                     "timestamp_unix": now, "unit": "items"}]
        return collect

    di_collector.serve(setup)

``collect(seq, now)`` returns a list of samples (``types.MetricSample`` JSON, as
``POST /ingest-sample`` takes) or a ``(samples, available)`` pair, where
``available`` lists every type the plugin can produce now; without it the agent
takes the types in ``samples``. An empty list is "no data right now". Raising in
``setup`` refuses the hello; raising in ``collect`` faults that tick, and the agent
restarts the plugin with backoff. Leave ``node_id``, ``source`` and ``event_id``
out: the agent stamps them. Log to stderr; stdout belongs to the protocol.

Standard library only, one file: copy it beside the plugin or put its directory
on ``sys.path``. Certify the plugin with ``mapctl collector check --exec PLUGIN``.
"""

import json
import sys

PROTOCOL_VERSION = 1

__all__ = ["PROTOCOL_VERSION", "serve"]


def _reply(out, msg):
    out.write(json.dumps(msg, separators=(",", ":")) + "\n")
    # The agent waits for each reply before sending anything else.
    out.flush()


def _answer(collect, req):
    seq = req.get("seq", 0)
    if collect is None:
        return {"seq": seq, "error": "collect before hello"}
    try:
        result = collect(seq, req.get("now_unix", 0))
    except Exception as e:  # a bad tick costs that tick, not the process
        return {"seq": seq, "error": str(e) or type(e).__name__}
    available = None
    if isinstance(result, tuple):
        samples, available = result
    else:
        samples = result
    reply = {"seq": seq, "samples": list(samples or [])}
    if available is not None:
        reply["available"] = list(available)
    return reply


def serve(setup, stdin=None, stdout=None):
    """Answer the agent until stdin closes, which is how the agent stops a plugin.

    ``setup(hello)`` is called on every hello and returns the collect function. If
    it raises, the refusal is sent and ``serve`` returns, since the agent will not
    use a plugin that refused. A line that is not JSON raises ``ValueError``.
    """
    stdin = stdin or sys.stdin
    stdout = stdout or sys.stdout
    collect = None
    for line in stdin:
        if not line.strip():
            continue
        req = json.loads(line)
        op = req.get("op")
        if op == "hello":
            try:
                collect = setup(req)
                if collect is None:
                    raise ValueError("setup returned no collect function")
            except Exception as e:
                _reply(stdout, {"error": str(e) or type(e).__name__})
                return
            _reply(stdout, {"protocol": PROTOCOL_VERSION})
        elif op == "collect":
            _reply(stdout, _answer(collect, req))
        else:
            # One reply per request keeps the agent's reads in step.
            _reply(stdout, {"seq": req.get("seq", 0), "error": "unknown op %r" % op})

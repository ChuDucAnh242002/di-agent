"""Verify Kafka telemetry reaches the designated agent and InfluxDB in kind."""

import csv
import io
import json
import subprocess
import sys
import time
import uuid


def kubectl(*args, input_text=None):
    return subprocess.run(
        ["kubectl", *args], input=input_text, text=True, capture_output=True,
        check=True, timeout=30,
    ).stdout


def agent_request(pod, path):
    return json.loads(kubectl(
        "exec", pod, "-c", "di-agent", "--",
        "curl", "-fsS", "--max-time", "10", f"http://localhost:9090{path}",
    ))


def main(replay=False):
    pods = json.loads(kubectl("get", "pods", "-l", "app=di-agent", "-o", "json"))["items"]
    agents = {pod["spec"]["nodeName"]: pod["metadata"]["name"] for pod in pods}
    designated = "di-agent-smoke-worker"
    if len(agents) != 3 or designated not in agents:
        raise AssertionError(f"expected three agents including {designated}: {agents}")

    run_id = "ci-replay" if replay else "ci-" + uuid.uuid4().hex
    subject = f"engine:{run_id}"
    metric = "ci_replay" if replay else "ci_speed"
    producer = """
import json, sys, time
from kafka import KafkaProducer
message = json.loads(sys.stdin.read())
message["timestamp"] = time.time()
message["timestamp_ns"] = time.time_ns()
producer = KafkaProducer(bootstrap_servers=["kafka:9092"],
                         value_serializer=lambda value: json.dumps(value).encode())
producer.send("dataset.telemetry", message).get(timeout=10)
producer.close(timeout=10)
"""
    message = json.dumps({
        "run_id": run_id, "source": "short", "signal": metric,
        "value": 42, "t_source": 1, "origin": "real",
        "metadata": {"signals": {metric: {"unit": "rpm", "range": [0, 100]}}},
    })
    flux = (
        'from(bucket: "telemetry") |> range(start: -10m) '
        f'|> filter(fn: (r) => r._measurement == "dataset_timeseries" '
        f'and r.run_id == "{run_id}" and r.signal == "{metric}" '
        'and r.origin == "real" and r.source == "short" and r._field == "value")'
    )
    deadline = time.monotonic() + 120
    last_error = "no observations yet"
    while time.monotonic() < deadline:
        if not replay:
            kubectl(
                "exec", "-i", "deploy/dataset-collector", "--", "python", "-c", producer,
                input_text=message,
            )
        try:
            state = agent_request(agents[designated], f"/state?subject={subject}")
            properties = state.get("properties") or []
            sample = next((p for p in properties if p["id"] == f"{metric}@{subject}"), None)
            output = kubectl(
                "exec", "influxdb-0", "--", "sh", "-c",
                'exec influx query --raw --host http://localhost:8086 '
                '--org di-agent --token "$DOCKER_INFLUXDB_INIT_ADMIN_TOKEN" "$1"',
                "sh", flux,
            )
            rows = list(csv.DictReader(line for line in io.StringIO(output) if not line.startswith("#")))
            points = [row for row in rows if row.get("_value") and float(row["_value"]) == 42]
            if sample and points:
                if (sample["subject"] != subject or sample["value"] != 42
                        or sample["unit"] != "rpm" or sample["range"] != [0, 100]
                        or not sample.get("range_declared")
                        or sample.get("labels", {}).get("origin") != "real"
                        or sample.get("labels", {}).get("dataset_source") != "short"):
                    raise AssertionError(f"unexpected agent property: {sample}")
                for node, pod in agents.items():
                    if node != designated:
                        other = agent_request(pod, f"/state?subject={subject}")
                        if other.get("properties"):
                            raise AssertionError(f"{node} incorrectly collected {subject}")
                origin = "PostgreSQL -> replay -> Kafka" if replay else "Kafka"
                print(f"{origin} -> collector -> {designated} and InfluxDB: {subject}")
                return
            last_error = f"agent property={sample}, InfluxDB matching points={len(points)}"
        except subprocess.CalledProcessError as exc:
            last_error = f"kubectl failed: {exc.stderr}"
        time.sleep(5)
    raise TimeoutError(f"telemetry did not reach both destinations: {last_error}")


if __name__ == "__main__":
    if sys.argv[1:] not in ([], ["--replay"]):
        sys.exit("usage: python3 e2e_telemetry.py [--replay]")
    main(replay=bool(sys.argv[1:]))

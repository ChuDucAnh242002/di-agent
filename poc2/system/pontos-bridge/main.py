"""Bridges telemetry from the PONTOS datahub (https://pontos.ri.se) MQTT
broker into the local Kafka bus, so it can be consumed like any other
telemetry source (see telemetry-writer, playground). Consumer/producer only,
no externally-exposed port — mirrors telemetry-cloud-sync's shape but in
the opposite direction (external MQTT -> local Kafka)."""
import base64
import json
import logging
import os
import time

from kafka import KafkaProducer
from paho.mqtt.client import Client

PONTOS_HOST = os.environ.get("PONTOS_HOST", "pontos.ri.se")
PONTOS_PORT = int(os.environ.get("PONTOS_PORT", "443"))
PONTOS_PATH = os.environ.get("PONTOS_PATH", "/mqtt")
PONTOS_USERNAME = os.environ.get("PONTOS_USERNAME", "__token__")
# The PONTOS HUB API token, used as the MQTT password. Mount from a Secret.
PONTOS_TOKEN = os.environ.get("PONTOS_TOKEN", "")
PONTOS_SUBSCRIBE_TOPIC = os.environ.get("PONTOS_SUBSCRIBE_TOPIC", "PONTOS_EGRESS/#")

KAFKA_BROKERS = os.environ.get("KAFKA_BROKERS", "localhost:9092").split(",")
KAFKA_TOPIC = os.environ.get("KAFKA_TOPIC", "pontos.telemetry")

RECONNECT_BACKOFF_S = float(os.environ.get("RECONNECT_BACKOFF_S", "5"))
LOG_EVERY_N_MESSAGES = int(os.environ.get("LOG_EVERY_N_MESSAGES", "50"))

logging.basicConfig(
    format="%(asctime)s %(levelname)s %(name)s %(message)s", level=logging.INFO
)
log = logging.getLogger("pontos-bridge")

RETURN_CODES = {
    0: "Connection successful",
    1: "Connection refused - incorrect protocol version",
    2: "Connection refused - invalid client identifier",
    3: "Connection refused - server unavailable",
    4: "Connection refused - bad username or password",
    5: "Connection refused - not authorized",
}


def _make_kafka_producer() -> KafkaProducer:
    while True:
        try:
            return KafkaProducer(
                bootstrap_servers=KAFKA_BROKERS,
                value_serializer=lambda v: json.dumps(v).encode("utf-8"),
                key_serializer=lambda k: k.encode("utf-8") if k else None,
            )
        except Exception as exc:  # noqa: BLE001 - local broker may not be up yet
            log.warning(
                "Kafka brokers %s not available yet (%s), retrying in %ss",
                KAFKA_BROKERS, exc, RECONNECT_BACKOFF_S,
            )
            time.sleep(RECONNECT_BACKOFF_S)


def _decode_payload(payload: bytes):
    try:
        return json.loads(payload.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        # Non-JSON/binary payloads are still forwarded, base64-encoded.
        return {"_base64": base64.b64encode(payload).decode("ascii")}


def main() -> None:
    if not PONTOS_TOKEN:
        raise RuntimeError("PONTOS_TOKEN is required (mount it from a Secret)")

    producer = _make_kafka_producer()
    stats = {"received": 0, "forwarded": 0, "failed": 0}

    client = Client(transport="websockets")
    client.enable_logger(logging.getLogger("paho"))
    client.ws_set_options(path=PONTOS_PATH)
    client.tls_set()
    client.username_pw_set(PONTOS_USERNAME, PONTOS_TOKEN)

    def on_connect(_client, _userdata, _flags, reason_code):
        if reason_code > 0:
            log.error(RETURN_CODES.get(reason_code, f"Connection failed ({reason_code})"))
            return
        log.info(RETURN_CODES.get(reason_code, "Connected"))
        _client.subscribe(PONTOS_SUBSCRIBE_TOPIC)
        log.info("Subscribed to %s", PONTOS_SUBSCRIBE_TOPIC)

    def on_message(_client, _userdata, message):
        stats["received"] += 1
        event = {
            "source_topic": message.topic,
            "received_at": time.time(),
            "payload": _decode_payload(message.payload),
        }
        try:
            producer.send(KAFKA_TOPIC, key=message.topic, value=event)
            stats["forwarded"] += 1
        except Exception as exc:  # noqa: BLE001 - local Kafka may be unreachable
            stats["failed"] += 1
            log.error("Failed to forward message from %s to Kafka: %s", message.topic, exc)
        if LOG_EVERY_N_MESSAGES and stats["received"] % LOG_EVERY_N_MESSAGES == 0:
            log.info("pontos-bridge stats: %s", stats)

    client.on_connect = on_connect
    client.on_message = on_message

    while True:
        try:
            client.connect(PONTOS_HOST, PONTOS_PORT)
            client.loop_forever()
        except Exception as exc:  # noqa: BLE001 - broker may be unreachable
            log.error(
                "Failed to connect to PONTOS MQTT broker %s:%s (%s); retrying in %ss",
                PONTOS_HOST, PONTOS_PORT, exc, RECONNECT_BACKOFF_S,
            )
            time.sleep(RECONNECT_BACKOFF_S)


if __name__ == "__main__":
    main()

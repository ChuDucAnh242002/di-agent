import json
import os
import time

from kafka import KafkaConsumer, KafkaProducer
from kafka.errors import KafkaError

KAFKA_BROKERS = os.environ.get("KAFKA_BROKERS", "localhost:9092").split(",")
KAFKA_TOPICS = [
    topic.strip()
    for topic in os.environ.get(
        "SYNC_KAFKA_TOPICS",
        "genset.telemetry,propulsion.telemetry,battery.telemetry,"
        "auxload.telemetry,shore_power.telemetry,switchboard.telemetry",
    ).split(",")
    if topic.strip()
]
KAFKA_GROUP_ID = os.environ.get("KAFKA_GROUP_ID", "telemetry-cloud-sync")

# Azure Event Hubs exposes a Kafka-compatible endpoint, so mirroring only
# needs a second Kafka producer pointed at it with SASL_SSL credentials -
# no Azure SDK required.
EVENTHUB_NAMESPACE_FQDN = os.environ.get("EVENTHUB_NAMESPACE_FQDN", "")
EVENTHUB_CONNECTION_STRING = os.environ.get("EVENTHUB_CONNECTION_STRING", "")
# Optional JSON mapping of local Kafka topic -> Event Hub name, e.g.
# {"genset.telemetry": "genset-telemetry"}. Falls back to the local topic
# name (an Event Hub with that exact name must exist in the namespace).
EVENTHUB_TOPIC_MAP = json.loads(os.environ.get("EVENTHUB_TOPIC_MAP", "{}"))

RETRY_BACKOFF_S = float(os.environ.get("RETRY_BACKOFF_S", "5"))
LOG_EVERY_N_MESSAGES = int(os.environ.get("LOG_EVERY_N_MESSAGES", "50"))


def _make_consumer() -> KafkaConsumer:
    while True:
        try:
            return KafkaConsumer(
                *KAFKA_TOPICS,
                bootstrap_servers=KAFKA_BROKERS,
                group_id=KAFKA_GROUP_ID,
                # Raw passthrough: mirrored bytes are re-published as-is,
                # so telemetry-writer's schema stays the single source of truth.
                value_deserializer=lambda v: v,
                key_deserializer=lambda k: k,
                enable_auto_commit=False,
                auto_offset_reset="earliest",
            )
        except Exception as exc:  # noqa: BLE001 - local broker may not be up yet
            print(f"Kafka brokers {KAFKA_BROKERS} not available yet ({exc}), retrying in 5s ...")
            time.sleep(5)


def _make_producer() -> KafkaProducer:
    if not EVENTHUB_NAMESPACE_FQDN or not EVENTHUB_CONNECTION_STRING:
        raise RuntimeError("EVENTHUB_NAMESPACE_FQDN and EVENTHUB_CONNECTION_STRING are required")
    while True:
        try:
            return KafkaProducer(
                bootstrap_servers=[f"{EVENTHUB_NAMESPACE_FQDN}:9093"],
                security_protocol="SASL_SSL",
                sasl_mechanism="PLAIN",
                sasl_plain_username="$ConnectionString",
                sasl_plain_password=EVENTHUB_CONNECTION_STRING,
                acks="all",
                retries=5,
                linger_ms=200,
            )
        except Exception as exc:  # noqa: BLE001 - cloud endpoint may be unreachable
            print(f"Event Hubs namespace {EVENTHUB_NAMESPACE_FQDN} not reachable yet ({exc}), retrying in 5s ...")
            time.sleep(5)


def main() -> None:
    consumer = _make_consumer()
    producer = _make_producer()
    stats = {"seen": 0, "synced": 0, "failed": 0}

    print(f"Mirroring {KAFKA_TOPICS} from {KAFKA_BROKERS} to Event Hubs namespace {EVENTHUB_NAMESPACE_FQDN}")

    try:
        for record in consumer:
            stats["seen"] += 1
            event_hub = EVENTHUB_TOPIC_MAP.get(record.topic, record.topic)
            # Retry indefinitely without advancing the Kafka offset, so a
            # disconnected Event Hubs endpoint just delays sync (bounded by
            # local Kafka retention) instead of dropping messages.
            while True:
                try:
                    future = producer.send(event_hub, key=record.key, value=record.value)
                    future.get(timeout=30)
                    stats["synced"] += 1
                    break
                except KafkaError as exc:
                    stats["failed"] += 1
                    print(
                        f"Failed to mirror message from {record.topic} to {event_hub}: {exc}; "
                        f"retrying in {RETRY_BACKOFF_S}s"
                    )
                    time.sleep(RETRY_BACKOFF_S)
            consumer.commit()
            if LOG_EVERY_N_MESSAGES and stats["seen"] % LOG_EVERY_N_MESSAGES == 0:
                print(
                    "telemetry-cloud-sync stats: "
                    f"seen={stats['seen']} synced={stats['synced']} failed={stats['failed']}"
                )
    finally:
        print(
            "telemetry-cloud-sync final stats: "
            f"seen={stats['seen']} synced={stats['synced']} failed={stats['failed']}"
        )
        producer.close()
        consumer.close()


if __name__ == "__main__":
    main()

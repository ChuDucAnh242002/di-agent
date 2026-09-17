import json
import os
import time

from kafka import KafkaConsumer, KafkaProducer

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

# "eventhub" (default) mirrors to Azure Event Hubs over its Kafka-compatible
# endpoint. "iothub" sends each topic's messages as device-to-cloud telemetry
# through a per-service Azure IoT Hub device identity. "kinesis" and
# "iotcore" use the AWS SDK credential provider chain.
SYNC_TARGET = os.environ.get("SYNC_TARGET", "eventhub").strip().lower()

# --- Event Hubs sink: a second Kafka producer pointed at the Kafka-compatible
# endpoint, no Azure SDK required. ---

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


def _make_eventhub_producer() -> KafkaProducer:
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


def _send_to_eventhub(producer: KafkaProducer, record) -> str:
    event_hub = EVENTHUB_TOPIC_MAP.get(record.topic, record.topic)
    future = producer.send(event_hub, key=record.key, value=record.value)
    future.get(timeout=30)
    return event_hub


# --- IoT Hub sink: one device identity per telemetry service, messages sent
# as device-to-cloud telemetry via the Azure IoT device SDK. ---

# JSON mapping of local Kafka topic -> name of the environment variable that
# holds that topic's device connection string, e.g.
# {"genset.telemetry": "IOTHUB_CONN_GENSET"}. Keeping the mapping indirect
# means the connection strings themselves never have to pass through JSON.
IOTHUB_TOPIC_ENV_MAP = json.loads(os.environ.get("IOTHUB_TOPIC_ENV_MAP", "{}"))


def _make_iothub_clients() -> dict:
    from azure.iot.device import IoTHubDeviceClient

    clients = {}
    for topic, env_name in IOTHUB_TOPIC_ENV_MAP.items():
        connection_string = os.environ.get(env_name, "")
        if not connection_string:
            print(f"No IoT Hub connection string in ${env_name} for topic {topic}; that topic will not be synced")
            continue
        client = IoTHubDeviceClient.create_from_connection_string(connection_string)
        client.connect()
        clients[topic] = client
    if not clients:
        raise RuntimeError("no IoT Hub device connection strings configured (IOTHUB_TOPIC_ENV_MAP)")
    return clients


def _send_to_iothub(clients: dict, record) -> str:
    from azure.iot.device import Message

    client = clients.get(record.topic)
    if client is None:
        raise RuntimeError(f"no IoT Hub device configured for topic {record.topic}")
    message = Message(record.value)
    message.content_type = "application/json"
    message.content_encoding = "utf-8"
    message.custom_properties["source_topic"] = record.topic
    client.send_message(message)
    return record.topic


# --- AWS sinks: credentials are resolved by boto3, allowing either Kubernetes
# workload identity or the standard AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY
# environment variables. ---

AWS_REGION = os.environ.get("AWS_REGION", "")
KINESIS_STREAM_MAP = json.loads(os.environ.get("KINESIS_STREAM_MAP", "{}"))
IOTCORE_ENDPOINT_URL = os.environ.get("IOTCORE_ENDPOINT_URL", "")
IOTCORE_TOPIC_MAP = json.loads(os.environ.get("IOTCORE_TOPIC_MAP", "{}"))
IOTCORE_QOS = int(os.environ.get("IOTCORE_QOS", "0"))


def _make_kinesis_client():
    if not AWS_REGION:
        raise RuntimeError("AWS_REGION is required for Kinesis")
    import boto3

    return boto3.client("kinesis", region_name=AWS_REGION)


def _send_to_kinesis(client, record) -> str:
    stream_name = KINESIS_STREAM_MAP.get(record.topic, record.topic)
    partition_key = record.key.decode("utf-8", errors="replace") if record.key else record.topic
    client.put_record(StreamName=stream_name, Data=record.value, PartitionKey=partition_key)
    return stream_name


def _make_iotcore_client():
    if not AWS_REGION or not IOTCORE_ENDPOINT_URL:
        raise RuntimeError("AWS_REGION and IOTCORE_ENDPOINT_URL are required for AWS IoT Core")
    import boto3

    return boto3.client("iot-data", region_name=AWS_REGION, endpoint_url=IOTCORE_ENDPOINT_URL)


def _send_to_iotcore(client, record) -> str:
    topic = IOTCORE_TOPIC_MAP.get(record.topic, record.topic)
    client.publish(topic=topic, qos=IOTCORE_QOS, payload=record.value)
    return topic


def main() -> None:
    consumer = _make_consumer()

    if SYNC_TARGET == "iothub":
        sinks = _make_iothub_clients()
        send = lambda record: _send_to_iothub(sinks, record)  # noqa: E731
        print(f"Mirroring {KAFKA_TOPICS} from {KAFKA_BROKERS} to Azure IoT Hub devices {list(sinks)}")
    elif SYNC_TARGET == "eventhub":
        sinks = _make_eventhub_producer()
        send = lambda record: _send_to_eventhub(sinks, record)  # noqa: E731
        print(f"Mirroring {KAFKA_TOPICS} from {KAFKA_BROKERS} to Event Hubs namespace {EVENTHUB_NAMESPACE_FQDN}")
    elif SYNC_TARGET == "kinesis":
        sinks = _make_kinesis_client()
        send = lambda record: _send_to_kinesis(sinks, record)  # noqa: E731
        print(f"Mirroring {KAFKA_TOPICS} from {KAFKA_BROKERS} to AWS Kinesis in {AWS_REGION}")
    elif SYNC_TARGET == "iotcore":
        sinks = _make_iotcore_client()
        send = lambda record: _send_to_iotcore(sinks, record)  # noqa: E731
        print(f"Mirroring {KAFKA_TOPICS} from {KAFKA_BROKERS} to AWS IoT Core at {IOTCORE_ENDPOINT_URL}")
    else:
        raise RuntimeError(
            f"Unknown SYNC_TARGET {SYNC_TARGET!r}; expected 'eventhub', 'iothub', 'kinesis', or 'iotcore'"
        )

    stats = {"seen": 0, "synced": 0, "failed": 0}

    try:
        for record in consumer:
            stats["seen"] += 1
            # Retry indefinitely without advancing the Kafka offset, so a
            # disconnected cloud endpoint just delays sync (bounded by local
            # Kafka retention) instead of dropping messages.
            while True:
                try:
                    send(record)
                    stats["synced"] += 1
                    break
                except Exception as exc:  # noqa: BLE001 - cloud endpoint may be unreachable/rejecting
                    stats["failed"] += 1
                    print(
                        f"Failed to mirror message from {record.topic} to {SYNC_TARGET}: {exc}; "
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
        if SYNC_TARGET == "iothub":
            for client in sinks.values():
                client.shutdown()
        elif SYNC_TARGET == "eventhub":
            sinks.close()
        consumer.close()


if __name__ == "__main__":
    main()

import json
import time
from types import SimpleNamespace
from unittest.mock import MagicMock, patch

import pytest
from kafka.errors import KafkaTimeoutError
from paho.mqtt.packettypes import PacketTypes
from pydantic import ValidationError

from conftest import load_component_modules

NOW = 1761200000.0


@pytest.fixture
def pontos():
    return load_component_modules("pontos-bridge", ["main"])["main"]


def payload(timestamp=NOW, value="12.5"):
    return json.dumps({"timestamp": timestamp, "value": value}).encode()


def test_maps_live_topic_with_units_identity_and_source_time(pontos):
    config = pontos.BridgeConfig(vessels=["imo_9007128"])
    message = pontos.map_message(
        config, "PONTOS_EGRESS/imo_9007128/enginemain_power_kw/1", payload(), NOW,
    )
    assert message["origin"] == "live"
    assert message["run_id"] == "pontos_imo_9007128"
    assert message["signal"] == "pontos.enginemain_power_kw.i_1"
    assert message["value"] == 12.5
    assert message["timestamp"] == message["t_source"] == NOW
    assert message["timestamp_ns"] == 1761200000000000000
    assert message["metadata"]["subject"] == "vessel:imo_9007128"
    assert message["metadata"]["signals"][message["signal"]] == {"unit": "kW"}
    assert message["metadata"]["license"] == "ODbL-1.0 / DbCL-1.0"
    assert config.subscriptions()[0][1] == 0
    assert all("/imo_9007128/" in topic for topic, _ in config.subscriptions())


def test_distinct_indices_and_escaped_vessels_cannot_collide(pontos):
    config = pontos.BridgeConfig()
    messages = [
        pontos.map_message(config, f"PONTOS_EGRESS/{vessel}/enginemain_power_kw/{index}", payload(), NOW)
        for vessel in ["name_My Ship", "name_hex_4d792053686970"]
        for index in ["1", "2", "shaft A", "x_73686166742041"]
    ]
    assert len({(m["run_id"], m["signal"]) for m in messages}) == len(messages)
    assert all(pontos.SUBJECT_RE.fullmatch(m["metadata"]["subject"]) for m in messages)
    assert all(pontos.SEGMENT_RE.fullmatch(m["signal"]) for m in messages)


@pytest.mark.parametrize("topic,body", [
    ("PONTOS_EGRESS/imo_9007128/unknown/1", payload()),
    ("PONTOS_EGRESS/imo_9007128/enginemain_power_kw", payload()),
    ("PONTOS_EGRESS/imo_9007128/enginemain_power_kw/+", payload()),
    ("PONTOS_EGRESS/imo_9007128/enginemain_power_kw/1", b"not JSON"),
    ("PONTOS_EGRESS/imo_9007128/enginemain_power_kw/1", b"[]"),
    ("PONTOS_EGRESS/imo_9007128/enginemain_power_kw/1", payload(value="NaN")),
    ("PONTOS_EGRESS/imo_9007128/enginemain_power_kw/1", payload(value=True)),
    ("PONTOS_EGRESS/imo_9007128/enginemain_power_kw/1", payload(timestamp=NOW * 1000)),
    ("PONTOS_EGRESS/imo_9007128/enginemain_power_kw/1", payload(timestamp=NOW - 301)),
])
def test_invalid_or_stale_measurement_is_logged_and_never_queued(pontos, caplog, topic, body):
    bridge = pontos.PontosBridge()
    assert not bridge.accept(topic, body, NOW)
    assert bridge.status()["dropped"] == 1
    assert bridge.status()["queued"] == 0
    assert "Dropping PONTOS measurement" in caplog.text


def test_cadence_uses_source_timestamps_and_duplicates_do_not_change_it(pontos):
    bridge = pontos.PontosBridge()
    topic = "PONTOS_EGRESS/imo_9007128/enginemain_power_kw/1"
    for offset in [0, 2, 7]:
        assert bridge.accept(topic, payload(timestamp=NOW + offset), NOW + 7)
    assert not bridge.accept(topic, payload(timestamp=NOW + 2), NOW + 7)
    state, = bridge.status()["series"].values()
    assert state["interval_count"] == 2
    assert state["mean_interval_seconds"] == 3.5
    assert state["min_interval_seconds"] == 2
    assert state["max_interval_seconds"] == 5
    assert bridge.status()["received"] == 3


@pytest.mark.parametrize("config", [
    {"queue_size": 1}, {"max_active_series": 1},
])
def test_queue_and_series_limits_drop_visibly_without_replacing_data(pontos, config, caplog):
    bridge = pontos.PontosBridge(pontos.BridgeConfig(**config))
    assert bridge.accept("PONTOS_EGRESS/imo_9007128/enginemain_power_kw/1", payload(), NOW)
    assert not bridge.accept("PONTOS_EGRESS/imo_9007128/enginemain_power_kw/2", payload(), NOW)
    assert bridge.status()["received"] == 1
    assert bridge.status()["dropped"] == 1
    assert bridge._queue.get_nowait()["signal"] == "pontos.enginemain_power_kw.i_1"
    assert "Dropping PONTOS measurement" in caplog.text


@pytest.mark.parametrize("config", [
    {"vessels": ["#"]}, {"signals": {}},
    {"signals": {"battery_soc_pct": {"unit": " ", "range": [0, 100]}}},
    {"signals": {"battery_soc_pct": {"unit": "percent", "range": [100, 0]}}},
])
def test_invalid_selection_or_declarations_refuse_configuration(pontos, config):
    with pytest.raises(ValidationError):
        pontos.BridgeConfig.model_validate(config)


def test_token_file_and_tls_websocket_connection_setup(pontos, tmp_path, monkeypatch):
    token_file = tmp_path / "token"
    token_file.write_text("\n")
    monkeypatch.setenv("PONTOS_TOKEN_FILE", str(token_file))
    bridge = pontos.PontosBridge()
    with pytest.raises(ValueError, match="non-empty access token"):
        bridge.start()
    token_file.write_text("fixture-token\n")
    client = MagicMock()
    client.loop_start.return_value = None
    with patch.object(pontos.mqtt, "Client", return_value=client) as create, patch.object(pontos.threading, "Thread"):
        bridge.start()
    assert create.call_args.kwargs["protocol"] == pontos.mqtt.MQTTv5
    assert create.call_args.kwargs["transport"] == "websockets"
    client.tls_set.assert_called_once_with()
    client.ws_set_options.assert_called_once_with(path="/mqtt")
    client.username_pw_set.assert_called_once_with("__token__", "fixture-token")
    client.connect_async.assert_called_once_with("pontos.ri.se", 443, keepalive=60)


def test_authentication_and_subscription_failure_are_not_ready(pontos, caplog):
    bridge = pontos.PontosBridge()
    client = MagicMock()
    bridge._on_connect(client, None, None, 135, None)
    assert not bridge.status()["mqtt_connected"]
    assert "access-token validity" in caplog.text
    client.subscribe.assert_not_called()
    client.subscribe.return_value = (pontos.mqtt.MQTT_ERR_SUCCESS, 1)
    bridge._on_connect(client, None, None, 0, None)
    assert bridge.status()["mqtt_connected"]
    assert not bridge.status()["subscribed"]
    bridge._on_subscribe(
        client, None, 1, [SimpleNamespace(value=135)] * len(bridge.config.subscriptions()), None,
    )
    assert bridge.status()["mqtt_error"] == "MQTT subscription refused"

def test_successful_v5_callbacks_mark_ready_and_disconnect_clears_it(pontos):
    bridge = pontos.PontosBridge()
    client = MagicMock()
    client.subscribe.return_value = (pontos.mqtt.MQTT_ERR_SUCCESS, 1)
    connected = pontos.mqtt.ReasonCodes(PacketTypes.CONNACK, identifier=0)
    bridge._on_connect(client, None, None, connected, None)
    granted = pontos.mqtt.ReasonCodes(PacketTypes.SUBACK, identifier=0)
    bridge._on_subscribe(client, None, 1, [granted] * len(bridge.config.subscriptions()), None)
    assert bridge.status()["mqtt_connected"]
    assert bridge.status()["subscribed"]
    bridge._on_disconnect(client, None, pontos.mqtt.MQTT_ERR_CONN_LOST)
    assert not bridge.status()["mqtt_connected"]
    assert not bridge.status()["subscribed"]


def test_kafka_delivery_retry_keeps_the_same_measurement(pontos, caplog):
    bridge = pontos.PontosBridge()
    now = time.time()
    assert bridge.accept("PONTOS_EGRESS/imo_9007128/enginemain_power_kw/1", payload(now), now)
    producer = MagicMock()
    attempts = 0

    def deliver(**kwargs):
        nonlocal attempts
        attempts += 1
        if attempts == 1:
            raise KafkaTimeoutError("fixture delivery failure")
        bridge._stop.set()

    producer.send.return_value.get.side_effect = deliver
    with patch.object(pontos, "KafkaProducer", return_value=producer), patch.object(bridge._stop, "wait", return_value=False):
        bridge._publish()
    assert producer.send.call_count == 2
    assert producer.send.call_args_list[0] == producer.send.call_args_list[1]
    assert bridge.status()["published"] == 1
    assert bridge.status()["dropped"] == 0
    assert "Kafka delivery failed" in caplog.text
    producer.close.assert_called_once_with(timeout=5)

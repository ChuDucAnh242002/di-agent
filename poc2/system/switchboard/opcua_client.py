"""OPC-UA client subscribing to genset/battery status, mirroring how a
real vessel's power management system (PMS) supervises engine/BMS
controllers over OPC-UA instead of (or alongside) Modbus TCP.

Unlike ModbusPollingClient, this uses OPC-UA subscriptions (a monitored
item per target) so status updates arrive as data-change notifications
instead of being polled on a fixed cycle, which is OPC-UA's actual
advantage over Modbus."""

import logging
import threading
import time

from asyncua.sync import Client

logger = logging.getLogger(__name__)

# Node browse names read from each source type's OPC-UA server (see
# genset/opcua_server.py, battery/opcua_server.py); the values are placed
# under the "Genset"/"Battery" object, itself under Objects.
_BROWSE_NAMES = {
    "genset": {
        "object": "Genset",
        "fields": {
            "current_load_ratio": "CurrentLoadRatio",
            "power_kw": "PowerKw",
            "co2_kg_per_s": "Co2KgPerS",
            "nox_kg_per_s": "NoxKgPerS",
        },
    },
    "battery": {
        "object": "Battery",
        "fields": {
            "current_load_ratio": "CurrentLoadRatio",
            "power_kw": "SupplyPowerKw",
        },
    },
}


def _parse_targets(raw: str) -> list[tuple[str, str, str]]:
    """Parses "genset-1@opc.tcp://genset-1:4840/genset/server/,..." into
    [(id, endpoint_url), ...]. Blank input yields no targets."""
    targets = []
    for entry in raw.split(","):
        entry = entry.strip()
        if not entry:
            continue
        target_id, _, endpoint = entry.partition("@")
        targets.append((target_id, endpoint))
    return targets


class _SubHandler:
    def __init__(self, on_change) -> None:
        self._on_change = on_change

    def datachange_notification(self, node, val, data) -> None:
        self._on_change(node, val)


class _Connection:
    """One OPC-UA client connection subscribed to a single target's
    read-only status variables."""

    def __init__(self, source_type: str, target_id: str, endpoint: str) -> None:
        self.source_type = source_type
        self.target_id = target_id
        self.endpoint = endpoint
        self._client: Client | None = None
        self._values: dict[str, float] = {}
        self._node_to_field: dict[object, str] = {}

    def connect(self) -> None:
        namespace = f"http://di-agent.io/{self.source_type}"
        self._client = Client(self.endpoint, timeout=4)
        self._client.connect()
        idx = self._client.get_namespace_index(namespace)
        obj = self._client.nodes.objects.get_child(
            [f"{idx}:{_BROWSE_NAMES[self.source_type]['object']}"]
        )
        nodes = []
        for field, browse_name in _BROWSE_NAMES[self.source_type]["fields"].items():
            node = obj.get_child([f"{idx}:{browse_name}"])
            self._node_to_field[node] = field
            self._values[field] = node.read_value()
            nodes.append(node)

        handler = _SubHandler(self._on_change)
        subscription = self._client.create_subscription(500, handler)
        subscription.subscribe_data_change(nodes)

    def disconnect(self) -> None:
        if self._client is not None:
            try:
                self._client.disconnect()
            except Exception:  # noqa: BLE001 - best-effort on shutdown
                pass

    def get_values(self) -> dict:
        return dict(self._values)

    def check_alive(self) -> None:
        """Reads the standard Server_ServerStatus_State node (ns=0;i=2259) as
        a lightweight liveness probe, since a dead subscription doesn't
        raise on its own."""
        self._client.get_node("i=2259").read_value()

    def _on_change(self, node, val) -> None:
        field = self._node_to_field.get(node)
        if field is not None:
            self._values[field] = val


class OpcuaSubscribingClient:
    """Connects to a fixed list of genset/battery OPC-UA servers and keeps
    the last data-change value per target, on a background thread that
    owns reconnection."""

    def __init__(
        self,
        genset_targets: str,
        battery_targets: str,
        reconnect_interval_s: float = 5.0,
    ) -> None:
        self._targets = [
            ("genset", target_id, endpoint)
            for target_id, endpoint in _parse_targets(genset_targets)
        ] + [
            ("battery", target_id, endpoint)
            for target_id, endpoint in _parse_targets(battery_targets)
        ]
        self._reconnect_interval_s = reconnect_interval_s
        self._lock = threading.Lock()
        self._status: dict[str, dict] = {}
        self._connections: dict[str, _Connection] = {}
        self._stop_event = threading.Event()
        self._thread: threading.Thread | None = None

    def start(self) -> None:
        if not self._targets:
            return
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()

    def stop(self) -> None:
        self._stop_event.set()
        if self._thread is not None:
            self._thread.join(timeout=self._reconnect_interval_s * 2)
        for connection in self._connections.values():
            connection.disconnect()

    def get_status(self) -> dict:
        with self._lock:
            return {target_id: dict(value) for target_id, value in self._status.items()}

    def _run(self) -> None:
        while not self._stop_event.is_set():
            for source_type, target_id, endpoint in self._targets:
                if target_id not in self._connections:
                    self._connect_one(source_type, target_id, endpoint)
            self._refresh_status()
            self._stop_event.wait(self._reconnect_interval_s)

    def _connect_one(self, source_type: str, target_id: str, endpoint: str) -> None:
        connection = _Connection(source_type, target_id, endpoint)
        try:
            connection.connect()
            self._connections[target_id] = connection
        except Exception as error:  # noqa: BLE001 - server may not be up yet
            logger.warning("OPC-UA connect to %s (%s) failed: %s", target_id, endpoint, error)

    def _refresh_status(self) -> None:
        with self._lock:
            for target_id, connection in list(self._connections.items()):
                try:
                    connection.check_alive()
                except Exception as error:  # noqa: BLE001 - server may be down
                    logger.warning("OPC-UA connection to %s lost: %s", target_id, error)
                    connection.disconnect()
                    del self._connections[target_id]
                    existing = self._status.get(target_id, {"source_type": connection.source_type})
                    existing["stale"] = True
                    self._status[target_id] = existing
                    continue
                self._status[target_id] = {
                    "source_type": connection.source_type,
                    "stale": False,
                    "fetched_at": time.time(),
                    **connection.get_values(),
                }

"""OPC-UA server mirroring the real-world link between a propulsion drive
controller and a vessel's power management system, alongside (not instead
of) the existing REST/Kafka/Modbus interfaces.

Address space: an object "Propulsion" under Objects, with:
  CurrentLoadRatio, AllocatedPowerKw, SpeedRpm  (read-only)
  TargetLoadRatio, AnomalyEnabled               (writable)
"""

import logging

from asyncua.sync import Server

logger = logging.getLogger(__name__)


class _SubHandler:
    """Forwards server-side data changes on writable nodes to a callback;
    an OPC-UA server can subscribe to its own address space to detect
    client writes, the same role pymodbus's _CallbackDataBlock plays."""

    def __init__(self, on_change) -> None:
        self._on_change = on_change

    def datachange_notification(self, node, val, data) -> None:
        self._on_change(node, val)


class PropulsionOpcuaServer:
    """Runs an OPC-UA server backed by the given PropulsionController, in
    addition to (not instead of) the existing REST/Kafka/Modbus interfaces."""

    def __init__(self, controller, endpoint: str = "opc.tcp://0.0.0.0:4840/propulsion/server/") -> None:
        self._controller = controller
        self._endpoint = endpoint
        self._server: Server | None = None
        self._nodes: dict[str, object] = {}

    def start(self) -> None:
        self._server = Server()
        self._server.set_endpoint(self._endpoint)
        self._server.set_server_name("Propulsion OPC-UA Server")
        idx = self._server.register_namespace("http://di-agent.io/propulsion")
        propulsion_obj = self._server.nodes.objects.add_object(idx, "Propulsion")

        self._nodes["current_load_ratio"] = propulsion_obj.add_variable(
            idx, "CurrentLoadRatio", 0.0
        )
        self._nodes["allocated_power_kw"] = propulsion_obj.add_variable(
            idx, "AllocatedPowerKw", 0.0
        )
        self._nodes["speed_rpm"] = propulsion_obj.add_variable(idx, "SpeedRpm", 0.0)

        target_load_ratio = propulsion_obj.add_variable(idx, "TargetLoadRatio", 0.0)
        target_load_ratio.set_writable()
        self._nodes["target_load_ratio"] = target_load_ratio

        anomaly_enabled = propulsion_obj.add_variable(idx, "AnomalyEnabled", False)
        anomaly_enabled.set_writable()
        self._nodes["anomaly_enabled"] = anomaly_enabled

        self._server.start()
        handler = _SubHandler(self._on_write)
        subscription = self._server.create_subscription(200, handler)
        subscription.subscribe_data_change([target_load_ratio, anomaly_enabled])

    def stop(self) -> None:
        if self._server is not None:
            self._server.stop()

    def sync_from_status(self, status: dict) -> None:
        """Pushes the controller's current state into the address space;
        called periodically from the controller's own telemetry loop."""
        self._nodes["current_load_ratio"].write_value(status["current_load_ratio"])
        self._nodes["allocated_power_kw"].write_value(status["allocated_power_kw"])
        self._nodes["speed_rpm"].write_value(status["speed_rpm"])
        self._nodes["target_load_ratio"].write_value(status["target_load_ratio"])
        self._nodes["anomaly_enabled"].write_value(status["anomaly_enabled"])

    def _on_write(self, node, val) -> None:
        try:
            if node == self._nodes["target_load_ratio"]:
                self._controller.set_target_load_ratio(max(0.0, min(1.0, float(val))))
            elif node == self._nodes["anomaly_enabled"]:
                self._controller.set_anomaly_enabled(bool(val))
        except (ValueError, TypeError) as error:
            logger.warning("Ignoring invalid OPC-UA write: %s", error)

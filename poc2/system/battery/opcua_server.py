"""OPC-UA server mirroring the real-world link between a battery
management/PCS controller and a vessel's power management system, alongside
(not instead of) the existing REST/Kafka/Modbus interfaces.

Address space: an object "Battery" under Objects, with:
  CurrentLoadRatio, CurrentChargePowerKw, Soc, SupplyPowerKw  (read-only)
  TargetLoadRatio, TargetChargePowerKw, AnomalyEnabled        (writable)
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


class BatteryOpcuaServer:
    """Runs an OPC-UA server backed by the given BatteryController, in
    addition to (not instead of) the existing REST/Kafka/Modbus interfaces."""

    def __init__(self, controller, endpoint: str = "opc.tcp://0.0.0.0:4840/battery/server/") -> None:
        self._controller = controller
        self._endpoint = endpoint
        self._server: Server | None = None
        self._nodes: dict[str, object] = {}

    def start(self) -> None:
        self._server = Server()
        self._server.set_endpoint(self._endpoint)
        self._server.set_server_name("Battery OPC-UA Server")
        idx = self._server.register_namespace("http://di-agent.io/battery")
        battery_obj = self._server.get_objects_node().add_object(idx, "Battery")

        self._nodes["current_load_ratio"] = battery_obj.add_variable(idx, "CurrentLoadRatio", 0.0)
        self._nodes["current_charge_power_kw"] = battery_obj.add_variable(
            idx, "CurrentChargePowerKw", 0.0
        )
        self._nodes["soc"] = battery_obj.add_variable(idx, "Soc", 0.0)
        self._nodes["supply_power_kw"] = battery_obj.add_variable(idx, "SupplyPowerKw", 0.0)

        target_load_ratio = battery_obj.add_variable(idx, "TargetLoadRatio", 0.0)
        target_load_ratio.set_writable()
        self._nodes["target_load_ratio"] = target_load_ratio

        target_charge_power_kw = battery_obj.add_variable(idx, "TargetChargePowerKw", 0.0)
        target_charge_power_kw.set_writable()
        self._nodes["target_charge_power_kw"] = target_charge_power_kw

        anomaly_enabled = battery_obj.add_variable(idx, "AnomalyEnabled", False)
        anomaly_enabled.set_writable()
        self._nodes["anomaly_enabled"] = anomaly_enabled

        self._server.start()
        handler = _SubHandler(self._on_write)
        subscription = self._server.create_subscription(200, handler)
        subscription.subscribe_data_change(
            [target_load_ratio, target_charge_power_kw, anomaly_enabled]
        )

    def stop(self) -> None:
        if self._server is not None:
            self._server.stop()

    def sync_from_status(self, status: dict) -> None:
        """Pushes the controller's current state into the address space;
        called periodically from the controller's own telemetry loop."""
        self._nodes["current_load_ratio"].write_value(status["current_load_ratio"])
        self._nodes["current_charge_power_kw"].write_value(status["current_charge_power_kw"])
        self._nodes["soc"].write_value(status["soc"])
        last_message = status.get("last_message") or {}
        self._nodes["supply_power_kw"].write_value(float(last_message.get("power_kw", 0.0)))
        self._nodes["target_load_ratio"].write_value(status["target_load_ratio"])
        self._nodes["target_charge_power_kw"].write_value(status["target_charge_power_kw"])
        self._nodes["anomaly_enabled"].write_value(status["anomaly_enabled"])

    def _on_write(self, node, val) -> None:
        try:
            if node == self._nodes["target_load_ratio"]:
                self._controller.set_target_load_ratio(max(0.0, min(1.0, float(val))))
            elif node == self._nodes["target_charge_power_kw"]:
                self._controller.set_target_charge_power_kw(max(0.0, float(val)))
            elif node == self._nodes["anomaly_enabled"]:
                self._controller.set_anomaly_enabled(bool(val))
        except (ValueError, TypeError) as error:
            logger.warning("Ignoring invalid OPC-UA write: %s", error)

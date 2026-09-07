import os

from feems.components_model.component_electric import Battery

# Model key used when neither the `model` argument nor the BATTERY_MODEL env
# var selects one. battery-1 keeps this model so the shore-power service
# (batteryUrl http://battery-1:8000) keeps charging the large battery.
DEFAULT_BATTERY_MODEL = "ayk-lfp"


def build_ayk_lfp_battery() -> Battery:
    return Battery(
        name="AYK LFP Battery",
        rated_capacity_kwh=10400,
        charging_rate_c=2,
        discharge_rate_c=3,
        switchboard_id=1,
        eff_charging=0.95,
        eff_discharging=0.97,
    )


def build_leclanche_mrs2_battery() -> Battery:
    return Battery(
        name="Leclanche MRS-2 Battery",
        rated_capacity_kwh=2200,
        charging_rate_c=3.0,
        discharge_rate_c=4.6,
        switchboard_id=1,
        eff_charging=0.92,
        eff_discharging=0.95,
    )


# Registry of the battery models a battery instance can simulate, selected
# per deployed instance via the BATTERY_MODEL env var (see
# helm/di-agent-system/templates/battery.yaml).
BATTERY_MODELS = {
    "ayk-lfp": build_ayk_lfp_battery,
    "leclanche-mrs2": build_leclanche_mrs2_battery,
}


def build_battery(model: str | None = None) -> Battery:
    """Builds one of the known battery models, selected by `model` or the
    BATTERY_MODEL env var (defaults to DEFAULT_BATTERY_MODEL)."""
    model = model or os.environ.get("BATTERY_MODEL", DEFAULT_BATTERY_MODEL)
    try:
        return BATTERY_MODELS[model]()
    except KeyError:
        raise ValueError(
            f"unknown battery model {model!r}; expected one of {sorted(BATTERY_MODELS)}"
        ) from None

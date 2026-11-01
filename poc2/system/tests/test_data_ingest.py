import csv
import importlib.util
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest


@pytest.fixture
def ingest(tmp_path, monkeypatch):
    source = Path(__file__).resolve().parents[2] / "data-ingest" / "ingest.py"
    spec = importlib.util.spec_from_file_location("engine_ingest", source)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    monkeypatch.setattr(module, "DATA_DIR", tmp_path)
    monkeypatch.setattr(module, "SPLIT_DIR", tmp_path)
    monkeypatch.delenv("DATA_URL", raising=False)
    monkeypatch.delenv("STREAMER_CONTROL_URL", raising=False)
    return module


def write_csv(path, rows):
    with path.open("w", newline="") as output:
        csv.writer(output).writerows(rows)


@pytest.mark.parametrize("run_id", [
    "1000rpm_150kW_0001", "1000rpm_150kW_0001_2",
    "925rpm_100kW_0001", "925rpm_100kW_0001_2",
])
def test_split_preserves_each_signals_own_time_and_run(ingest, run_id):
    source = ingest.DATA_DIR / f"{run_id}_timeseries_short.csv"
    write_csv(source, [
        ["Data1_IMEPn1", "Data1_PowerCyl4", "Data1_time_IMEPn1", "Data1_time_PowerCyl4"],
        ["6.357223", "32.25634", "0", "100"],
        ["7.262658", "31.6588", "7200", "7300"],
        ["7.481922", "", "14400", ""],
    ])
    original = source.read_bytes()
    ingest.split_all()
    for signal, expected in [
        ("IMEPn1", [("0", "6.357223"), ("7200", "7.262658"), ("14400", "7.481922")]),
        ("PowerCyl4", [("100", "32.25634"), ("7300", "31.6588")]),
    ]:
        split = ingest.DATA_DIR / f"{run_id}_timeseries_short_{signal}.csv"
        match = ingest.SPLIT_RE.fullmatch(split.name)
        assert match.groupdict() == {"run": run_id, "source": "short", "signal": signal}
        cursor = MagicMock()
        ingest.load_timeseries(cursor, match["run"], match["source"], split)
        copy = cursor.copy.return_value.__enter__.return_value
        assert [call.args[0] for call in copy.write_row.call_args_list] == [
            (run_id, "short", signal, timestamp, value) for timestamp, value in expected
        ]
    assert source.read_bytes() == original


def test_new_split_file_is_loaded_without_reloading_existing_series(ingest):
    run_id = "925rpm_100kW_0001"
    original = ingest.DATA_DIR / f"{run_id}_timeseries_short_Injection_ds_ASOI14.csv"
    added = ingest.DATA_DIR / f"{run_id}_timeseries_short_PowerCyl4.csv"
    write_csv(original, [
        ["Data1_Injection_ds_ASOI14", "Data1_time_Injection_ds_ASOI14"], ["-12", "0"],
    ])
    write_csv(added, [
        ["Data1_PowerCyl4", "Data1_time_PowerCyl4"], ["32.25634", "0"], ["13.68181", "2210400"],
    ])
    connection = MagicMock()
    connection.execute.return_value.fetchall.return_value = [
        (original.name, original.stat().st_size),
    ]
    with patch("psycopg.connect") as connect, patch.object(ingest, "load_timeseries") as load:
        connect.return_value.__enter__.return_value = connection
        ingest.main()
        load.assert_called_once_with(
            connection.cursor.return_value.__enter__.return_value, run_id, "short", added,
        )
        load.reset_mock()
        connection.execute.return_value.fetchall.return_value.append(
            (added.name, added.stat().st_size),
        )
        ingest.main()
        load.assert_not_called()


def test_successful_ingestion_refreshes_streamer_without_changing_config(ingest, monkeypatch):
    path = ingest.DATA_DIR / "925rpm_100kW_0001_timeseries_short_PowerCyl4.csv"
    write_csv(path, [["Data1_PowerCyl4", "Data1_time_PowerCyl4"], ["32.25634", "0"]])
    monkeypatch.setenv("STREAMER_CONTROL_URL", "http://replay-streamer:8000/config")
    connection = MagicMock()
    connection.execute.return_value.fetchall.return_value = []
    with patch("psycopg.connect") as connect, patch.object(ingest.urllib.request, "urlopen") as open_url:
        connect.return_value.__enter__.return_value = connection
        open_url.return_value.__enter__.return_value.read.return_value = b'{"mode":"synthetic"}'
        ingest.main()
    request = open_url.call_args.args[0]
    assert request.full_url == "http://replay-streamer:8000/config"
    assert request.method == "PUT"
    assert request.data == b"{}"
    assert open_url.call_args.kwargs["timeout"] == ingest.REQUEST_TIMEOUT_S
    assert connect.return_value.__exit__.called


def test_streamer_refresh_failure_is_not_reported_as_ingestion_success(ingest, monkeypatch):
    path = ingest.DATA_DIR / "925rpm_100kW_0001_timeseries_short_PowerCyl4.csv"
    write_csv(path, [["Data1_PowerCyl4", "Data1_time_PowerCyl4"], ["32.25634", "0"]])
    monkeypatch.setenv("STREAMER_CONTROL_URL", "http://replay-streamer:8000/config")
    connection = MagicMock()
    connection.execute.return_value.fetchall.return_value = []
    with patch("psycopg.connect") as connect, patch.object(ingest.urllib.request, "urlopen") as open_url:
        connect.return_value.__enter__.return_value = connection
        open_url.side_effect = OSError("streamer unavailable")
        with pytest.raises(OSError, match="streamer unavailable"):
            ingest.main()

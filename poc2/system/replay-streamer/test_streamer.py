import unittest
from unittest.mock import patch

from pydantic import ValidationError

from main import ConfigUpdate, ReplayService, SignalProfile, StreamConfig, synthetic_records


class SyntheticRecordsTests(unittest.TestCase):
    def setUp(self):
        self.config = StreamConfig(
            mode="synthetic",
            run_id="test-run",
            source="short",
            interval_ms=100,
            points_per_interval=2,
            loop=False,
            random_seed=7,
        )
        self.profiles = [
            SignalProfile("pressure", -2, 2, 0, 0.5, 2, 0, 1),
            SignalProfile("temperature", 10, 10, 10, 0, 1, 0, 0),
        ]

    def test_generates_the_profile_sample_count_with_metadata(self):
        metadata = {"rpm": "1000"}
        with patch("main.load_profiles", return_value=(self.profiles, metadata)):
            records = list(synthetic_records(self.config))

        self.assertEqual(len(records), 3)
        self.assertEqual(
            [record["signal"] for record in records],
            ["pressure", "temperature", "pressure"],
        )
        self.assertTrue(
            all(-2 <= record["value"] <= 2 for record in records if record["signal"] == "pressure")
        )
        source_times = [record["t_source"] for record in records]
        self.assertEqual(source_times, sorted(source_times))
        self.assertTrue(all(record["metadata"] == metadata for record in records))
        self.assertTrue(all(record["origin"] == "synthetic" for record in records))

    def test_generation_is_reproducible_from_seed(self):
        with patch("main.load_profiles", return_value=(self.profiles, {})):
            first = list(synthetic_records(self.config))
            second = list(synthetic_records(self.config))

        self.assertEqual([record["value"] for record in first], [record["value"] for record in second])


class StreamConfigurationTests(unittest.TestCase):
    def test_rejects_out_of_range_pacing_values(self):
        with self.assertRaises(ValidationError):
            ConfigUpdate(interval_ms=9)
        with self.assertRaises(ValidationError):
            ConfigUpdate(points_per_interval=10_001)

    def test_runtime_update_ignores_nulls_and_changes_mode(self):
        service = ReplayService()
        config = service.update(ConfigUpdate(mode="real", interval_ms=250, run_id=None))

        self.assertEqual(config.mode, "real")
        self.assertEqual(config.interval_ms, 250)
        self.assertEqual(config.run_id, "1000rpm_150kW_0001")


if __name__ == "__main__":
    unittest.main()

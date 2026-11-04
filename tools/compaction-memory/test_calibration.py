import argparse
import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import calibration


class CalibrationTest(unittest.TestCase):
    def test_units_and_logfmt(self):
        fields = calibration.fields('job=test msg="estimated compaction job memory" estimated_memory="1.5 GiB"')
        self.assertEqual(fields["msg"], "estimated compaction job memory")
        self.assertEqual(calibration.byte_value(fields["estimated_memory"]), 1.5 * 1024**3)

    def test_markdown_table(self):
        with contextlib.redirect_stdout(io.StringIO()) as stdout:
            calibration.markdown_table(["Level", "Jobs"], [["L0", 2]])
        self.assertEqual(stdout.getvalue(),
                         "| Level | Jobs |\n| --- | ---: |\n| L0 | 2 |\n\n")

    def test_statistics(self):
        self.assertEqual(calibration.quantile([1, 2, 3, 4], 0.5), 2.5)
        self.assertEqual(calibration.median_interval([1, 2]), "unbounded")
        self.assertEqual(calibration.median_interval(list(range(1, 92))), "36.00–56.00")

    def test_fetch_splits_and_deduplicates(self):
        def response(command, text):
            start = calibration.timestamp(command[command.index("--from") + 1])
            end = calibration.timestamp(command[command.index("--to") + 1])
            stamps = [v for v in (1, 2, 3) if start <= v <= end][:3]
            return json.dumps({"status": "success", "data": {"result": [{
                "stream": {"pod": "worker-0"},
                "values": [{"timestamp": str(v), "line": "example"} for v in stamps],
            }]}})

        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "logs.json"
            args = argparse.Namespace(start=0, end=4, limit=3, context=None,
                                      datasource="test", namespace="test", pods=".*", output=output)
            with patch("calibration.subprocess.check_output", side_effect=response), contextlib.redirect_stdout(io.StringIO()):
                calibration.fetch(args)
            self.assertEqual(len(json.loads(output.read_text())["logs"]), 3)

    def test_mean_heap_includes_zero_estimates(self):
        logs = []
        for i, (estimate, peak) in enumerate(((2, 5), (0, 9))):
            logs.extend([
                {"pod": "p", "timestamp": str(i * 2), "line": (
                    f'job=x-L0 msg="estimated compaction job memory" '
                    f'buffered_objects="0 B" persistent_index="0 B" '
                    f'dataset_working_set="0 B" estimated_memory="{estimate} MiB"')},
                {"pod": "p", "timestamp": str(i * 2 + 1), "line": (
                    f'job=x-L0 msg="compaction memory calibration" '
                    f'baseline_heap="1 MiB" sampled_peak_heap="{peak} MiB" '
                    f'peak_heap_increase="{peak - 1} MiB" allocated="10 MiB" '
                    f'post_gc_heap="2 MiB" gc_cycles=1 retained_heap_delta_bytes=1048576')},
            ])
        with tempfile.TemporaryDirectory() as directory:
            source, output = Path(directory) / "logs.json", Path(directory) / "jobs.csv"
            source.write_text(json.dumps({"logs": logs}))
            with contextlib.redirect_stdout(io.StringIO()) as stdout:
                calibration.summarize(argparse.Namespace(input=source, csv=output))
            self.assertIn("L0 | 2 | 1.00 | 7.00 | 6.00 | 2.00", stdout.getvalue())
            self.assertIn("L0 | 1 | 2.00x", stdout.getvalue())

    def test_join_attempts(self):
        estimate = ('job=x-L0 msg="estimated compaction job memory" '
                    'buffered_objects="1 MiB" persistent_index="0 B" '
                    'dataset_working_set="1 MiB" estimated_memory="2 MiB"')
        measured = ('job=x-L0 msg="compaction memory calibration" '
                    'baseline_heap="1 MiB" sampled_peak_heap="5 MiB" '
                    'peak_heap_increase="4 MiB" allocated="8 MiB" '
                    'post_gc_heap="2 MiB" gc_cycles=1 retained_heap_delta_bytes=1048576')
        logs = [{"pod": "p", "timestamp": str(i), "line": line}
                for i, line in enumerate([estimate, measured, measured, estimate, measured])]
        with tempfile.TemporaryDirectory() as directory:
            source, output = Path(directory) / "logs.json", Path(directory) / "jobs.csv"
            source.write_text(json.dumps({"logs": logs}))
            with contextlib.redirect_stdout(io.StringIO()) as stdout:
                calibration.summarize(argparse.Namespace(input=source, csv=output))
            self.assertIn("matched: 2; unmatched: 1", stdout.getvalue())
            self.assertIn("L0 | 2 | 1.00 | 5.00 | 4.00 | 2.00", stdout.getvalue())
            self.assertEqual(len(output.read_text().splitlines()), 3)


if __name__ == "__main__":
    unittest.main()

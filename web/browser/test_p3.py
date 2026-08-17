import importlib.util
import io
import os
from pathlib import Path
import tempfile
import unittest


HERE = Path(__file__).resolve().parent


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, HERE / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


prepare = load("prepare_p3_obs", "prepare-p3-obs.py")
runner = load("run_p3", "run-p3.py")


class PrepareP3Tests(unittest.TestCase):
    def test_profiles_use_output_resolution_and_browser_source_is_native_750x640(self):
        for width, height, expected_position in ((1920, 1080, (1130.0, 60.0)), (2560, 1440, (1770.0, 80.0))):
            with tempfile.TemporaryDirectory() as directory:
                recording = Path(directory)
                profile = prepare.profile("P3", recording, width, height)
                collection = prepare.collection("P3", True, width, height)
            self.assertIn(f"BaseCX={width}", profile)
            self.assertIn(f"OutputCY={height}", profile)
            browser = next(source for source in collection["sources"] if source["id"] == "browser_source")
            self.assertEqual((browser["settings"]["width"], browser["settings"]["height"]), (750, 640))
            scene = next(source for source in collection["sources"] if source["id"] == "scene")
            item = next(item for item in scene["settings"]["items"] if item["name"] == "Analytics Sidebar")
            self.assertEqual((item["pos"]["x"], item["pos"]["y"]), expected_position)
            self.assertEqual(item["scale"], {"x": 1.0, "y": 1.0})
            self.assertEqual(item["rot"], 0.0)
            self.assertEqual(item["bounds_type"], 0)
            self.assertEqual(item["bounds"], {"x": 0.0, "y": 0.0})
            self.assertEqual(
                (item["crop_left"], item["crop_top"], item["crop_right"], item["crop_bottom"]),
                (0, 0, 0, 0),
            )


class RunP3Tests(unittest.TestCase):
    def test_only_protocol_resolutions_are_accepted(self):
        self.assertEqual(runner.resolution("1080p"), {"label": "1080p", "width": 1920, "height": 1080})
        self.assertEqual(runner.resolution("1440p"), {"label": "1440p", "width": 2560, "height": 1440})
        with self.assertRaises(ValueError):
            runner.resolution("720p")

    def test_visibility_region_is_the_complete_native_source(self):
        expected = {"1080p": (1130, 60), "1440p": (1770, 80)}
        for label, position in expected.items():
            config = runner.resolution(label)
            region = runner.browser_source_rectangle(config)
            self.assertEqual((region["x"], region["y"]), position)
            self.assertEqual((region["width"], region["height"]), (750, 640))
            self.assertLessEqual(region["x"] + region["width"], config["width"])
            self.assertLessEqual(region["y"] + region["height"], config["height"])

    def test_p3_gates_include_absolute_tree_and_browser_growth(self):
        pss = {
            "total": {"incrementalMedianKiB": 200_000, "overlayMedianKiB": 900_000, "growthKiBPerMinute": 500, "totalRangeKiB": 30_000},
            "browser": {"growthKiBPerMinute": 1_025},
        }
        gates = runner.evaluate_gates(
            overlay_duration=4_200,
            pss=pss,
            browser_cpu_median=1.0,
            lag_delta=0.0,
            hidden_violations=0,
            recovery_violations=0,
            remote=[],
            crashed=False,
            required_duration=4_200,
        )
        self.assertTrue(gates["incrementalWholeTreePss"])
        self.assertTrue(gates["absoluteOverlayTreePss"])
        self.assertTrue(gates["wholeTreeGrowth"])
        self.assertFalse(gates["browserGrowth"])
        pss["browser"]["growthKiBPerMinute"] = 500
        pss["total"]["overlayMedianKiB"] = 1_048_577
        self.assertFalse(runner.evaluate_gates(
            overlay_duration=4_200, pss=pss, browser_cpu_median=1.0, lag_delta=0.0,
            hidden_violations=0, recovery_violations=0, remote=[], crashed=False,
            required_duration=4_200,
        )["absoluteOverlayTreePss"])

    def test_component_pss_summary_preserves_total_and_process_kinds(self):
        baseline = [
            {"elapsedSeconds": 0, "pssKiB": 100, "pssKiBByKind": {"obs": 100, "browser": 0}},
            {"elapsedSeconds": 60, "pssKiB": 120, "pssKiBByKind": {"obs": 120, "browser": 0}},
        ]
        overlay = [
            {"elapsedSeconds": 0, "pssKiB": 310, "pssKiBByKind": {"obs": 130, "browser": 180}},
            {"elapsedSeconds": 60, "pssKiB": 350, "pssKiBByKind": {"obs": 150, "browser": 200}},
        ]
        summary = runner.summarize_pss(baseline, overlay)
        self.assertEqual(summary["total"]["incrementalMedianKiB"], 220)
        self.assertEqual(summary["obs"]["incrementalMedianKiB"], 30)
        self.assertEqual(summary["browser"]["incrementalMedianKiB"], 190)
        self.assertEqual(summary["total"]["growthKiBPerMinute"], 40)

    def test_evidence_stem_is_bounded_and_resolution_specific(self):
        self.assertEqual(runner.evidence_stem("preflight", "1080p"), "p3-preflight-1080p")
        with self.assertRaises(ValueError):
            runner.evidence_stem("../escape", "1440p")

    def test_visibility_frame_requires_settled_start_and_safe_end_margin(self):
        self.assertTrue(runner.visibility_frame_is_settled(2.0, 3.0))
        self.assertFalse(runner.visibility_frame_is_settled(1.999, 8.0))
        self.assertFalse(runner.visibility_frame_is_settled(8.0, 2.999))
        self.assertFalse(runner.visibility_frame_is_settled(8.0, None))

    def test_progress_is_written_to_stderr_and_flushed(self):
        stream = io.StringIO()
        runner.report_progress("overlay-recording", 125.4, 3600, stream=stream)
        self.assertEqual(
            stream.getvalue(),
            f"P3 progress: {runner.RESOLUTION['label']} overlay-recording 125/3600 seconds\n",
        )


if __name__ == "__main__":
    unittest.main()

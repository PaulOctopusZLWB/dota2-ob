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
    def test_profiles_and_browser_sources_use_requested_resolution(self):
        with tempfile.TemporaryDirectory() as directory:
            recording = Path(directory)
            profile = prepare.profile("P3", recording, 1920, 1080)
            collection = prepare.collection("P3", True, 1920, 1080)
        self.assertIn("BaseCX=1920", profile)
        self.assertIn("OutputCY=1080", profile)
        browser = next(source for source in collection["sources"] if source["id"] == "browser_source")
        self.assertEqual((browser["settings"]["width"], browser["settings"]["height"]), (1920, 1080))


class RunP3Tests(unittest.TestCase):
    def test_only_protocol_resolutions_are_accepted(self):
        self.assertEqual(runner.resolution("1080p"), {"label": "1080p", "width": 1920, "height": 1080})
        self.assertEqual(runner.resolution("1440p"), {"label": "1440p", "width": 2560, "height": 1440})
        with self.assertRaises(ValueError):
            runner.resolution("720p")

    def test_visibility_crop_stays_inside_each_protocol_canvas(self):
        for label in ("1080p", "1440p"):
            config = runner.resolution(label)
            crop = runner.visibility_crop(config)
            self.assertLessEqual(crop["x"] + crop["width"], config["width"])
            self.assertLessEqual(crop["y"] + crop["height"], config["height"])
            self.assertEqual((crop["width"], crop["height"]), (750, 450))

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

    def test_progress_is_written_to_stderr_and_flushed(self):
        stream = io.StringIO()
        runner.report_progress("overlay-recording", 125.4, 3600, stream=stream)
        self.assertEqual(
            stream.getvalue(),
            f"P3 progress: {runner.RESOLUTION['label']} overlay-recording 125/3600 seconds\n",
        )


if __name__ == "__main__":
    unittest.main()

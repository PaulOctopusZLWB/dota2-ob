#!/usr/bin/env python3
"""Validate the durable, sanitized DOT-34 real-OBS evidence bundle."""

from __future__ import annotations

import hashlib
import json
import re
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
EVIDENCE = ROOT / "evidence"
MEASUREMENT = EVIDENCE / "obs-measurement.json"
MANIFEST = EVIDENCE / "obs-run-manifest.json"
TRANSCRIPT = EVIDENCE / "obs-run-transcript.log"
RUNBOOK = ROOT / "OBS_RUNBOOK.md"
PREPARE = ROOT / "prepare_obs_profile.py"


class ObsEvidenceTest(unittest.TestCase):
    def setUp(self) -> None:
        self.measurement = json.loads(MEASUREMENT.read_text(encoding="utf-8"))

    def test_manifest_pins_identity_and_checksums(self) -> None:
        manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
        self.assertEqual(
            manifest["flatpak"]["commit"],
            "a3a5cbd575a1ab74c239cd3fd6903377377a43ef65e936271f655d458dc520b4",
        )
        self.assertEqual(manifest["flatpak"]["obsVersion"], "32.2.1")
        for item in manifest["artifacts"]:
            path = ROOT / item["path"]
            self.assertTrue(path.is_file(), item["path"])
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            self.assertEqual(digest, item["sha256"], item["path"])

    def test_transcript_traces_every_reported_metric(self) -> None:
        transcript = TRANSCRIPT.read_text(encoding="utf-8")
        expected = {
            "hide_observed_ms": self.measurement["failureRecovery"]["connectionLossToHiddenObservedMs"],
            "recovery_observed_ms": self.measurement["failureRecovery"]["serverRecoveryToVisibleObservedMs"],
            "tree_pss_kib": self.measurement["steady1440p"]["obsAndBrowserPssKiB"],
            "gpu_framebuffer_mib": self.measurement["steady1440p"]["gpuFramebufferMiB"],
            "frames_output": self.measurement["recording1440p"]["framesOutput"],
            "drawn_frames": self.measurement["recording1440p"]["drawnFrames"],
            "attempted_frames": self.measurement["recording1440p"]["attemptedFrames"],
            "render_lagged_frames": self.measurement["recording1440p"]["renderLaggedFrames"],
            "encoding_skipped_frames": self.measurement["recording1440p"]["encodingSkippedFrames"],
            "encoding_attempted_frames": self.measurement["recording1440p"]["encodingAttemptedFrames"],
        }
        for key, value in expected.items():
            self.assertRegex(transcript, rf"(?m)^{re.escape(key)}={re.escape(str(value))}$")
        self.assertRegex(transcript, r"(?m)^cef_peer=127\.0\.0\.1:18838$")
        self.assertRegex(transcript, r"(?m)^namespace_default_route=none$")

    def test_runbook_contains_complete_isolated_workflow(self) -> None:
        runbook = RUNBOOK.read_text(encoding="utf-8")
        required = [
            "flatpak info --user",
            "unshare -Urn",
            "ip link set lo up",
            "XDG_CONFIG_HOME",
            "--profile",
            "--collection",
            "1920x1080",
            "2560x1440",
            "browser_source",
            "timing-probe",
            "smaps_rollup",
            "nvidia-smi",
            "ss -Hntp",
            "sha256sum",
            "cleanup",
        ]
        for needle in required:
            self.assertIn(needle, runbook)

    def test_bundle_is_sanitized(self) -> None:
        transcript = TRANSCRIPT.read_text(encoding="utf-8")
        manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
        runbook = RUNBOOK.read_text(encoding="utf-8")
        combined = "\n".join((transcript, json.dumps(manifest), runbook))
        forbidden = [
            "/home/paul-zhang",
            "steamid",
            "account_id",
            "obs-websocket-password",
            "Authorization:",
        ]
        for needle in forbidden:
            self.assertNotIn(needle, combined)

    def test_profile_generator_creates_isolated_native_canvases(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            subprocess.run(
                [
                    "python3",
                    str(PREPARE),
                    "--root",
                    directory,
                    "--recording-dir",
                    f"{directory}/recordings",
                ],
                check=True,
                capture_output=True,
                text=True,
            )
            root = Path(directory)
            for label, width, height in (("DOT34-1080p", 1920, 1080), ("DOT34-1440p", 2560, 1440)):
                profile = (root / "config/obs-studio/basic/profiles" / label / "basic.ini").read_text()
                scene = json.loads((root / "config/obs-studio/basic/scenes" / f"{label}.json").read_text())
                self.assertIn(f"BaseCX={width}", profile)
                self.assertIn(f"BaseCY={height}", profile)
                browser = next(source for source in scene["sources"] if source["id"] == "browser_source")
                self.assertEqual(browser["settings"]["width"], width)
                self.assertEqual(browser["settings"]["height"], height)
                self.assertEqual(browser["settings"]["url"], "http://127.0.0.1:18838/?fixture=item-timing-long")
                self.assertNotIn("DesktopAudioDevice1", scene)
                self.assertNotIn("AuxAudioDevice1", scene)


if __name__ == "__main__":
    unittest.main()

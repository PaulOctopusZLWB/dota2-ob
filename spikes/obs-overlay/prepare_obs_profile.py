#!/usr/bin/env python3
"""Create disposable OBS profiles/collections for the DOT-34 host measurement."""

from __future__ import annotations

import argparse
import json
from pathlib import Path


OVERLAY_URL = "http://127.0.0.1:18838/?fixture=item-timing-long"
PROFILES = (("DOT34-1080p", 1920, 1080), ("DOT34-1440p", 2560, 1440))


def profile_ini(name: str, width: int, height: int, recording_dir: Path) -> str:
    return f"""[General]
Name={name}

[Video]
BaseCX={width}
BaseCY={height}
OutputCX={width}
OutputCY={height}
FPSType=0
FPSCommon=60
ScaleType=bicubic

[Output]
Mode=Advanced
FilenameFormatting=DOT34-%CCYY-%MM-%DD-%hh-%mm-%ss
DelayEnable=false
Reconnect=false

[AdvOut]
RecType=Standard
RecFilePath={recording_dir}
RecFormat2=mkv
RecEncoder=obs_nvenc_h264_tex
RecTracks=1
RecRB=false
RecRescale=false
TrackIndex=1

[Audio]
SampleRate=48000
ChannelSetup=Stereo
MonitoringDeviceName=Default
MonitoringDeviceId=default
"""


def scene_collection(name: str, width: int, height: int) -> dict:
    suffix = "1080" if width == 1920 else "1440"
    scene_uuid = f"00000000-0000-4000-8000-00000000{suffix}"
    color_uuid = f"00000000-0000-4000-8001-00000000{suffix}"
    browser_uuid = f"00000000-0000-4000-8002-00000000{suffix}"
    return {
        "name": name,
        "sources": [
            {
                "prev_ver": 537001985,
                "name": "Known Color",
                "uuid": color_uuid,
                "id": "color_source_v3",
                "versioned_id": "color_source_v3",
                "settings": {"color": 4282668390, "width": width, "height": height},
                "mixers": 0,
                "sync": 0,
                "flags": 0,
                "volume": 1.0,
                "balance": 0.5,
                "enabled": True,
                "muted": False,
                "hotkeys": {},
                "deinterlace_mode": 0,
                "deinterlace_field_order": 0,
                "monitoring_type": 0,
                "private_settings": {},
            },
            {
                "prev_ver": 537001985,
                "name": "DOT34 Browser",
                "uuid": browser_uuid,
                "id": "browser_source",
                "versioned_id": "browser_source",
                "settings": {
                    "url": OVERLAY_URL,
                    "width": width,
                    "height": height,
                    "fps": 60,
                    "shutdown": False,
                    "restart_when_active": False,
                    "reroute_audio": False,
                    "css": "body { background-color: rgba(0, 0, 0, 0); margin: 0; overflow: hidden; }",
                },
                "mixers": 0,
                "sync": 0,
                "flags": 0,
                "volume": 1.0,
                "balance": 0.5,
                "enabled": True,
                "muted": False,
                "hotkeys": {"OBSBasic.RefreshBrowser": []},
                "deinterlace_mode": 0,
                "deinterlace_field_order": 0,
                "monitoring_type": 0,
                "private_settings": {},
            },
            {
                "prev_ver": 537001985,
                "name": "DOT34 Scene",
                "uuid": scene_uuid,
                "id": "scene",
                "versioned_id": "scene",
                "settings": {
                    "id_counter": 2,
                    "custom_size": False,
                    "items": [
                        {
                            "name": "Known Color",
                            "source_uuid": color_uuid,
                            "visible": True,
                            "locked": True,
                            "rot": 0.0,
                            "pos": {"x": 0.0, "y": 0.0},
                            "scale": {"x": 1.0, "y": 1.0},
                            "align": 5,
                            "bounds_type": 2,
                            "bounds_align": 0,
                            "bounds": {"x": float(width), "y": float(height)},
                            "crop_left": 0,
                            "crop_top": 0,
                            "crop_right": 0,
                            "crop_bottom": 0,
                            "id": 1,
                        },
                        {
                            "name": "DOT34 Browser",
                            "source_uuid": browser_uuid,
                            "visible": True,
                            "locked": True,
                            "rot": 0.0,
                            "pos": {"x": 0.0, "y": 0.0},
                            "scale": {"x": 1.0, "y": 1.0},
                            "align": 5,
                            "bounds_type": 0,
                            "bounds_align": 0,
                            "bounds": {"x": 0.0, "y": 0.0},
                            "crop_left": 0,
                            "crop_top": 0,
                            "crop_right": 0,
                            "crop_bottom": 0,
                            "id": 2,
                        },
                    ],
                },
                "mixers": 0,
                "sync": 0,
                "flags": 0,
                "volume": 1.0,
                "balance": 0.5,
                "enabled": True,
                "muted": False,
                "hotkeys": {"OBSBasic.SelectScene": []},
                "deinterlace_mode": 0,
                "deinterlace_field_order": 0,
                "monitoring_type": 0,
                "canvas_uuid": "6c69626f-6273-4c00-9d88-c5136d61696e",
                "private_settings": {},
            },
        ],
        "groups": [],
        "scene_order": [{"name": "DOT34 Scene"}],
        "current_scene": "DOT34 Scene",
        "current_program_scene": "DOT34 Scene",
        "canvases": [],
        "current_transition": "Cut",
        "transition_duration": 300,
        "transitions": [],
        "quick_transitions": [],
        "saved_projectors": [],
        "preview_locked": True,
        "scaling_enabled": False,
        "scaling_level": -11,
        "scaling_off_x": 0.0,
        "scaling_off_y": 0.0,
        "virtual-camera": {"type2": 3},
        "modules": {},
        "version": 2,
    }


def write_text(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True, type=Path)
    parser.add_argument("--recording-dir", required=True, type=Path)
    args = parser.parse_args()
    root = args.root.resolve()
    recording_dir = args.recording_dir.resolve()
    recording_dir.mkdir(parents=True, exist_ok=True)

    obs = root / "config/obs-studio"
    write_text(
        obs / "global.ini",
        "[General]\nMaxLogs=10\nInfoIncrement=-1\nProcessPriority=Normal\n"
        "EnableAutoUpdates=false\nBrowserHWAccel=true\nLastVersion=537001985\n\n"
        "[Video]\nRenderer=OpenGL\n",
    )
    write_text(
        obs / "user.ini",
        "[General]\nFirstRun=false\nConfirmOnExit=false\nHotkeyFocusType=NeverDisableHotkeys\n\n"
        "[BasicWindow]\nPreviewEnabled=true\nShowStatusBar=true\nDocksLocked=true\n",
    )
    for name, width, height in PROFILES:
        write_text(obs / "basic/profiles" / name / "basic.ini", profile_ini(name, width, height, recording_dir))
        write_text(
            obs / "basic/scenes" / f"{name}.json",
            json.dumps(scene_collection(name, width, height), indent=2, ensure_ascii=False) + "\n",
        )

    (root / "data").mkdir(parents=True, exist_ok=True)
    (root / "cache").mkdir(parents=True, exist_ok=True)
    print(root)


if __name__ == "__main__":
    main()

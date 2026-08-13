#!/usr/bin/env python3
"""Create isolated, audio-free DOT-24 P3 OBS profiles and collections."""

from __future__ import annotations

import argparse
import json
from pathlib import Path


WIDTH, HEIGHT = 2560, 1440
URL = "http://127.0.0.1:18838/overlay/"


def profile(name: str, recording_dir: Path) -> str:
    return f"""[General]
Name={name}

[Video]
BaseCX={WIDTH}
BaseCY={HEIGHT}
OutputCX={WIDTH}
OutputCY={HEIGHT}
FPSType=0
FPSCommon=60
ScaleType=bicubic

[Output]
Mode=Advanced
FilenameFormatting=DOT24-P3-%CCYY-%MM-%DD-%hh-%mm-%ss
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


def source(name: str, uuid: str, source_id: str, settings: dict) -> dict:
    return {
        "prev_ver": 537001985, "name": name, "uuid": uuid, "id": source_id,
        "versioned_id": source_id, "settings": settings, "mixers": 0, "sync": 0,
        "flags": 0, "volume": 1.0, "balance": 0.5, "enabled": True, "muted": False,
        "hotkeys": {}, "deinterlace_mode": 0, "deinterlace_field_order": 0,
        "monitoring_type": 0, "private_settings": {},
    }


def collection(name: str, browser: bool) -> dict:
    suffix = "0001" if browser else "0000"
    scene_uuid = f"10000000-0000-4000-8000-00000000{suffix}"
    color_uuid = f"10000000-0000-4000-8001-00000000{suffix}"
    browser_uuid = f"10000000-0000-4000-8002-00000000{suffix}"
    sources = [source("Known Color", color_uuid, "color_source_v3", {"color": 4282668390, "width": WIDTH, "height": HEIGHT})]
    items = [{
        "name": "Known Color", "source_uuid": color_uuid, "visible": True, "locked": True,
        "rot": 0.0, "pos": {"x": 0.0, "y": 0.0}, "scale": {"x": 1.0, "y": 1.0},
        "align": 5, "bounds_type": 2, "bounds_align": 0,
        "bounds": {"x": float(WIDTH), "y": float(HEIGHT)},
        "crop_left": 0, "crop_top": 0, "crop_right": 0, "crop_bottom": 0, "id": 1,
    }]
    if browser:
        sources.append(source("Analytics Sidebar", browser_uuid, "browser_source", {
            "url": URL, "width": WIDTH, "height": HEIGHT, "fps": 30, "shutdown": False,
            "restart_when_active": False, "reroute_audio": False,
        }))
        items.append({
            "name": "Analytics Sidebar", "source_uuid": browser_uuid, "visible": True, "locked": True,
            "rot": 0.0, "pos": {"x": 0.0, "y": 0.0}, "scale": {"x": 1.0, "y": 1.0},
            "align": 5, "bounds_type": 0, "bounds_align": 0, "bounds": {"x": 0.0, "y": 0.0},
            "crop_left": 0, "crop_top": 0, "crop_right": 0, "crop_bottom": 0, "id": 2,
        })
    sources.append(source("DOT24 P3 Scene", scene_uuid, "scene", {"id_counter": len(items), "custom_size": False, "items": items}))
    sources[-1]["canvas_uuid"] = "6c69626f-6273-4c00-9d88-c5136d61696e"
    return {
        "name": name, "sources": sources, "groups": [], "scene_order": [{"name": "DOT24 P3 Scene"}],
        "current_scene": "DOT24 P3 Scene", "current_program_scene": "DOT24 P3 Scene", "canvases": [],
        "current_transition": "Cut", "transition_duration": 300, "transitions": [], "quick_transitions": [],
        "saved_projectors": [], "preview_locked": True, "scaling_enabled": False, "scaling_level": -11,
        "scaling_off_x": 0.0, "scaling_off_y": 0.0, "virtual-camera": {"type2": 3}, "modules": {}, "version": 2,
    }


def write(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.write_text(content, encoding="utf-8")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True, type=Path)
    parser.add_argument("--recording-dir", required=True, type=Path)
    args = parser.parse_args()
    root, recording = args.root.resolve(), args.recording_dir.resolve()
    recording.mkdir(parents=True, exist_ok=True, mode=0o700)
    obs = root / "config/obs-studio"
    write(obs / "global.ini", "[General]\nMaxLogs=10\nInfoIncrement=-1\nProcessPriority=Normal\nEnableAutoUpdates=false\nBrowserHWAccel=false\nLastVersion=537001985\n\n[Video]\nRenderer=OpenGL\n")
    write(obs / "user.ini", "[General]\nFirstRun=false\nConfirmOnExit=false\n\n[BasicWindow]\nPreviewEnabled=true\nShowStatusBar=true\nDocksLocked=true\n")
    for name, browser in (("DOT24-P3-Empty", False), ("DOT24-P3-Overlay", True)):
        write(obs / "basic/profiles" / name / "basic.ini", profile(name, recording))
        write(obs / "basic/scenes" / f"{name}.json", json.dumps(collection(name, browser), ensure_ascii=False, indent=2) + "\n")
    (root / "data").mkdir(parents=True, exist_ok=True, mode=0o700)
    (root / "cache").mkdir(parents=True, exist_ok=True, mode=0o700)


if __name__ == "__main__":
    main()

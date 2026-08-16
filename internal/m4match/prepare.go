package m4match

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

const gsiConfig = `"Dota 2 Integration Configuration"
{
  "uri"       "http://127.0.0.1:43910/gsi"
  "timeout"   "5.0"
  "buffer"    "0.1"
  "throttle"  "0.1"
  "heartbeat" "5.0"
  "data"
  {
    "provider"  "0"
    "map"       "1"
    "player"    "1"
    "hero"      "1"
    "abilities" "1"
    "items"     "1"
    "buildings" "1"
    "league"    "1"
    "draft"     "1"
    "wearables" "0"
  }
}
`

func Prepare(root string, width, height int) ([]Artifact, error) {
	if (width != 1920 || height != 1080) && (width != 2560 || height != 1440) {
		return nil, fmt.Errorf("output must be 1920x1080 or 2560x1440")
	}
	for _, dir := range []string{"application", "cache", "config/dota", "config/obs-studio/basic/profiles/DOT65-P4", "config/obs-studio/basic/scenes", "data/sessions", "evidence/canonical", "evidence/logs", "recordings", "runtime", "tools"} {
		if err := rootMkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			return nil, err
		}
	}
	files := map[string][]byte{
		"config/dota/gamestate_integration_dota2_ob_m4.cfg":   []byte(gsiConfig),
		"config/obs-studio/global.ini":                        []byte("[General]\nMaxLogs=10\nEnableAutoUpdates=false\nBrowserHWAccel=false\n\n[Video]\nRenderer=OpenGL\n"),
		"config/obs-studio/user.ini":                          []byte("[General]\nFirstRun=false\nConfirmOnExit=true\n\n[BasicWindow]\nPreviewEnabled=true\nShowStatusBar=true\nDocksLocked=true\n"),
		"config/obs-studio/basic/profiles/DOT65-P4/basic.ini": []byte(obsProfile(width, height)),
	}
	collection, err := obsCollection(width, height)
	if err != nil {
		return nil, err
	}
	files["config/obs-studio/basic/scenes/DOT65-P4.json"] = collection
	artifacts := make([]Artifact, 0, len(files))
	for name, payload := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := writePrivate(path, payload); err != nil {
			return nil, err
		}
		hash, size, err := fileSHA(path)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, Artifact{Path: name, SHA256: hash, Bytes: size})
	}
	return artifacts, nil
}

func obsProfile(width, height int) string {
	return fmt.Sprintf(`[General]
Name=DOT65-P4

[Video]
BaseCX=%d
BaseCY=%d
OutputCX=%d
OutputCY=%d
FPSType=0
FPSCommon=60
ScaleType=bicubic

[Output]
Mode=Advanced
FilenameFormatting=DOT65-P4-%%CCYY-%%MM-%%DD-%%hh-%%mm-%%ss
DelayEnable=false
Reconnect=false

[AdvOut]
RecType=Standard
RecFilePath=recordings
RecFormat2=mkv
RecEncoder=obs_nvenc_h264_tex
RecTracks=1
RecRB=false
RecRescale=false
TrackIndex=1
`, width, height, width, height)
}

func obsCollection(width, height int) ([]byte, error) {
	x, y := 1130, 60
	if width == 2560 {
		x, y = 1770, 80
	}
	dota := map[string]any{"prev_ver": 537001985, "name": "Dota 2 Window", "uuid": "65000000-0000-4000-8001-000000000001", "id": "xcomposite_input", "versioned_id": "xcomposite_input", "settings": map[string]any{"capture_window": "Dota 2", "exclude_alpha": false, "show_cursor": false, "cut_top": 0, "cut_left": 0, "cut_right": 0, "cut_bot": 0}, "mixers": 0, "sync": 0, "flags": 0, "volume": 1, "balance": 0.5, "enabled": true, "muted": false, "hotkeys": map[string]any{}, "private_settings": map[string]any{}}
	browser := map[string]any{"prev_ver": 537001985, "name": "Analytics Sidebar", "uuid": "65000000-0000-4000-8002-000000000001", "id": "browser_source", "versioned_id": "browser_source", "settings": map[string]any{"url": DeliveryOrigin + "/overlay/", "width": 750, "height": 640, "fps": 30, "shutdown": false, "restart_when_active": false, "reroute_audio": false}, "mixers": 0, "sync": 0, "flags": 0, "volume": 1, "balance": 0.5, "enabled": true, "muted": false, "hotkeys": map[string]any{}, "private_settings": map[string]any{}}
	items := []any{
		map[string]any{"name": "Dota 2 Window", "source_uuid": dota["uuid"], "visible": true, "locked": true, "rot": 0.0, "pos": map[string]any{"x": 0.0, "y": 0.0}, "scale": map[string]any{"x": 1.0, "y": 1.0}, "align": 5, "bounds_type": 2, "bounds_align": 0, "bounds": map[string]any{"x": float64(width), "y": float64(height)}, "crop_left": 0, "crop_top": 0, "crop_right": 0, "crop_bottom": 0, "id": 1},
		map[string]any{"name": "Analytics Sidebar", "source_uuid": browser["uuid"], "visible": true, "locked": true, "rot": 0.0, "pos": map[string]any{"x": float64(x), "y": float64(y)}, "scale": map[string]any{"x": 1.0, "y": 1.0}, "align": 5, "bounds_type": 0, "bounds_align": 0, "bounds": map[string]any{"x": 0.0, "y": 0.0}, "crop_left": 0, "crop_top": 0, "crop_right": 0, "crop_bottom": 0, "id": 2},
	}
	scene := map[string]any{"prev_ver": 537001985, "name": "DOT65 P4 Scene", "uuid": "65000000-0000-4000-8003-000000000001", "id": "scene", "versioned_id": "scene", "settings": map[string]any{"id_counter": 2, "custom_size": false, "items": items}, "mixers": 0, "sync": 0, "flags": 0, "volume": 1, "balance": 0.5, "enabled": true, "muted": false, "hotkeys": map[string]any{}, "private_settings": map[string]any{}, "canvas_uuid": "6c69626f-6273-4c00-9d88-c5136d61696e"}
	value := map[string]any{"name": "DOT65-P4", "sources": []any{dota, browser, scene}, "groups": []any{}, "scene_order": []any{map[string]any{"name": "DOT65 P4 Scene"}}, "current_scene": "DOT65 P4 Scene", "current_program_scene": "DOT65 P4 Scene", "canvases": []any{}, "current_transition": "Cut", "transition_duration": 300, "transitions": []any{}, "quick_transitions": []any{}, "saved_projectors": []any{}, "preview_locked": true, "scaling_enabled": false, "scaling_level": -11, "scaling_off_x": 0.0, "scaling_off_y": 0.0, "virtual-camera": map[string]any{"type2": 3}, "modules": map[string]any{}, "version": 2}
	payload, err := json.MarshalIndent(value, "", "  ")
	return append(payload, '\n'), err
}

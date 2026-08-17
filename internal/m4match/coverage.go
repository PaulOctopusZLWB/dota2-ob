package m4match

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	FieldCoverageDeltaSchemaVersion = "field_coverage_delta.v1"
	CoverageNormalizationVersion    = "escaped_typed_path_value_free.v1"
)

type CoverageFrameV1 struct {
	Sequence        uint64          `json:"sequence"`
	RawRecordSHA256 string          `json:"raw_record_sha256"`
	RawRecord       json.RawMessage `json:"-"`
	IdentityRecord  []byte          `json:"-"`
}

type FieldPathProfileV1 struct {
	FrameCount            uint64   `json:"frame_count"`
	SeenCount             uint64   `json:"seen_count"`
	NullCount             uint64   `json:"null_count"`
	JSONTypes             []string `json:"json_types"`
	Presence              string   `json:"presence"`
	Nullable              bool     `json:"nullable"`
	DynamicCollisionCount uint64   `json:"dynamic_collision_count"`
	HasDynamicCollision   bool     `json:"has_dynamic_collision"`
}

type FieldCoveragePathDeltaV1 struct {
	Path           string             `json:"path"`
	Baseline       FieldPathProfileV1 `json:"baseline"`
	Rehearsal      FieldPathProfileV1 `json:"rehearsal"`
	Classification string             `json:"classification"`
}

type FieldCoverageDeltaV1 struct {
	SchemaVersion                 string                     `json:"schema_version"`
	CapturedScheduleSHA256        string                     `json:"captured_schedule_sha256"`
	BaselineRawSessionSHA256      string                     `json:"baseline_raw_session_sha256"`
	RehearsalRawSessionSHA256     string                     `json:"rehearsal_raw_session_sha256"`
	NormalizationAlgorithmVersion string                     `json:"normalization_algorithm_version"`
	EvidenceRootSHA256            string                     `json:"evidence_root_sha256"`
	BaselineSourceFrameCount      uint64                     `json:"baseline_source_frame_count"`
	RehearsalSourceFrameCount     uint64                     `json:"rehearsal_source_frame_count"`
	Paths                         []FieldCoveragePathDeltaV1 `json:"paths"`
}

type mutableFieldProfile struct {
	FieldPathProfileV1
	types  map[string]bool
	frames map[uint64]bool
}

var (
	fixedCoverageKey      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	decimalCoverageKey    = regexp.MustCompile(`^(?:0|[1-9][0-9]*)$`)
	uuidCoverageKey       = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	opaqueCoverageKey     = regexp.MustCompile(`^[0-9a-fA-F]{16,128}$`)
	sensitiveCoverageKeys = map[string]bool{"steamid": true, "steam_id": true, "accountid": true, "account_id": true, "personaname": true, "persona_name": true, "display_name": true, "chat": true}
)

func GenerateFieldCoverageDelta(baseline, rehearsal []CoverageFrameV1, baselineRawSHA, rehearsalRawSHA, evidenceRootSHA string) (FieldCoverageDeltaV1, error) {
	if len(baseline) > MaxRehearsalCoverageFrames || len(rehearsal) > MaxRehearsalCoverageFrames {
		return FieldCoverageDeltaV1{}, errors.New("coverage frame limit exceeded")
	}
	for name, value := range map[string]string{"baseline raw session": baselineRawSHA, "rehearsal raw session": rehearsalRawSHA, "evidence root": evidenceRootSHA} {
		if !validSHA256(value) {
			return FieldCoverageDeltaV1{}, fmt.Errorf("%s SHA-256 is invalid", name)
		}
	}
	baselineProfiles, err := profileCoverageFrames(baseline)
	if err != nil {
		return FieldCoverageDeltaV1{}, fmt.Errorf("baseline: %w", err)
	}
	rehearsalProfiles, err := profileCoverageFrames(rehearsal)
	if err != nil {
		return FieldCoverageDeltaV1{}, fmt.Errorf("rehearsal: %w", err)
	}
	if len(baseline) == 0 || len(rehearsal) == 0 {
		return FieldCoverageDeltaV1{}, errors.New("both coverage populations must be nonzero")
	}
	paths := map[string]bool{}
	for path := range baselineProfiles {
		paths[path] = true
	}
	for path := range rehearsalProfiles {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	result := FieldCoverageDeltaV1{SchemaVersion: FieldCoverageDeltaSchemaVersion, CapturedScheduleSHA256: CapturedScheduleSHA256, BaselineRawSessionSHA256: baselineRawSHA, RehearsalRawSessionSHA256: rehearsalRawSHA, NormalizationAlgorithmVersion: CoverageNormalizationVersion, EvidenceRootSHA256: evidenceRootSHA, BaselineSourceFrameCount: uint64(len(baseline)), RehearsalSourceFrameCount: uint64(len(rehearsal))}
	for _, path := range ordered {
		base := finalizeCoverageProfile(baselineProfiles[path], uint64(len(baseline)))
		rehearsed := finalizeCoverageProfile(rehearsalProfiles[path], uint64(len(rehearsal)))
		classification, classErr := classifyCoverage(base, rehearsed)
		if classErr != nil {
			return FieldCoverageDeltaV1{}, fmt.Errorf("path %s: %w", path, classErr)
		}
		result.Paths = append(result.Paths, FieldCoveragePathDeltaV1{Path: path, Baseline: base, Rehearsal: rehearsed, Classification: classification})
	}
	if err := ValidateFieldCoverageDelta(result); err != nil {
		return FieldCoverageDeltaV1{}, err
	}
	return result, nil
}

func profileCoverageFrames(frames []CoverageFrameV1) (map[string]*mutableFieldProfile, error) {
	profiles := map[string]*mutableFieldProfile{}
	var previous uint64
	seenTuple := map[string]bool{}
	for index, frame := range frames {
		if frame.Sequence == 0 || frame.RawRecordSHA256 == "" || !validSHA256(frame.RawRecordSHA256) {
			return nil, errors.New("frame identity component is missing")
		}
		if index > 0 && frame.Sequence <= previous {
			return nil, errors.New("frame sequence is repeated or out of order")
		}
		identityRecord := frame.IdentityRecord
		if identityRecord == nil {
			identityRecord = frame.RawRecord
		}
		if payloadSHA(identityRecord) != frame.RawRecordSHA256 {
			return nil, errors.New("raw record hash mismatch")
		}
		tuple := fmt.Sprintf("%d:%s", frame.Sequence, frame.RawRecordSHA256)
		if seenTuple[tuple] {
			return nil, errors.New("repeated frame identity")
		}
		seenTuple[tuple] = true
		previous = frame.Sequence
		decoder := json.NewDecoder(bytes.NewReader(frame.RawRecord))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("raw record is malformed JSON")
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return nil, errors.New("raw record has trailing JSON")
		}
		if err := walkCoverage(value, "", frame.Sequence, profiles); err != nil {
			return nil, err
		}
	}
	return profiles, nil
}

func walkCoverage(value any, path string, frame uint64, profiles map[string]*mutableFieldProfile) error {
	if path != "" {
		observeCoverage(profiles, path, frame, coverageJSONType(value))
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		collisionKinds := map[string]int{}
		for _, key := range keys {
			segment, dynamic, err := normalizeCoverageKey(key)
			if err != nil {
				return err
			}
			child := path + "/" + segment
			if dynamic {
				collisionKinds[segment]++
				if collisionKinds[segment] > 1 {
					ensureMutableProfile(profiles, child).DynamicCollisionCount++
				}
			}
			if err := walkCoverage(typed[key], child, frame, profiles); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := walkCoverage(item, path+"/a:[]", frame, profiles); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeCoverageKey(key string) (string, bool, error) {
	if !utf8.ValidString(key) || strings.ContainsAny(key, "\x00\r\n") {
		return "", false, errors.New("unclassifiable object key")
	}
	escaped := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
	if sensitiveCoverageKeys[strings.ToLower(key)] {
		return "d:sensitive", true, nil
	}
	if decimalCoverageKey.MatchString(key) {
		return "d:decimal", true, nil
	}
	if uuidCoverageKey.MatchString(key) {
		return "d:uuid", true, nil
	}
	if opaqueCoverageKey.MatchString(key) {
		return "d:opaque", true, nil
	}
	if fixedCoverageKey.MatchString(key) {
		return "k:" + escaped, false, nil
	}
	return "", false, fmt.Errorf("unclassifiable object key shape")
}

func ensureMutableProfile(profiles map[string]*mutableFieldProfile, path string) *mutableFieldProfile {
	profile := profiles[path]
	if profile == nil {
		profile = &mutableFieldProfile{types: map[string]bool{}, frames: map[uint64]bool{}}
		profiles[path] = profile
	}
	return profile
}

func observeCoverage(profiles map[string]*mutableFieldProfile, path string, frame uint64, jsonType string) {
	profile := ensureMutableProfile(profiles, path)
	profile.SeenCount++
	profile.types[jsonType] = true
	if jsonType == "null" {
		profile.NullCount++
	}
	if !profile.frames[frame] {
		profile.frames[frame] = true
		profile.FrameCount++
	}
}

func coverageJSONType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "invalid"
	}
}

func finalizeCoverageProfile(profile *mutableFieldProfile, source uint64) FieldPathProfileV1 {
	if profile == nil {
		return FieldPathProfileV1{JSONTypes: []string{}, Presence: "never"}
	}
	result := profile.FieldPathProfileV1
	result.JSONTypes = make([]string, 0, len(profile.types))
	for value := range profile.types {
		result.JSONTypes = append(result.JSONTypes, value)
	}
	sort.Strings(result.JSONTypes)
	result.Nullable = result.NullCount > 0
	result.HasDynamicCollision = result.DynamicCollisionCount > 0
	switch {
	case result.FrameCount == 0:
		result.Presence = "never"
	case result.FrameCount == source:
		result.Presence = "always"
	default:
		result.Presence = "sometimes"
	}
	return result
}

func classifyCoverage(baseline, rehearsal FieldPathProfileV1) (string, error) {
	if baseline.Presence == "never" && rehearsal.Presence == "never" {
		return "", errors.New("union path is never on both sides")
	}
	if baseline.Presence != "never" && rehearsal.Presence == "never" {
		return "missing_in_rehearsal", nil
	}
	if baseline.Presence == "never" && rehearsal.Presence != "never" {
		return "additional_in_rehearsal", nil
	}
	if baseline.Presence != rehearsal.Presence || baseline.Nullable != rehearsal.Nullable || !equalStrings(baseline.JSONTypes, rehearsal.JSONTypes) || baseline.HasDynamicCollision != rehearsal.HasDynamicCollision {
		return "different_type_or_nullability", nil
	}
	return "same", nil
}

func ValidateFieldCoverageDelta(delta FieldCoverageDeltaV1) error {
	if delta.SchemaVersion != FieldCoverageDeltaSchemaVersion || delta.CapturedScheduleSHA256 != CapturedScheduleSHA256 || delta.NormalizationAlgorithmVersion != CoverageNormalizationVersion || !validSHA256(delta.BaselineRawSessionSHA256) || delta.BaselineRawSessionSHA256 != CapturedScheduleSHA256 || !validSHA256(delta.RehearsalRawSessionSHA256) || !validSHA256(delta.EvidenceRootSHA256) || delta.BaselineSourceFrameCount == 0 || delta.RehearsalSourceFrameCount == 0 || len(delta.Paths) == 0 {
		return errors.New("coverage identity or population mismatch")
	}
	allowedTypes := map[string]bool{"null": true, "boolean": true, "number": true, "string": true, "array": true, "object": true}
	seen := map[string]bool{}
	for index, item := range delta.Paths {
		if item.Path == "" || !validNormalizedCoveragePath(item.Path) || seen[item.Path] || index > 0 && delta.Paths[index-1].Path >= item.Path {
			return errors.New("coverage union paths are omitted, duplicated, or unsorted")
		}
		seen[item.Path] = true
		for _, side := range []struct {
			p FieldPathProfileV1
			n uint64
		}{{item.Baseline, delta.BaselineSourceFrameCount}, {item.Rehearsal, delta.RehearsalSourceFrameCount}} {
			expectedPresence := "sometimes"
			if side.p.FrameCount == 0 {
				expectedPresence = "never"
			} else if side.p.FrameCount == side.n {
				expectedPresence = "always"
			}
			if side.p.FrameCount > side.n || side.p.SeenCount < side.p.FrameCount || side.p.NullCount > side.p.SeenCount || side.p.Nullable != (side.p.NullCount > 0) || side.p.HasDynamicCollision != (side.p.DynamicCollisionCount > 0) || side.p.Presence != expectedPresence {
				return errors.New("coverage count invariant failed")
			}
			for typeIndex, jsonType := range side.p.JSONTypes {
				if !allowedTypes[jsonType] || typeIndex > 0 && side.p.JSONTypes[typeIndex-1] >= jsonType {
					return errors.New("coverage JSON types are unknown, duplicated, or unsorted")
				}
			}
			if side.p.FrameCount == 0 && (side.p.SeenCount != 0 || side.p.NullCount != 0 || len(side.p.JSONTypes) != 0 || side.p.DynamicCollisionCount != 0) || side.p.FrameCount > 0 && len(side.p.JSONTypes) == 0 {
				return errors.New("coverage absent/observed population is inconsistent")
			}
		}
		classification, err := classifyCoverage(item.Baseline, item.Rehearsal)
		if err != nil || classification != item.Classification {
			return errors.New("coverage classification mismatch")
		}
	}
	return nil
}

func validNormalizedCoveragePath(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if segment == "a:[]" || segment == "d:decimal" || segment == "d:uuid" || segment == "d:opaque" || segment == "d:sensitive" {
			continue
		}
		if !strings.HasPrefix(segment, "k:") || strings.Contains(strings.TrimPrefix(segment, "k:"), "~") && !validCoverageEscape(strings.TrimPrefix(segment, "k:")) {
			return false
		}
	}
	return true
}

func validCoverageEscape(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] != '~' {
			continue
		}
		if index+1 >= len(value) || value[index+1] != '0' && value[index+1] != '1' {
			return false
		}
		index++
	}
	return true
}

func loadBaselineCoverageFrames(path string) ([]CoverageFrameV1, string, error) {
	payload, err := readBoundedFile(path, MaxRehearsalRawBytes)
	if err != nil || payloadSHA(payload) != CapturedScheduleSHA256 {
		return nil, "", errors.New("captured schedule baseline hash mismatch")
	}
	var schedule struct {
		Updates []struct {
			Body json.RawMessage `json:"body"`
		} `json:"updates"`
	}
	if err := json.Unmarshal(payload, &schedule); err != nil || len(schedule.Updates) == 0 {
		return nil, "", errors.New("captured schedule baseline is invalid")
	}
	frames := make([]CoverageFrameV1, 0, len(schedule.Updates))
	for index, update := range schedule.Updates {
		body := append(json.RawMessage(nil), update.Body...)
		frames = append(frames, CoverageFrameV1{Sequence: uint64(index + 1), RawRecordSHA256: payloadSHA(body), RawRecord: body})
	}
	return frames, payloadSHA(payload), nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

package m4match

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
)

const (
	coverageSchemaV1       = "public_match_field_coverage.v1"
	coverageAlgorithmV1    = "typed-value-free-json-shape.v1"
	acceptedBaselineSHA256 = "2c87c90fe9bb472ff8ad44efd5838b9ea20eab9b932f20df26719785cc4ae30e"
)

type CoverageTypeCountV1 struct {
	Type  string `json:"type"`
	Count uint64 `json:"count"`
}
type CoveragePathV1 struct {
	Path        string                `json:"path"`
	Frames      uint64                `json:"frames"`
	Occurrences uint64                `json:"occurrences"`
	Nulls       uint64                `json:"nulls"`
	Collisions  uint64                `json:"dynamic_collisions"`
	Types       []CoverageTypeCountV1 `json:"types"`
}
type FieldCoverageDeltaV1 struct {
	SchemaVersion         string           `json:"schema_version"`
	Algorithm             string           `json:"algorithm"`
	AcceptedRehearsalSpec string           `json:"accepted_rehearsal_spec"`
	BaselineSHA256        string           `json:"baseline_sha256"`
	RehearsalRawSHA256    string           `json:"rehearsal_raw_sha256"`
	EvidenceRootSHA256    string           `json:"evidence_root_sha256"`
	BaselineFrames        uint64           `json:"baseline_frames"`
	RehearsalFrames       uint64           `json:"rehearsal_frames"`
	Baseline              []CoveragePathV1 `json:"baseline"`
	Rehearsal             []CoveragePathV1 `json:"rehearsal"`
	Added                 []string         `json:"added_paths"`
	Missing               []string         `json:"missing_paths"`
	Changed               []string         `json:"changed_paths"`
}
type FieldCoverageUnavailableV1 struct {
	Reason             string `json:"reason"`
	Observed           uint64 `json:"observed_frames"`
	Accepted           uint64 `json:"accepted_frames"`
	Rejected           uint64 `json:"rejected_frames"`
	BaselineSHA256     string `json:"baseline_sha256,omitempty"`
	RehearsalRawSHA256 string `json:"rehearsal_raw_sha256,omitempty"`
}
type FieldCoverageStatusV1 struct {
	State       string                      `json:"state"`
	Complete    *FieldCoverageDeltaV1       `json:"complete,omitempty"`
	Unavailable *FieldCoverageUnavailableV1 `json:"unavailable,omitempty"`
}

type coverageAccumulator struct {
	frames uint64
	paths  map[string]*coveragePathAccumulator
}
type coveragePathAccumulator struct {
	frames                         map[uint64]bool
	occurrences, nulls, collisions uint64
	types                          map[string]uint64
}

func buildCoverageDelta(baseline, rehearsal [][]byte, rehearsalRawSHA, rootSHA string) (FieldCoverageDeltaV1, error) {
	if len(baseline) == 0 || len(baseline) > MaxRehearsalCoverageFrames || len(rehearsal) < 2 || len(rehearsal) > MaxRehearsalCoverageFrames || !validSHA256(rehearsalRawSHA) || !validSHA256(rootSHA) {
		return FieldCoverageDeltaV1{}, errors.New("coverage inputs incomplete")
	}
	baseProfile, err := profileCoverage(baseline)
	if err != nil {
		return FieldCoverageDeltaV1{}, err
	}
	rehearsalProfile, err := profileCoverage(rehearsal)
	if err != nil {
		return FieldCoverageDeltaV1{}, err
	}
	delta := FieldCoverageDeltaV1{SchemaVersion: coverageSchemaV1, Algorithm: coverageAlgorithmV1, AcceptedRehearsalSpec: AcceptedRehearsalSpec, BaselineSHA256: acceptedBaselineSHA256, RehearsalRawSHA256: rehearsalRawSHA, EvidenceRootSHA256: rootSHA, BaselineFrames: uint64(len(baseline)), RehearsalFrames: uint64(len(rehearsal)), Baseline: baseProfile, Rehearsal: rehearsalProfile, Added: []string{}, Missing: []string{}, Changed: []string{}}
	baseByPath, runByPath := indexCoverage(baseProfile), indexCoverage(rehearsalProfile)
	for path, value := range runByPath {
		before, ok := baseByPath[path]
		if !ok {
			delta.Added = append(delta.Added, path)
			continue
		}
		if !sameCoverageShape(before, value) {
			delta.Changed = append(delta.Changed, path)
		}
	}
	for path := range baseByPath {
		if _, ok := runByPath[path]; !ok {
			delta.Missing = append(delta.Missing, path)
		}
	}
	sort.Strings(delta.Added)
	sort.Strings(delta.Missing)
	sort.Strings(delta.Changed)
	return delta, validateCoverageDelta(delta)
}

func profileCoverage(frames [][]byte) ([]CoveragePathV1, error) {
	acc := coverageAccumulator{paths: map[string]*coveragePathAccumulator{}}
	for index, payload := range frames {
		var value any
		decoder := json.NewDecoder(strings.NewReader(string(payload)))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("coverage frame is malformed")
		}
		acc.frames++
		if err := acc.walk(value, "", uint64(index+1), map[string]uint64{}); err != nil {
			return nil, err
		}
	}
	result := make([]CoveragePathV1, 0, len(acc.paths))
	for path, value := range acc.paths {
		entry := CoveragePathV1{Path: path, Frames: uint64(len(value.frames)), Occurrences: value.occurrences, Nulls: value.nulls, Collisions: value.collisions}
		for kind, count := range value.types {
			entry.Types = append(entry.Types, CoverageTypeCountV1{Type: kind, Count: count})
		}
		sort.Slice(entry.Types, func(i, j int) bool { return entry.Types[i].Type < entry.Types[j].Type })
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func (a *coverageAccumulator) walk(value any, path string, frame uint64, seen map[string]uint64) error {
	if path == "" {
		path = "/"
	}
	entry := a.paths[path]
	if entry == nil {
		entry = &coveragePathAccumulator{frames: map[uint64]bool{}, types: map[string]uint64{}}
		a.paths[path] = entry
	}
	entry.frames[frame] = true
	entry.occurrences++
	seen[path]++
	if seen[path] > 1 {
		entry.collisions++
	}
	kind := coverageKind(value)
	entry.types[kind]++
	if value == nil {
		entry.nulls++
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := a.walk(typed[key], appendCoverageSegment(path, classifyCoverageKey(key)), frame, seen); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := a.walk(child, appendCoverageSegment(path, "[]"), frame, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func classifyCoverageKey(key string) string {
	lower := strings.ToLower(key)
	for _, sensitive := range []string{"steamid", "steam_id", "accountid", "account_id", "personaname", "persona_name", "display_name", "chat"} {
		if lower == sensitive {
			return "{sensitive}"
		}
	}
	if _, err := strconv.ParseUint(key, 10, 64); err == nil {
		return "{dynamic}"
	}
	return strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}
func appendCoverageSegment(path, segment string) string {
	if path == "/" {
		return "/" + segment
	}
	return path + "/" + segment
}
func coverageKind(value any) string {
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
func indexCoverage(values []CoveragePathV1) map[string]CoveragePathV1 {
	result := map[string]CoveragePathV1{}
	for _, v := range values {
		result[v.Path] = v
	}
	return result
}
func sameCoverageShape(a, b CoveragePathV1) bool {
	if a.Nulls != b.Nulls || len(a.Types) != len(b.Types) {
		return false
	}
	for i := range a.Types {
		if a.Types[i] != b.Types[i] {
			return false
		}
	}
	return true
}

func validateCoverageDelta(value FieldCoverageDeltaV1) error {
	if value.SchemaVersion != coverageSchemaV1 || value.Algorithm != coverageAlgorithmV1 || value.AcceptedRehearsalSpec != AcceptedRehearsalSpec || value.BaselineSHA256 != acceptedBaselineSHA256 || !validSHA256(value.RehearsalRawSHA256) || !validSHA256(value.EvidenceRootSHA256) || value.BaselineFrames == 0 || value.RehearsalFrames < 2 {
		return errors.New("coverage identity invalid")
	}
	for _, population := range [][]CoveragePathV1{value.Baseline, value.Rehearsal} {
		prior := ""
		for _, path := range population {
			if path.Path == "" || path.Path <= prior || path.Frames == 0 || path.Occurrences < path.Frames || len(path.Types) == 0 || strings.Contains(strings.ToLower(path.Path), "steam") || strings.Contains(strings.ToLower(path.Path), "account") || strings.Contains(strings.ToLower(path.Path), "persona") || strings.Contains(strings.ToLower(path.Path), "display_name") {
				return errors.New("coverage population invalid or identity leaking")
			}
			prior = path.Path
		}
	}
	return nil
}

func coverageRootSHA(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

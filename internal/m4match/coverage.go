package m4match

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

type coverageAccumulator struct {
	present    map[uint64]bool
	null       map[uint64]bool
	occurrence uint64
	types      map[string]bool
}

const (
	maxCoverageBaselineBytes = 1 << 20
	maxCoveragePaths         = 4096
	maxCoverageDepth         = 64
	maxCoverageOccurrences   = 1 << 20
)

var stableCoverageKeys = map[string]bool{
	"provider": true, "map": true, "player": true, "hero": true, "abilities": true, "items": true, "buildings": true,
	"auth": true, "previously": true, "added": true, "name": true, "appid": true, "version": true, "timestamp": true,
	"matchid": true, "game_time": true, "clock_time": true, "daytime": true, "nightstalker_night": true, "game_state": true,
	"win_team": true, "customgamename": true, "ward_purchase_cooldown": true, "team2": true, "team3": true,
	"score": true, "id": true, "xpos": true, "ypos": true, "health": true, "max_health": true, "level": true,
	"alive": true, "respawn_seconds": true, "buyback_cost": true, "buyback_cooldown": true, "gold": true,
	"gold_reliable": true, "gold_unreliable": true, "gpm": true, "xpm": true, "net_worth": true, "kills": true,
	"deaths": true, "assists": true, "last_hits": true, "denies": true, "mana": true, "max_mana": true,
	"silenced": true, "stunned": true, "disarmed": true, "magicimmune": true, "hexed": true, "muted": true,
	"break": true, "has_debuff": true, "smoked": true, "slot": true, "can_cast": true, "cooldown": true,
	"max_cooldown": true, "charges": true, "passive": true, "ultimate": true, "item_level": true,
}

func buildFieldCoverage(records []*session.Record, manifest []session.RawRecordAttestationV1, baselinePath string) (FieldCoverageDeltaV1, error) {
	if len(records) == 0 || len(records) != len(manifest) || len(records) > MaxRehearsalCoverageFrames {
		return FieldCoverageDeltaV1{}, errors.New("coverage population invalid")
	}
	baselineBodies, err := readCoverageBaseline(baselinePath)
	if err != nil {
		return FieldCoverageDeltaV1{}, err
	}
	manifestSHA, err := contracts.CanonicalSHA256(manifest)
	if err != nil {
		return FieldCoverageDeltaV1{}, err
	}
	baseline := map[string]*coverageAccumulator{}
	for index, value := range baselineBodies {
		if err := walkCoverage(value, "$", uint64(index+1), 0, baseline); err != nil {
			return FieldCoverageDeltaV1{}, err
		}
	}
	rehearsal := map[string]*coverageAccumulator{}
	for index, record := range records {
		if record == nil || record.Sequence != uint64(index+1) {
			return FieldCoverageDeltaV1{}, errors.New("coverage record order invalid")
		}
		decoder := json.NewDecoder(bytes.NewReader(record.Raw))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) == nil {
			return FieldCoverageDeltaV1{}, errors.New("coverage raw JSON invalid")
		}
		if err := walkCoverage(value, "$", record.Sequence, 0, rehearsal); err != nil {
			return FieldCoverageDeltaV1{}, err
		}
	}
	paths := make([]string, 0, len(baseline)+len(rehearsal))
	seen := map[string]bool{}
	for path := range baseline {
		paths = append(paths, path)
		seen[path] = true
	}
	for path := range rehearsal {
		if !seen[path] {
			paths = append(paths, path)
		}
	}
	if len(paths) > maxCoveragePaths {
		return FieldCoverageDeltaV1{}, errors.New("coverage path population exceeds bound")
	}
	sort.Strings(paths)
	delta := FieldCoverageDeltaV1{
		SchemaVersion: RehearsalCoverageSchemaV1, Algorithm: "value_free_typed_paths.v1", BaselineSHA256: CapturedScheduleSHA256,
		RehearsalSpec: AcceptedRehearsalSpec, RawManifestSHA256: manifestSHA, BaselineFrames: uint64(len(baselineBodies)), FrameCount: uint64(len(records)),
	}
	for _, path := range paths {
		base := baseline[path]
		observed := rehearsal[path]
		basePresent, baseNull, baseOccurrences, baseTypes := coverageFacts(base)
		observedPresent, observedNull, observedOccurrences, observedTypes := coverageFacts(observed)
		classification := classifyCoverage(basePresent, baseNull, baseOccurrences, baseTypes, observedPresent, observedNull, observedOccurrences, observedTypes)
		delta.Entries = append(delta.Entries, FieldCoverageEntryV1{
			Path: path, PathKind: "typed_json", BaselinePresentFrames: basePresent, BaselineNullFrames: baseNull,
			BaselineOccurrenceCount: baseOccurrences, BaselineTypes: baseTypes, RehearsalPresentFrames: observedPresent,
			RehearsalNullFrames: observedNull, RehearsalOccurrenceCount: observedOccurrences, RehearsalTypes: observedTypes,
			Classification: classification,
		})
	}
	delta.ContentSHA256, err = coverageContentID(delta)
	return delta, err
}

func coverageContentID(delta FieldCoverageDeltaV1) (string, error) {
	delta.ContentSHA256 = ""
	return contracts.CanonicalSHA256(delta)
}

func walkCoverage(value any, path string, frame uint64, depth int, acc map[string]*coverageAccumulator) error {
	if depth > maxCoverageDepth {
		return errors.New("coverage nesting exceeds bound")
	}
	if err := recordCoverage(path, value, frame, acc); err != nil {
		return err
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			segment := ".{dynamic}"
			if stableCoverageKeys[key] && !sensitiveCoverageKey(key) {
				segment = "." + key
			}
			if err := walkCoverage(typed[key], path+segment, frame, depth+1, acc); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := walkCoverage(child, path+"[]", frame, depth+1, acc); err != nil {
				return err
			}
		}
	}
	return nil
}

func recordCoverage(path string, value any, frame uint64, acc map[string]*coverageAccumulator) error {
	entry := acc[path]
	if entry == nil {
		if len(acc) >= maxCoveragePaths {
			return errors.New("coverage path population exceeds bound")
		}
		entry = &coverageAccumulator{present: map[uint64]bool{}, null: map[uint64]bool{}, types: map[string]bool{}}
		acc[path] = entry
	}
	entry.present[frame] = true
	entry.occurrence++
	if entry.occurrence > maxCoverageOccurrences {
		return errors.New("coverage occurrence population exceeds bound")
	}
	typeName := "null"
	switch value.(type) {
	case nil:
		entry.null[frame] = true
	case bool:
		typeName = "boolean"
	case json.Number:
		typeName = "number"
	case string:
		typeName = "string"
	case []any:
		typeName = "array"
	case map[string]any:
		typeName = "object"
	}
	entry.types[typeName] = true
	return nil
}

func readCoverageBaseline(path string) ([]any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxCoverageBaselineBytes+1))
	if err != nil || len(payload) > maxCoverageBaselineBytes || payloadSHA(payload) != CapturedScheduleSHA256 {
		return nil, errors.New("coverage baseline identity mismatch")
	}
	var envelope struct {
		Updates []struct {
			Body json.RawMessage `json:"body"`
		} `json:"updates"`
	}
	if json.Unmarshal(payload, &envelope) != nil || len(envelope.Updates) == 0 || len(envelope.Updates) > MaxRehearsalCoverageFrames {
		return nil, errors.New("coverage baseline population invalid")
	}
	result := make([]any, 0, len(envelope.Updates))
	for _, update := range envelope.Updates {
		decoder := json.NewDecoder(bytes.NewReader(update.Body))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) == nil {
			return nil, errors.New("coverage baseline body invalid")
		}
		result = append(result, value)
	}
	return result, nil
}

func coverageFacts(value *coverageAccumulator) (uint64, uint64, uint64, []string) {
	if value == nil {
		return 0, 0, 0, []string{}
	}
	types := make([]string, 0, len(value.types))
	for name := range value.types {
		types = append(types, name)
	}
	sort.Strings(types)
	return uint64(len(value.present)), uint64(len(value.null)), value.occurrence, types
}

func classifyCoverage(basePresent, baseNull, baseOccurrences uint64, baseTypes []string, observedPresent, observedNull, observedOccurrences uint64, observedTypes []string) string {
	switch {
	case baseOccurrences > basePresent || observedOccurrences > observedPresent:
		return "dynamic_collision"
	case basePresent == 0:
		return "newly_observed"
	case observedPresent == 0:
		return "absent_in_rehearsal"
	case !reflect.DeepEqual(baseTypes, observedTypes):
		return "type_changed"
	case baseNull != observedNull:
		return "nullability_changed"
	case basePresent != observedPresent || baseOccurrences != observedOccurrences:
		return "presence_changed"
	default:
		return "stable"
	}
}

func sensitiveCoverageKey(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range []string{"account", "steam", "player_name", "team_name", "token", "secret", "cookie"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

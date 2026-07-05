package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Profiler struct {
	mu            sync.RWMutex
	snapshotCount uint64
	startedAt     time.Time
	lastSeenAt    time.Time
	fields        map[string]*Field
}

type Field struct {
	Path        string    `json:"path"`
	SeenCount   uint64    `json:"seen_count"`
	NullCount   uint64    `json:"null_count"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	Sample      any       `json:"sample,omitempty"`
}

type Snapshot struct {
	SnapshotCount uint64     `json:"snapshot_count"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	Fields        []Field    `json:"fields"`
}

func NewProfiler() *Profiler {
	return &Profiler{
		fields: make(map[string]*Field),
	}
}

func (p *Profiler) Observe(receivedAt time.Time, payload any) {
	receivedAt = receivedAt.UTC()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.snapshotCount == 0 {
		p.startedAt = receivedAt
	}
	p.snapshotCount++
	p.lastSeenAt = receivedAt
	walk(p.fields, "", payload, receivedAt)
}

func (p *Profiler) Snapshot() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()

	snapshot := Snapshot{
		SnapshotCount: p.snapshotCount,
		Fields:        make([]Field, 0, len(p.fields)),
	}
	if p.snapshotCount > 0 {
		startedAt := p.startedAt
		lastSeenAt := p.lastSeenAt
		snapshot.StartedAt = &startedAt
		snapshot.LastSeenAt = &lastSeenAt
	}

	for _, field := range p.fields {
		copied := *field
		copied.Sample = clone(field.Sample)
		snapshot.Fields = append(snapshot.Fields, copied)
	}
	sort.Slice(snapshot.Fields, func(i, j int) bool {
		return snapshot.Fields[i].Path < snapshot.Fields[j].Path
	})
	return snapshot
}

func WriteSummary(path string, snapshot Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create summary dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(RenderSummary(snapshot)), 0o644); err != nil {
		return fmt.Errorf("write session summary: %w", err)
	}
	return nil
}

func RenderSummary(snapshot Snapshot) string {
	paths := make([]string, 0, len(snapshot.Fields))
	for _, field := range snapshot.Fields {
		paths = append(paths, field.Path)
	}

	var builder strings.Builder
	builder.WriteString("# Session Summary\n\n")
	builder.WriteString(fmt.Sprintf("- Snapshot count: %d\n", snapshot.SnapshotCount))
	builder.WriteString(fmt.Sprintf("- Field count: %d\n", len(snapshot.Fields)))
	if snapshot.StartedAt != nil {
		builder.WriteString(fmt.Sprintf("- Session start: %s\n", snapshot.StartedAt.Format(time.RFC3339Nano)))
	}
	if snapshot.LastSeenAt != nil {
		builder.WriteString(fmt.Sprintf("- Session last update: %s\n", snapshot.LastSeenAt.Format(time.RFC3339Nano)))
	}

	builder.WriteString("\n## Availability\n\n")
	builder.WriteString(fmt.Sprintf("- Ten-player hero/player data: %s\n", availability(hasTenPlayerData(paths))))
	builder.WriteString(fmt.Sprintf("- Hero positions: %s\n", availability(hasAnySuffix(paths, ".xpos") && hasAnySuffix(paths, ".ypos"))))
	builder.WriteString(fmt.Sprintf("- Economy fields: %s\n", availability(hasAnySuffix(paths, ".gold") || hasAnySuffix(paths, ".net_worth") || hasAnySuffix(paths, ".gpm") || hasAnySuffix(paths, ".xpm"))))
	builder.WriteString(fmt.Sprintf("- Item fields: %s\n", availability(hasPathPart(paths, "items"))))
	builder.WriteString(fmt.Sprintf("- Ability fields: %s\n", availability(hasPathPart(paths, "abilities"))))
	builder.WriteString(fmt.Sprintf("- Building/Roshan/map fields: %s\n", availability(hasPathPart(paths, "buildings") || hasPathPart(paths, "map") || hasSubstring(paths, "roshan"))))
	builder.WriteString(fmt.Sprintf("- Ward-related fields: %s\n", availability(hasSubstring(paths, "ward"))))
	builder.WriteString(fmt.Sprintf("- Exact ward coordinates: %s\n", wardCoordinateConclusion(paths)))
	return builder.String()
}

func walk(fields map[string]*Field, path string, value any, receivedAt time.Time) {
	if path != "" {
		record(fields, path, value, receivedAt)
	}

	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			walk(fields, join(path, key), typed[key], receivedAt)
		}
	case []any:
		for index, item := range typed {
			walk(fields, fmt.Sprintf("%s[%d]", path, index), item, receivedAt)
		}
	}
}

func record(fields map[string]*Field, path string, value any, receivedAt time.Time) {
	field, ok := fields[path]
	if !ok {
		field = &Field{
			Path:        path,
			FirstSeenAt: receivedAt,
		}
		fields[path] = field
	}
	field.SeenCount++
	field.LastSeenAt = receivedAt
	if value == nil {
		field.NullCount++
		return
	}
	if field.Sample == nil && isScalar(value) {
		field.Sample = clone(value)
	}
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func isScalar(value any) bool {
	switch value.(type) {
	case string, bool, float64, json.Number, int, int64, uint64:
		return true
	default:
		return false
	}
}

func clone(value any) any {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

func availability(ok bool) string {
	if ok {
		return "available"
	}
	return "not observed"
}

func hasTenPlayerData(paths []string) bool {
	for _, team := range []string{"team2", "team3"} {
		for index := 0; index < 5; index++ {
			prefixHero := fmt.Sprintf("hero.%s.player%d", team, index)
			prefixPlayer := fmt.Sprintf("player.%s.player%d", team, index)
			if !hasPrefix(paths, prefixHero) && !hasPrefix(paths, prefixPlayer) {
				return false
			}
		}
	}
	return true
}

func hasPrefix(paths []string, prefix string) bool {
	for _, path := range paths {
		if path == prefix || strings.HasPrefix(path, prefix+".") {
			return true
		}
	}
	return false
}

func hasAnySuffix(paths []string, suffix string) bool {
	for _, path := range paths {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

func hasPathPart(paths []string, part string) bool {
	for _, path := range paths {
		if path == part || strings.HasPrefix(path, part+".") || strings.Contains(path, "."+part+".") {
			return true
		}
	}
	return false
}

func hasSubstring(paths []string, substring string) bool {
	for _, path := range paths {
		if strings.Contains(strings.ToLower(path), substring) {
			return true
		}
	}
	return false
}

func wardCoordinateConclusion(paths []string) string {
	hasWard := false
	for _, path := range paths {
		lower := strings.ToLower(path)
		if !strings.Contains(lower, "ward") {
			continue
		}
		hasWard = true
		if strings.HasSuffix(lower, ".xpos") || strings.HasSuffix(lower, ".ypos") || strings.HasSuffix(lower, ".position") {
			return "available"
		}
	}
	if hasWard {
		return "not available"
	}
	return "not observed"
}

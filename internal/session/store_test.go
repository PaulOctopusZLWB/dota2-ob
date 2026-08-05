package session_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

type fakeRawFile struct {
	data         []byte
	offset       int64
	writeLimit   int
	truncateErr  error
	closeErr     error
	writeCalls   int
	seekMismatch bool
}

func (f *fakeRawFile) Write(p []byte) (int, error) {
	f.writeCalls++
	n := len(p)
	if f.writeLimit > 0 && n > f.writeLimit {
		n = f.writeLimit
	}
	end := int(f.offset) + n
	if end > len(f.data) {
		f.data = append(f.data, make([]byte, end-len(f.data))...)
	}
	copy(f.data[int(f.offset):end], p[:n])
	f.offset += int64(n)
	if n != len(p) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

func (f *fakeRawFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.offset = offset
	case io.SeekCurrent:
		f.offset += offset
	case io.SeekEnd:
		f.offset = int64(len(f.data)) + offset
	}
	if f.seekMismatch && whence == io.SeekStart {
		return f.offset + 1, nil
	}
	return f.offset, nil
}

func (f *fakeRawFile) Truncate(size int64) error {
	if f.truncateErr != nil {
		return f.truncateErr
	}
	f.data = f.data[:size]
	if f.offset > size {
		f.offset = size
	}
	return nil
}

func (f *fakeRawFile) Stat() (os.FileInfo, error) { return fakeFileInfo(int64(len(f.data))), nil }
func (f *fakeRawFile) Close() error               { return f.closeErr }

type fakeFileInfo int64

func (f fakeFileInfo) Name() string       { return "raw.jsonl" }
func (f fakeFileInfo) Size() int64        { return int64(f) }
func (f fakeFileInfo) Mode() os.FileMode  { return 0o644 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func TestStoreAppendWritesJSONLRecord(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 5, 12, 30, 45, 123000000, time.UTC)

	store, err := session.NewStore(root,
		session.WithClock(func() time.Time { return now }),
		session.WithSessionID("test-session"),
	)
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}

	record, err := store.Append([]byte(`{"provider":{"name":"Dota 2","appid":570},"map":{"game_time":123}}`))
	if err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	if record.ReceivedAt != now {
		t.Fatalf("record timestamp = %s, want %s", record.ReceivedAt, now)
	}

	rawPath := filepath.Join(root, "test-session", "raw.jsonl")
	data, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read raw JSONL: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if got, want := len(lines), 1; got != want {
		t.Fatalf("line count = %d, want %d; data=%q", got, want, string(data))
	}

	var persisted struct {
		SchemaVersion int             `json:"schema_version"`
		SessionID     string          `json:"session_id"`
		Sequence      uint64          `json:"sequence"`
		ReceivedAt    time.Time       `json:"received_at"`
		Source        string          `json:"source"`
		Payload       map[string]any  `json:"payload"`
		Raw           json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &persisted); err != nil {
		t.Fatalf("JSONL line did not parse as JSON object: %v", err)
	}

	if !persisted.ReceivedAt.Equal(now) {
		t.Fatalf("persisted timestamp = %s, want %s", persisted.ReceivedAt, now)
	}
	if persisted.SchemaVersion != 2 || persisted.SessionID != "test-session" || persisted.Sequence != 1 || persisted.Source != "gsi" {
		t.Fatalf("persisted capture identity = %#v", persisted)
	}
	if persisted.Payload["provider"] == nil {
		t.Fatalf("persisted payload missing provider: %#v", persisted.Payload)
	}

	var raw map[string]any
	if err := json.Unmarshal(persisted.Raw, &raw); err != nil {
		t.Fatalf("raw payload did not parse as JSON: %v", err)
	}
	if raw["map"] == nil {
		t.Fatalf("raw payload missing map: %#v", raw)
	}
}

func TestStoreShortWriteRollsBackAndReusesSequence(t *testing.T) {
	f := &fakeRawFile{data: []byte("existing\n"), offset: int64(len("existing\n")), writeLimit: 8}
	store, err := session.NewStore(t.TempDir(),
		session.WithSessionID("short-write"),
		session.WithRawFile(func(string) (session.RawFile, error) { return f, nil }),
	)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := store.Append([]byte(`{"map":{"game_time":1}}`)); !errors.Is(err, session.ErrAppendFailed) {
		t.Fatalf("Append error = %v, want ErrAppendFailed", err)
	}
	if got := string(f.data); got != "existing\n" {
		t.Fatalf("data after rollback = %q", got)
	}
	f.writeLimit = 0
	rec, err := store.Append([]byte(`{"map":{"game_time":2}}`))
	if err != nil {
		t.Fatalf("recovery append: %v", err)
	}
	if rec.Sequence != 1 {
		t.Fatalf("sequence = %d, want 1", rec.Sequence)
	}
}

func TestStoreRollbackFailureSealsWithoutFurtherWrites(t *testing.T) {
	f := &fakeRawFile{writeLimit: 4, truncateErr: errors.New("disk refused rollback")}
	store, err := session.NewStore(t.TempDir(),
		session.WithSessionID("sealed"),
		session.WithRawFile(func(string) (session.RawFile, error) { return f, nil }),
	)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := store.Append([]byte(`{"ok":true}`)); !errors.Is(err, session.ErrStoreSealed) {
		t.Fatalf("first Append error = %v, want ErrStoreSealed", err)
	}
	calls := f.writeCalls
	if _, err := store.Append([]byte(`{"ok":true}`)); !errors.Is(err, session.ErrStoreSealed) {
		t.Fatalf("second Append error = %v, want ErrStoreSealed", err)
	}
	if f.writeCalls != calls {
		t.Fatalf("sealed store attempted another write")
	}
}

func TestStoreRollbackSeekMismatchSeals(t *testing.T) {
	f := &fakeRawFile{writeLimit: 4, seekMismatch: true}
	store, err := session.NewStore(t.TempDir(), session.WithSessionID("seek-mismatch"), session.WithRawFile(func(string) (session.RawFile, error) { return f, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`{"ok":true}`)); !errors.Is(err, session.ErrStoreSealed) {
		t.Fatalf("Append error=%v", err)
	}
}

func TestStoreRecoveryRemovesOnlyUnterminatedTail(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "recover")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	valid := `{"schema_version":2,"session_id":"recover","sequence":1,"received_at":"2026-08-05T12:00:00Z","source":"gsi","payload":{},"raw":{}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), append([]byte(valid), []byte(`{"partial"`)...), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewStore(root, session.WithSessionID("recover"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	data, _ := os.ReadFile(store.RawPath())
	if !bytes.Equal(data, []byte(valid)) {
		t.Fatalf("recovered data = %q", data)
	}
	rec, err := store.Append([]byte(`{"ok":true}`))
	if err != nil || rec.Sequence != 2 {
		t.Fatalf("append after recovery: rec=%#v err=%v", rec, err)
	}
}

func TestStoreRecoveryRejectsTerminatedInvalidRecordWithoutMutation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "corrupt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("not-json\n")
	path := filepath.Join(dir, "raw.jsonl")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := session.NewStore(root, session.WithSessionID("corrupt")); err == nil {
		t.Fatal("NewStore succeeded for terminated corruption")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, want) {
		t.Fatalf("corrupt file changed: %q", got)
	}
}

func TestStoreRecoveryRejectsMissingVersionOneFields(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bad-v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := session.NewStore(root, session.WithSessionID("bad-v1")); err == nil {
		t.Fatal("NewStore accepted invalid v1 envelope")
	}
}

func TestStoreRecoveryRejectsInvalidVersionTwoRawEnvelope(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bad-v2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := `{"schema_version":2,"session_id":"bad-v2","sequence":1,"received_at":"2026-08-05T12:00:00Z","source":"gsi","payload":{"ok":true}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := session.NewStore(root, session.WithSessionID("bad-v2")); err == nil {
		t.Fatal("NewStore accepted v2 envelope without raw")
	}
}

func TestStoreAppendRejectsMalformedJSONWithoutWriting(t *testing.T) {
	root := t.TempDir()

	store, err := session.NewStore(root, session.WithSessionID("bad-session"))
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}

	_, err = store.Append([]byte(`{"provider":`))
	if !errors.Is(err, session.ErrInvalidJSON) {
		t.Fatalf("Append error = %v, want ErrInvalidJSON", err)
	}

	rawPath := filepath.Join(root, "bad-session", "raw.jsonl")
	if _, err := os.Stat(rawPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("raw file stat error = %v, want not exist", err)
	}
}

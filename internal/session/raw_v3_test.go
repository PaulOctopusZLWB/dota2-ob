package session_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture/v3fixture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func TestStoreAppendWritesExactRawRecordV3Evidence(t *testing.T) {
	now := time.Date(2026, 8, 14, 0, 20, 26, 123400000, time.FixedZone("offset", 8*60*60))
	raw := []byte(" \n{\"text\":\"<>&\\u2028\",\"n\":1e+09}\t")
	store, err := session.NewStore(t.TempDir(), session.WithSessionID("v3.session-1"), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Append(raw)
	if err != nil {
		t.Fatal(err)
	}
	if record.SchemaVersion != 3 || !stringEqualBytes(record.Raw, raw) || record.ProjectionResult != session.ProjectionProduced {
		t.Fatalf("record=%#v", record)
	}
	line, err := os.ReadFile(store.RawPath())
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(raw)
	want := `{"schema_version":3,"session_id":"v3.session-1","sequence":1,"received_at":"2026-08-13T16:20:26.1234Z","source":"gsi","raw_encoding":"base64_std","raw_byte_length":33,"raw_base64":"` + base64.StdEncoding.EncodeToString(raw) + `","raw_payload_sha256":"` + hex.EncodeToString(wantHash[:]) + `"}` + "\n"
	if string(line) != want {
		t.Fatalf("V3 line mismatch\nwant=%s\n got=%s", want, line)
	}
}

func TestStoreRawAdmissionAtTenMiBBoundary(t *testing.T) {
	for _, size := range []int{v3fixture.RawLimit - 1, v3fixture.RawLimit} {
		body := sizedUnknownObject(size)
		store, err := session.NewStore(t.TempDir(), session.WithSessionID(fmt.Sprintf("size-%d", size)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(body); err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
	}
	store, err := session.NewStore(t.TempDir(), session.WithSessionID("plus-one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(sizedUnknownObject(v3fixture.RawLimit + 1)); err == nil {
		t.Fatal("accepted 10 MiB plus one")
	}
}

func sizedUnknownObject(size int) []byte {
	prefix, suffix := []byte(`{"unknown":"`), []byte(`"}`)
	body := append([]byte(nil), prefix...)
	body = append(body, strings.Repeat("x", size-len(prefix)-len(suffix))...)
	return append(body, suffix...)
}

func TestGSIProjectionAcceptedLexicalAndUnicodeMatrix(t *testing.T) {
	cases := []struct{ name, raw, want string }{
		{"escapes", `{"provider":{"name":"\"\\\/\b\f\n\r\t"}}`, "\"\\/\b\f\n\r\t"},
		{"escaped-separators", `{"provider":{"name":"\u2028\u2029"}}`, "\u2028\u2029"},
		{"literal-separators", "{\"provider\":{\"name\":\"\u2028\u2029\"}}", "\u2028\u2029"},
		{"surrogate-pair", `{"provider":{"name":"\ud83d\ude00"}}`, "😀"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := session.NewStore(t.TempDir(), session.WithSessionID("lexical"))
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Append([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if got := record.Payload.(map[string]any)["provider"].(map[string]any)["name"]; got != tc.want {
				t.Fatalf("got=%q want=%q", got, tc.want)
			}
		})
	}
	for _, raw := range []string{`{"provider":{"timestamp":0}}`, `{"provider":{"timestamp":-0}}`, `{"provider":{"timestamp":1e+09}}`, `{"provider":{"timestamp":1.25E-2}}`} {
		store, _ := session.NewStore(t.TempDir(), session.WithSessionID("number"))
		if _, err := store.Append([]byte(raw)); err != nil {
			t.Fatalf("accepted number %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"provider":{"timestamp":01}}`, `{"provider":{"name":"\x"}}`, `{} {}`, "{}\x00"} {
		store, _ := session.NewStore(t.TempDir(), session.WithSessionID("invalid"))
		if _, err := store.Append([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid lexical form %q", raw)
		}
	}
}

func TestV3CanonicalAndNoncanonicalFramingMatrix(t *testing.T) {
	valid := rawV3Frame("frame", 1, "2026-08-14T00:20:26Z", []byte(`{}`))
	if _, err := session.DecodeRecordV3([]byte(valid), "frame", 1); err != nil {
		t.Fatal(err)
	}
	for _, frame := range []string{" " + valid, valid + " ", strings.Replace(valid, `,"session_id"`, `, "session_id"`, 1), strings.Replace(valid, `"schema_version":3`, `"schema_version":3.0`, 1), strings.Replace(valid, `"source":"gsi"`, `"source":"gsi" `, 1)} {
		if _, err := session.DecodeRecordV3([]byte(frame), "frame", 1); err == nil {
			t.Fatalf("accepted noncanonical frame %q", frame[:min(len(frame), 80)])
		}
	}
}

func TestV3ReadersRejectTerminatedFrameAtFormulaLimitBeforeLegacyCap(t *testing.T) {
	lateHead := `{"session_id":"overlimit","unknown":"`
	lateTail := `","schema_version":3,`
	lateOrdered := lateHead + strings.Repeat("x", session.MaxEncodedRecordBytes()+1-len(lateHead)-len(lateTail)) + lateTail
	for _, tc := range []struct{ name, prefix string }{
		{"canonical", `{"schema_version":3,`},
		{"noncanonical-number", ` { "schema_version" : 3.0,`},
		{"session-first", `{"session_id":"overlimit","schema_version":3,`},
		{"nested-unknown-first", `{"unknown":{"nested":true},"schema_version":3,`},
		{"adversarial-spacing-and-order", ` { "session_id" : "overlimit" , "unknown" : null , "schema_version" : 3 ,`},
		{"schema-after-formula-bound", lateOrdered},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "overlimit")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			frame := append([]byte(tc.prefix), bytes.Repeat([]byte{' '}, session.MaxEncodedRecordBytes()+1-len(tc.prefix))...)
			frame = append(frame, '\n')
			path := filepath.Join(dir, "raw.jsonl")
			if err := os.WriteFile(path, frame, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := session.NewStore(root, session.WithSessionID("overlimit")); err == nil || !strings.Contains(err.Error(), "V3 frame exceeds encoded limit") {
				t.Fatalf("store recovery error=%v", err)
			}
			if err := session.StreamRecords(path, "overlimit", func(*session.Record) error { return nil }); err == nil || !strings.Contains(err.Error(), "V3 frame exceeds encoded limit") {
				t.Fatalf("offline reader error=%v", err)
			}
		})
	}
}

func TestStoreAppendClassifiesEveryTopLevelNonObjectWithoutRejectingRaw(t *testing.T) {
	for _, raw := range []string{"null", "true", "1e2", `"text"`, "[]"} {
		t.Run(raw, func(t *testing.T) {
			store, err := session.NewStore(t.TempDir(), session.WithSessionID("non-object"))
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Append([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if record.ProjectionResult != session.ProjectionConsumedNoOutput || record.ProjectionCode != "gsi_projection_non_object" || record.ProjectionReason != "top_level_non_object" {
				t.Fatalf("classification=%#v", record)
			}
		})
	}
}

func TestStoreRecoveryRejectsNoncanonicalOrCorruptV3(t *testing.T) {
	validBody := []byte(`{"ok":true}`)
	valid := rawV3Frame("bad-v3", 1, "2026-08-14T00:20:26Z", validBody)
	invalidBody := rawV3Frame("bad-v3", 1, "2026-08-14T00:20:26Z", []byte("{"))
	for _, tc := range []struct {
		name string
		line string
	}{
		{"noncanonical-time", strings.Replace(valid, "00:20:26Z", "00:20:26.0Z", 1)},
		{"wrong-length", strings.Replace(valid, `"raw_byte_length":11`, `"raw_byte_length":12`, 1)},
		{"wrong-hash", valid[:len(valid)-66] + strings.Repeat("0", 64) + `"}`},
		{"wrong-encoding", strings.Replace(valid, "base64_std", "base64url", 1)},
		{"noncanonical-base64", strings.Replace(valid, base64.StdEncoding.EncodeToString(validBody), strings.TrimRight(base64.StdEncoding.EncodeToString(validBody), "="), 1)},
		{"invalid-body", invalidBody},
		{"unknown-member", strings.Replace(valid, `"raw_payload_sha256"`, `"unknown":1,"raw_payload_sha256"`, 1)},
		{"duplicate-member", strings.Replace(valid, `"source":"gsi"`, `"source":"gsi","source":"gsi"`, 1)},
		{"out-of-order", strings.Replace(valid, `"source":"gsi","raw_encoding":"base64_std"`, `"raw_encoding":"base64_std","source":"gsi"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := root + "/bad-v3"
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dir+"/raw.jsonl", []byte(tc.line+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := session.NewStore(root, session.WithSessionID("bad-v3")); err == nil {
				t.Fatal("corrupt V3 accepted")
			}
		})
	}
}

func TestV3CanonicalTimeAndJSONUnicodeEdges(t *testing.T) {
	for _, stamp := range []string{"2026-08-14T00:20:26Z", "2026-08-14T00:20:26.000000001Z", "2026-08-14T00:20:26.1234Z"} {
		if _, err := session.DecodeRecordV3([]byte(rawV3Frame("time", 1, stamp, []byte(`{"n":1e+9}`))), "time", 1); err != nil {
			t.Fatalf("canonical time %s: %v", stamp, err)
		}
	}
	for _, stamp := range []string{"2026-08-14T00:20:26+00:00", "2026-08-14t00:20:26Z", "2026-08-14T00:20:26.0Z", "2026-08-14T00:20:26.123400000Z"} {
		if _, err := session.DecodeRecordV3([]byte(rawV3Frame("time", 1, stamp, []byte(`{}`))), "time", 1); err == nil {
			t.Fatalf("accepted noncanonical time %s", stamp)
		}
	}
	malformed := []byte{'{', '"', 'p', 'r', 'o', 'v', 'i', 'd', 'e', 'r', '"', ':', '{', '"', 'n', 'a', 'm', 'e', '"', ':', '"', 0xff, '"', '}', '}'}
	store, _ := session.NewStore(t.TempDir(), session.WithSessionID("unicode"))
	r, err := store.Append(malformed)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Payload.(map[string]any)["provider"].(map[string]any)["name"]; got != "�" {
		t.Fatalf("malformed UTF-8 replacement=%q", got)
	}
	r, err = store.Append([]byte(`{"provider":{"name":"\ud800"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Payload.(map[string]any)["provider"].(map[string]any)["name"]; got != "�" {
		t.Fatalf("surrogate replacement=%q", got)
	}
}

func rawV3Frame(sessionID string, sequence uint64, received string, raw []byte) string {
	h := sha256.Sum256(raw)
	return fmt.Sprintf(`{"schema_version":3,"session_id":%q,"sequence":%d,"received_at":%q,"source":"gsi","raw_encoding":"base64_std","raw_byte_length":%d,"raw_base64":%q,"raw_payload_sha256":%q}`, sessionID, sequence, received, len(raw), base64.StdEncoding.EncodeToString(raw), hex.EncodeToString(h[:]))
}

func TestStoreRefusesToResumeLegacySessionForV3Append(t *testing.T) {
	root := t.TempDir()
	dir := root + "/legacy"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := []byte(`{"schema_version":2,"session_id":"legacy","sequence":1,"received_at":"2026-08-05T12:00:00Z","source":"gsi","payload":{},"raw":{}}` + "\n")
	if err := os.WriteFile(dir+"/raw.jsonl", line, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := session.NewStore(root, session.WithSessionID("legacy")); err == nil {
		t.Fatal("legacy session resumed for V3 append")
	}
}

func TestStoreEnforcesV3SessionIDGrammar(t *testing.T) {
	for _, id := range []string{".", "..", "has space", "slash/name", "é", strings.Repeat("a", 129)} {
		if _, err := session.NewStore(t.TempDir(), session.WithSessionID(id)); err == nil {
			t.Fatalf("accepted session id %q", id)
		}
	}
	if _, err := session.NewStore(t.TempDir(), session.WithSessionID("A-z_09.ok-1")); err != nil {
		t.Fatalf("rejected valid id: %v", err)
	}
}

func stringEqualBytes(got json.RawMessage, want []byte) bool { return string(got) == string(want) }

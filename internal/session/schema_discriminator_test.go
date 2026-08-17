package session

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestSchemaDiscriminatorUsesEncodingJSONMemberSemantics(t *testing.T) {
	v1 := `"received_at":"2026-08-14T00:00:00Z","payload":{},"raw":{}`
	v2 := `"session_id":"s","sequence":1,"received_at":"2026-08-14T00:00:00Z","source":"gsi","payload":{},"raw":{}`
	for _, tc := range []struct {
		name    string
		frame   string
		version int
	}{
		{"v1-missing", `{` + v1 + `}` + "\n", 1},
		{"literal-v2", `{"schema_version":2,` + v2 + `}` + "\n", 2},
		{"escaped-v3", `{"schema\u005fversion":3}` + "\n", 3},
		{"duplicate-last-v3", `{"schema_version":2,"schema_version":3}` + "\n", 3},
		{"duplicate-last-v2", `{"schema_version":3,"schema_version":2,` + v2 + `}` + "\n", 2},
		{"mixed-literal-escaped-last-v3", `{"schema_version":2,"schema\u005fversion":3}` + "\n", 3},
		{"mixed-escaped-literal-last-v2", `{"schema\u005fversion":3,"schema_version":2,` + v2 + `}` + "\n", 2},
		{"nested-decoy", `{"nested":{"schema_version":2},"schema\u005fversion":3}` + "\n", 3},
		{"malformed-escape-fails-closed", `{"schema\u00zzversion":2}` + "\n", 3},
		{"null-fails-closed", `{"schema_version":null}` + "\n", 3},
		{"noncanonical-number-fails-closed", `{"schema_version":3.0}` + "\n", 3},
		{"non-object-fails-closed", `[]` + "\n", 3},
		{"missing-version-unresolved", `{}` + "\n", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version, err := detectSchemaVersion(bufio.NewReader(strings.NewReader(tc.frame)))
			if err != nil {
				t.Fatal(err)
			}
			if version != tc.version {
				t.Fatalf("version=%d want=%d", version, tc.version)
			}
		})
	}
}

func TestSchemaDiscriminatorPreservesNextJSONLRecord(t *testing.T) {
	v1 := `{"received_at":"2026-08-14T00:00:00Z","payload":{},"raw":{}}`
	v2 := `{"schema_version":2,"session_id":"continuation","sequence":1,"received_at":"2026-08-14T00:00:00Z","source":"gsi","payload":{},"raw":{}}`
	const next = `{"distinct_second_record":true}` + "\n"
	for _, tc := range []struct {
		name, first string
		version     int
	}{
		{"v1-decoder-prefetch", v1, 1},
		{"v2-decoder-prefetch", v2, 2},
		{"v1-json-whitespace", v1 + " \t\r", 1},
		{"v2-json-whitespace", v2 + " \t\r", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := bufio.NewReaderSize(strings.NewReader(tc.first+"\n"+next), 64*1024)
			version, err := detectSchemaVersion(reader)
			if err != nil {
				t.Fatal(err)
			}
			if version != tc.version {
				t.Fatalf("version=%d want=%d", version, tc.version)
			}
			remainder, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if string(remainder) != next {
				t.Fatalf("remainder=%q want=%q", remainder, next)
			}
		})
	}
}

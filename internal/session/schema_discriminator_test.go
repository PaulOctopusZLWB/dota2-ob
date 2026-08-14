package session

import (
	"bufio"
	"strings"
	"testing"
)

func TestSchemaDiscriminatorUsesEncodingJSONMemberSemantics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		frame   string
		version int
	}{
		{"v1-missing", `{"received_at":"2026-08-14T00:00:00Z"}` + "\n", 1},
		{"literal-v2", `{"schema_version":2}` + "\n", 2},
		{"escaped-v3", `{"schema\u005fversion":3}` + "\n", 3},
		{"duplicate-last-v3", `{"schema_version":2,"schema_version":3}` + "\n", 3},
		{"duplicate-last-v2", `{"schema_version":3,"schema_version":2}` + "\n", 2},
		{"mixed-literal-escaped-last-v3", `{"schema_version":2,"schema\u005fversion":3}` + "\n", 3},
		{"mixed-escaped-literal-last-v2", `{"schema\u005fversion":3,"schema_version":2}` + "\n", 2},
		{"nested-decoy", `{"nested":{"schema_version":2},"schema\u005fversion":3}` + "\n", 3},
		{"malformed-escape-fails-closed", `{"schema\u00zzversion":2}` + "\n", 3},
		{"null-fails-closed", `{"schema_version":null}` + "\n", 3},
		{"noncanonical-number-fails-closed", `{"schema_version":3.0}` + "\n", 3},
		{"non-object-fails-closed", `[]` + "\n", 3},
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

package preflight_test

import (
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/preflight"
)

func TestNormalizeListenAddressAllowsOnlyExplicitLoopback(t *testing.T) {
	tests := []struct {
		input, want string
		ok          bool
	}{
		{"127.0.0.1:43210", "127.0.0.1:43210", true},
		{"localhost:43210", "127.0.0.1:43210", true},
		{"[::1]:43210", "[::1]:43210", true},
		{"0.0.0.0:43210", "", false}, {"[::]:43210", "", false},
		{":43210", "", false}, {"10.0.0.1:43210", "", false},
		{"example.com:43210", "", false}, {"127.0.0.1:0", "", false},
		{"127.0.0.1", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := preflight.NormalizeListenAddress(tc.input)
			if tc.ok && (err != nil || got != tc.want) {
				t.Fatalf("got %q err=%v, want %q", got, err, tc.want)
			}
			if !tc.ok && err == nil {
				t.Fatalf("got %q, want error", got)
			}
		})
	}
}

func TestValidateGSIConfigURIRequiresExactEndpoint(t *testing.T) {
	valid := `"Dota 2 Integration Configuration" { "uri" "http://localhost:43210/gsi" }`
	if err := preflight.ValidateGSIConfig([]byte(valid), "127.0.0.1:43210"); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	for _, uri := range []string{
		"https://127.0.0.1:43210/gsi", "http://127.0.0.1:43211/gsi", "http://127.0.0.1:43210/gsi/",
		"http://127.0.0.1:43210/gsi?q=1", "http://user@127.0.0.1:43210/gsi", "http://[::1]:43210/gsi",
	} {
		config := []byte(`"cfg" { "uri" "` + uri + `" }`)
		if err := preflight.ValidateGSIConfig(config, "127.0.0.1:43210"); err == nil {
			t.Fatalf("URI %q accepted", uri)
		}
	}
}

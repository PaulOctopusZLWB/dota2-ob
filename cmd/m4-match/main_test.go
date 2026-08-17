package main

import "testing"

func TestPurposeAndClassFlagsAreRequiredClosedAndUnique(t *testing.T) {
	for name, args := range map[string][]string{
		"missing":           {"preflight", "--data-root", "/var/tmp/unused"},
		"unknown purpose":   {"preflight", "--purpose", "unknown", "--match-class", "public_match", "--data-root", "/var/tmp/unused"},
		"illegal pair":      {"preflight", "--purpose", "p4_acceptance", "--match-class", "public_match", "--data-root", "/var/tmp/unused"},
		"duplicate purpose": {"preflight", "--purpose", "p4_acceptance", "--purpose", "p4_acceptance", "--match-class", "ti", "--data-root", "/var/tmp/unused"},
		"duplicate class":   {"verify", "--purpose", "p4_acceptance", "--match-class", "ti", "--match-class", "ti", "--data-root", "/var/tmp/unused"},
	} {
		t.Run(name, func(t *testing.T) {
			if code := run(args); code != 2 {
				t.Fatalf("exit=%d want=2", code)
			}
		})
	}
}

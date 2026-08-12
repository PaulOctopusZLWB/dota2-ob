package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateMatchID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"8941092540", true},
		{"1", true},
		{"", false},
		{"abc", false},
		{"8941abc", false},
		{"123456789012345678901", false}, // too long
		{"../escape", false},
		{"123/456", false},
	}
	for _, c := range cases {
		err := validateMatchID(c.id)
		if (err == nil) != c.want {
			t.Errorf("validateMatchID(%q) err=%v, want ok=%v", c.id, err, c.want)
		}
	}
}

func TestValidateReplayURLAcceptlist(t *testing.T) {
	good := []string{
		"http://replay273.valve.net/570/8941092540_1595018738.dem.bz2",
		"https://replay1.valve.net/570/123_456.dem.bz2",
	}
	for _, u := range good {
		if err := validateReplayURL(u); err != nil {
			t.Errorf("validateReplayURL(%q) unexpected err: %v", u, err)
		}
	}
	bad := []string{
		"", // no url
		"ftp://replay273.valve.net/x.dem.bz2",                              // bad scheme
		"http://replay273.valve.net.evil.com/x.dem.bz2",                    // not valve suffix
		"http://replay.valve.net/x.dem.bz2",                                // missing digits
		"http://replayabc.valve.net/x.dem.bz2",                            // non-numeric suffix
		"http://evil.com/570/8941092540_1595018738.dem.bz2",               // untrusted host
		"http://replay273.valve.net",                                       // ok host, missing path (host test only)
		"http://replay273.evil.net/x.dem.bz2",                             // wrong domain
	}
	for _, u := range bad {
		// "" and the missing-path case are treated as "no replay_url"/ok-host
		// respectively; assert the host/scheme rejects the clearly bad ones.
		if u == "" {
			if err := validateReplayURL(u); err == nil {
				t.Errorf("validateReplayURL(%q) expected error", u)
			}
			continue
		}
		err := validateReplayURL(u)
		if u == "http://replay273.valve.net" {
			// host is valid; only scheme+host are checked here, path not required
			if err != nil {
				t.Errorf("validateReplayURL(%q) unexpected err: %v", u, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("validateReplayURL(%q) expected rejection, got nil", u)
		}
	}
}

func TestLooksLikeValveReplayHost(t *testing.T) {
	good := []string{"replay273.valve.net", "replay1.valve.net", "replay999999.valve.net"}
	for _, h := range good {
		if !looksLikeValveReplayHost(h) {
			t.Errorf("looksLikeValveReplayHost(%q) want true", h)
		}
	}
	bad := []string{"replay.valve.net", "replayabc.valve.net", "replay273.evil.net", "evil.valve.net", "replay273.valve.net.evil.com", "replay273valve.net"}
	for _, h := range bad {
		if looksLikeValveReplayHost(h) {
			t.Errorf("looksLikeValveReplayHost(%q) want false", h)
		}
	}
}

func TestValidateMagic(t *testing.T) {
	dir := t.TempDir()
	zstdFile := filepath.Join(dir, "x.bin")
	if err := os.WriteFile(zstdFile, append(append([]byte{}, zstdMagic...), 0xaa, 0xbb), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateMagic(zstdFile, zstdMagic); err != nil {
		t.Fatalf("zstd magic mismatch: %v", err)
	}
	demFile := filepath.Join(dir, "x.dem")
	demBytes := append(append([]byte{}, pbDEMS2Magic...), 0x01, 0x02)
	if err := os.WriteFile(demFile, demBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateMagic(demFile, pbDEMS2Magic); err != nil {
		t.Fatalf("pbDEMS2 magic mismatch: %v", err)
	}
	// wrong magic on the zstd file (expecting demo magic) must fail
	if err := validateMagic(zstdFile, pbDEMS2Magic); err == nil {
		t.Fatalf("expected magic mismatch on zstd file vs pbDEMS2")
	}
	// missing file must fail
	if err := validateMagic(filepath.Join(dir, "nope"), zstdMagic); err == nil {
		t.Fatalf("expected error for missing file")
	}
}
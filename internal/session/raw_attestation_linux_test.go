//go:build linux

package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAttestedRawExactBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	line := bytes.Repeat([]byte("x"), 64)
	if err := os.WriteFile(path, append(append([]byte{}, line...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []byte
	summary, err := ReadAttestedRaw(path, 65, 64, func(frame []byte, sequence uint64) error {
		got = append([]byte(nil), frame...)
		if sequence != 1 {
			t.Fatalf("sequence=%d", sequence)
		}
		return nil
	})
	if err != nil || summary.Bytes != 65 || summary.Lines != 1 || !bytes.Equal(got, line) {
		t.Fatalf("exact boundary rejected: summary=%+v err=%v len=%d", summary, err, len(got))
	}
}

func TestReadAttestedRawRejectsTotalLineAndTermination(t *testing.T) {
	for _, tc := range []struct {
		name      string
		content   []byte
		total     int64
		line      int64
		wantError string
	}{
		{"total-cap-plus-one", []byte("a\nb\n"), 3, 2, "total limit"},
		{"line-cap-plus-one", []byte("abc\n"), 16, 2, "line limit"},
		{"unterminated", []byte("abc"), 16, 8, "unterminated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			if err := os.WriteFile(path, tc.content, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadAttestedRaw(path, tc.total, tc.line, func([]byte, uint64) error { return nil })
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("wanted %q, got %v", tc.wantError, err)
			}
		})
	}
}

func TestReadAttestedRawRejectsConcurrentGrowthAndReplacement(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "growth"
		if replacement {
			name = "replacement"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "raw.jsonl")
			if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadAttestedRaw(path, 64, 16, func(_ []byte, sequence uint64) error {
				if sequence != 1 {
					return nil
				}
				if replacement {
					next := filepath.Join(dir, "replacement")
					if writeErr := os.WriteFile(next, []byte("one\ntwo\n"), 0o600); writeErr != nil {
						return writeErr
					}
					return os.Rename(next, path)
				}
				file, openErr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if openErr != nil {
					return openErr
				}
				_, writeErr := file.WriteString("three\n")
				return errorsJoin(writeErr, file.Close())
			})
			if err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("mutation admitted: %v", err)
			}
		})
	}
}

func errorsJoin(first, second error) error {
	if first != nil {
		return first
	}
	return second
}

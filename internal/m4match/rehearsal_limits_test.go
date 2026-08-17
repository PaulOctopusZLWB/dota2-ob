package m4match

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func TestLiteralRehearsalRawTotalBoundaryAndCapPlusOne(t *testing.T) {
	for _, extra := range int64Slice(0, 1) {
		name := "exact"
		if extra == 1 {
			name = "cap-plus-one"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			writeTerminatedBytes(t, path, MaxRehearsalRawBytes+extra, 64<<10)
			summary, err := session.ReadAttestedRaw(path, MaxRehearsalRawBytes, MaxRehearsalRawLineBytes, func([]byte, uint64) error { return nil })
			if extra == 0 {
				if err != nil || summary.Bytes != MaxRehearsalRawBytes {
					t.Fatalf("literal exact total rejected: %+v %v", summary, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "total limit") {
				t.Fatalf("literal total cap+1 admitted: %v", err)
			}
		})
	}
}

func TestLiteralRehearsalRawLineBoundaryAndCapPlusOne(t *testing.T) {
	for _, extra := range int64Slice(0, 1) {
		name := "exact"
		if extra == 1 {
			name = "cap-plus-one"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			writeTerminatedBytes(t, path, MaxRehearsalRawLineBytes+extra+1, MaxRehearsalRawLineBytes+extra+1)
			summary, err := session.ReadAttestedRaw(path, MaxRehearsalRawBytes, MaxRehearsalRawLineBytes, func([]byte, uint64) error { return nil })
			if extra == 0 {
				if err != nil || summary.Lines != 1 || summary.Bytes != MaxRehearsalRawLineBytes+1 {
					t.Fatalf("literal exact line rejected: %+v %v", summary, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "line limit") {
				t.Fatalf("literal line cap+1 admitted: %v", err)
			}
		})
	}
}

func writeTerminatedBytes(t *testing.T, path string, total, maximumLineBytes int64) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, maximumLineBytes)
	for index := range buffer {
		buffer[index] = 'x'
	}
	written := int64(0)
	for written < total {
		lineBytes := maximumLineBytes
		if remaining := total - written; remaining < lineBytes {
			lineBytes = remaining
		}
		if lineBytes == 1 {
			buffer[0] = '\n'
		} else {
			buffer[lineBytes-1] = '\n'
		}
		if _, err = file.Write(buffer[:lineBytes]); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		buffer[lineBytes-1] = 'x'
		written += lineBytes
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
}

func int64Slice(values ...int64) []int64 { return values }

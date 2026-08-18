//go:build linux

package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAttestedRawLiteralLineAndTotalBoundaries(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root, WithSessionID("attested-boundary"), WithClock(func() time.Time { return time.Unix(1, 0).UTC() }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`{"provider":{"name":"Dota 2"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(store.RawPath())
	if err != nil {
		t.Fatal(err)
	}
	lineLimit, totalLimit := uint64(len(payload)), uint64(len(payload))
	accepted, err := ReadAttestedRawV1(store.RawPath(), store.SessionID(), totalLimit, lineLimit, 1)
	if err != nil || len(accepted.Records) != 1 || !accepted.Receipt.FullContentHash || accepted.Records[0].EncodedLineBytes != lineLimit {
		t.Fatalf("exact boundary=%#v err=%v", accepted, err)
	}
	lineRejected, err := ReadAttestedRawV1(store.RawPath(), store.SessionID(), totalLimit, lineLimit-1, 1)
	if err == nil || lineRejected.Receipt.Failure.Primary != "line_limit_exceeded" || lineRejected.Receipt.BytesRead == 0 || lineRejected.Receipt.ReadPrefixSHA256 == strings.Repeat("0", 64) || lineRejected.Receipt.PreDescriptor.Inode == 0 || lineRejected.Receipt.PostDescriptor.Inode == 0 {
		t.Fatalf("line cap receipt=%#v err=%v", lineRejected, err)
	}
	totalRejected, err := ReadAttestedRawV1(store.RawPath(), store.SessionID(), totalLimit-1, lineLimit, 1)
	if err == nil || totalRejected.Receipt.Failure.Primary != "total_limit_exceeded" || totalRejected.Receipt.FullContentHash {
		t.Fatalf("total cap receipt=%#v err=%v", totalRejected, err)
	}
}

func TestAttestedRawRejectsCRLFAndUnterminatedWithIdentityReceipt(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root, WithSessionID("attested-ending"), WithClock(func() time.Time { return time.Unix(1, 0).UTC() }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`{"map":{"clock_time":-1}}`)); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	original, _ := os.ReadFile(store.RawPath())
	for name, data := range map[string][]byte{
		"unterminated": bytes.TrimSuffix(original, []byte{'\n'}),
		"crlf":         append(bytes.TrimSuffix(original, []byte{'\n'}), '\r', '\n'),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := ReadAttestedRawV1(path, store.SessionID(), uint64(len(data)), uint64(len(data)), 1)
			if err == nil || result.Receipt.BytesRead != uint64(len(data)) || result.Receipt.ReadPrefixSHA256 == "" || result.Receipt.PreDescriptor.Inode == 0 || result.Receipt.PostDescriptor.Inode == 0 || result.Receipt.FullContentHash {
				t.Fatalf("receipt=%#v err=%v", result.Receipt, err)
			}
			if name == "unterminated" && result.Receipt.Failure.Primary != "unterminated_line" {
				t.Fatalf("primary=%s", result.Receipt.Failure.Primary)
			}
			if name == "crlf" && result.Receipt.Failure.Primary != "noncanonical_line_ending" {
				t.Fatalf("primary=%s", result.Receipt.Failure.Primary)
			}
		})
	}
}

func TestAttestedRawAbsoluteRehearsalLineCapIsLiteral(t *testing.T) {
	const capBytes = 13_985_113
	for _, size := range []int{capBytes, capBytes + 1} {
		path := filepath.Join(t.TempDir(), "raw.jsonl")
		payload := bytes.Repeat([]byte{' '}, size)
		payload[size-1] = '\n'
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		result, _ := ReadAttestedRawV1(path, "literal-boundary", 64<<20, capBytes, 1)
		if size == capBytes && result.Receipt.Failure.Primary == "line_limit_exceeded" {
			t.Fatal("exact LF-inclusive cap rejected by line bound")
		}
		if size == capBytes+1 && result.Receipt.Failure.Primary != "line_limit_exceeded" {
			t.Fatalf("cap+1 primary=%s", result.Receipt.Failure.Primary)
		}
	}
}

func TestAttestedRawReconcilesConcurrentGrowthAfterParserFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	if err := os.WriteFile(path, []byte("unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := rawAdmissionPostReadHook
	rawAdmissionPostReadHook = func(path string) {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = file.WriteString("-growth")
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { rawAdmissionPostReadHook = previous })
	result, err := ReadAttestedRawV1(path, "growth", 64, 64, 1)
	if err == nil || result.Receipt.Failure.Primary != "unterminated_line" || !strings.Contains(result.Receipt.Failure.ConcurrentChange, "descriptor_changed") || result.Receipt.FullContentHash {
		t.Fatalf("growth receipt=%#v err=%v", result.Receipt, err)
	}
}

func TestAttestedRawReconcilesPathReplacementAfterRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	if err := os.WriteFile(path, []byte("unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := rawAdmissionPostReadHook
	rawAdmissionPostReadHook = func(path string) {
		replacement := path + ".replacement"
		if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { rawAdmissionPostReadHook = previous })
	result, err := ReadAttestedRawV1(path, "replacement", 64, 64, 1)
	if err == nil || !strings.Contains(result.Receipt.Failure.ConcurrentChange, "path_replaced_or_resized") || result.Receipt.PreDescriptor.Inode == result.Receipt.PostPath.Inode || result.Receipt.FullContentHash {
		t.Fatalf("replacement receipt=%#v err=%v", result.Receipt, err)
	}
}

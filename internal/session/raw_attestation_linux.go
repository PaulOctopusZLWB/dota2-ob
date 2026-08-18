//go:build linux

package session

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

type RawDescriptorIdentityV1 struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
}

type RawAdmissionFailureV1 struct {
	Primary          string `json:"primary"`
	ConcurrentChange string `json:"concurrent_change,omitempty"`
}

type RawAdmissionReceiptV1 struct {
	SchemaVersion    string                  `json:"schema_version"`
	Path             string                  `json:"path"`
	PreDescriptor    RawDescriptorIdentityV1 `json:"pre_descriptor"`
	PostDescriptor   RawDescriptorIdentityV1 `json:"post_descriptor"`
	PrePath          RawDescriptorIdentityV1 `json:"pre_path"`
	PostPath         RawDescriptorIdentityV1 `json:"post_path"`
	BytesRead        uint64                  `json:"bytes_read"`
	CompleteLines    uint64                  `json:"complete_lines"`
	AcceptedRecords  uint64                  `json:"accepted_records"`
	ReadPrefixSHA256 string                  `json:"read_prefix_sha256"`
	FullContentHash  bool                    `json:"full_content_hash"`
	ContentGuardOK   bool                    `json:"content_guard_ok"`
	Failure          RawAdmissionFailureV1   `json:"failure"`
}

type RawRecordAttestationV1 struct {
	Sequence         uint64 `json:"sequence"`
	EncodedLineBytes uint64 `json:"encoded_line_bytes"`
	RawRecordSHA256  string `json:"raw_record_sha256"`
	RawPayloadSHA256 string `json:"raw_payload_sha256"`
}

type AttestedRawV1 struct {
	Receipt RawAdmissionReceiptV1    `json:"receipt"`
	Records []RawRecordAttestationV1 `json:"records"`
	Decoded []*Record                `json:"-"`
}

type countingReader struct {
	r io.Reader
	n uint64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += uint64(n)
	return n, err
}

// ReadAttestedRawV1 performs one descriptor-bound streaming pass. Limits are
// checked on complete encoded lines (including LF) before growing the bounded
// line buffer. CRLF and unterminated input are non-canonical.
func ReadAttestedRawV1(path, sessionID string, maxTotalBytes, maxLineBytes uint64, maxRecords int) (result AttestedRawV1, retErr error) {
	return ReadAttestedRawV1WithGuard(path, sessionID, maxTotalBytes, maxLineBytes, maxRecords, nil)
}

type RawRecordAdmissionGuardV1 func(*Record, string) error

// rawAdmissionPostReadHook is a package-private deterministic seam for the
// descriptor/path reconciliation tests. Production leaves it as a no-op.
var rawAdmissionPostReadHook = func(string) {}

// ReadAttestedRawV1WithGuard runs the guard after exact frame validation and
// before the sequence is appended to the admitted population.
func ReadAttestedRawV1WithGuard(path, sessionID string, maxTotalBytes, maxLineBytes uint64, maxRecords int, guard RawRecordAdmissionGuardV1) (result AttestedRawV1, retErr error) {
	result.Receipt.SchemaVersion = "raw_admission_receipt.v1"
	result.Receipt.Path = path
	if maxTotalBytes == 0 || maxLineBytes < 2 || maxRecords <= 0 {
		return result, errors.New("invalid raw admission limits")
	}
	file, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer file.Close()
	preFD, err := descriptorIdentity(file)
	if err != nil || preFD.Mode&uint32(syscall.S_IFMT) != uint32(syscall.S_IFREG) {
		return result, errors.New("raw descriptor is not a regular file")
	}
	prePath, err := pathIdentity(path)
	if err != nil || prePath.Device != preFD.Device || prePath.Inode != preFD.Inode {
		return result, errors.New("raw path changed during descriptor open")
	}
	result.Receipt.PreDescriptor, result.Receipt.PrePath = preFD, prePath
	initialGuardOK, initialGuardAvailable := RawContentGuardState(path)
	result.Receipt.ContentGuardOK = initialGuardOK

	prefixHash := sha256.New()
	limited := &io.LimitedReader{R: file, N: int64(maxTotalBytes + 1)}
	counter := &countingReader{r: io.TeeReader(limited, prefixHash)}
	reader := bufio.NewReaderSize(counter, 64<<10)
	expectedSequence := uint64(1)
	primary := ""
	for primary == "" {
		line := make([]byte, 0, minInt(int(maxLineBytes), 64<<10))
		for {
			chunk, readErr := reader.ReadSlice('\n')
			if counter.n > maxTotalBytes {
				primary = "total_limit_exceeded"
				break
			}
			if uint64(len(line))+uint64(len(chunk)) > maxLineBytes {
				primary = "line_limit_exceeded"
				break
			}
			line = append(line, chunk...)
			if readErr == nil {
				break
			}
			if errors.Is(readErr, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(readErr, io.EOF) {
				if len(line) == 0 {
					primary = "eof"
				} else {
					primary = "unterminated_line"
				}
				break
			}
			primary = "read_failure"
			break
		}
		if primary != "" {
			break
		}
		result.Receipt.CompleteLines++
		if len(line) < 2 || line[len(line)-1] != '\n' || line[len(line)-2] == '\r' {
			primary = "noncanonical_line_ending"
			break
		}
		if len(result.Records) >= maxRecords {
			primary = "record_limit_exceeded"
			break
		}
		payload := line[:len(line)-1]
		record, decodeErr := DecodeRecordV3(payload, sessionID, expectedSequence)
		if decodeErr != nil {
			primary = "record_decode_failure"
			break
		}
		digest := sha256.Sum256(line)
		rawDigest := sha256.Sum256(record.Raw)
		recordSHA256 := hex.EncodeToString(digest[:])
		if guard != nil {
			if guardErr := guard(record, recordSHA256); guardErr != nil {
				primary = "record_admission_guard_failure"
				break
			}
		}
		result.Records = append(result.Records, RawRecordAttestationV1{
			Sequence: expectedSequence, EncodedLineBytes: uint64(len(line)), RawRecordSHA256: recordSHA256, RawPayloadSHA256: hex.EncodeToString(rawDigest[:]),
		})
		result.Decoded = append(result.Decoded, record)
		result.Receipt.AcceptedRecords++
		expectedSequence++
	}

	rawAdmissionPostReadHook(path)
	postFD, fdErr := descriptorIdentity(file)
	postPath, pathErr := pathIdentity(path)
	result.Receipt.PostDescriptor, result.Receipt.PostPath = postFD, postPath
	result.Receipt.BytesRead = counter.n
	result.Receipt.ReadPrefixSHA256 = hex.EncodeToString(prefixHash.Sum(nil))
	concurrent := ""
	if fdErr != nil {
		concurrent = "post_descriptor_unavailable"
	} else if preFD.Device != postFD.Device || preFD.Inode != postFD.Inode || preFD.Mode != postFD.Mode || preFD.Size != postFD.Size {
		concurrent = "descriptor_changed"
	}
	if pathErr != nil {
		concurrent = joinFailure(concurrent, "post_path_unavailable")
	} else if postPath.Device != postFD.Device || postPath.Inode != postFD.Inode || prePath.Device != postPath.Device || prePath.Inode != postPath.Inode || prePath.Size != postPath.Size {
		concurrent = joinFailure(concurrent, "path_replaced_or_resized")
	}
	guardOK, guardAvailable := RawContentGuardState(path)
	if !initialGuardAvailable {
		concurrent = joinFailure(concurrent, "content_guard_unavailable_at_open")
	} else if !initialGuardOK {
		concurrent = joinFailure(concurrent, "content_guard_mismatch_at_open")
	}
	if !guardAvailable {
		concurrent = joinFailure(concurrent, "content_guard_unavailable")
	} else if !guardOK {
		concurrent = joinFailure(concurrent, "content_guard_mismatch")
	}
	result.Receipt.ContentGuardOK = guardOK
	if primary == "eof" {
		primary = ""
	}
	result.Receipt.FullContentHash = primary == "" && concurrent == "" && postFD.Size >= 0 && uint64(postFD.Size) == counter.n
	result.Receipt.Failure = RawAdmissionFailureV1{Primary: primary, ConcurrentChange: concurrent}
	if primary != "" || concurrent != "" {
		return result, fmt.Errorf("raw admission failed: primary=%s concurrent=%s", primary, concurrent)
	}
	return result, nil
}

func descriptorIdentity(file *os.File) (RawDescriptorIdentityV1, error) {
	info, err := file.Stat()
	if err != nil {
		return RawDescriptorIdentityV1{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return RawDescriptorIdentityV1{}, errors.New("raw descriptor stat unavailable")
	}
	return RawDescriptorIdentityV1{Device: uint64(stat.Dev), Inode: stat.Ino, Mode: stat.Mode, Size: info.Size()}, nil
}

func pathIdentity(path string) (RawDescriptorIdentityV1, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return RawDescriptorIdentityV1{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSymlink != 0 {
		return RawDescriptorIdentityV1{}, errors.New("raw path stat unavailable")
	}
	return RawDescriptorIdentityV1{Device: uint64(stat.Dev), Inode: stat.Ino, Mode: stat.Mode, Size: info.Size()}, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func joinFailure(a, b string) string {
	if a == "" {
		return b
	}
	return a + "+" + b
}

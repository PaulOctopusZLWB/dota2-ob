package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
)

const rawAuthorityEntryBytes = 8 + sha256.Size + sha256.Size

func rawAuthorityPath(rawPath string) string { return rawPath + ".authority-v3" }

func advanceRawChain(chain [sha256.Size]byte, raw []byte) [sha256.Size]byte {
	rawHash := sha256.Sum256(raw)
	var joined [sha256.Size * 2]byte
	copy(joined[:sha256.Size], chain[:])
	copy(joined[sha256.Size:], rawHash[:])
	return sha256.Sum256(joined[:])
}

func rawAuthoritySummaryHash(c liveCursor) [sha256.Size]byte {
	value := struct {
		Sequence     uint64 `json:"sequence"`
		Active       bool   `json:"active"`
		Count        uint64 `json:"count"`
		LastSequence uint64 `json:"last_sequence"`
		Code         string `json:"code"`
		Reason       string `json:"reason"`
	}{c.Sequence, c.ProjectionRejectionActive, c.ProjectionRejectionCount, c.LastProjectionRejectionSequence, c.LastProjectionRejectionCode, c.LastProjectionRejectionReason}
	data, _ := json.Marshal(value)
	return sha256.Sum256(data)
}

func encodeRawAuthority(offset int64, chain, summary [sha256.Size]byte) []byte {
	entry := make([]byte, rawAuthorityEntryBytes)
	binary.BigEndian.PutUint64(entry[:8], uint64(offset))
	copy(entry[8:8+sha256.Size], chain[:])
	copy(entry[8+sha256.Size:], summary[:])
	return entry
}

func appendRawAuthority(rawPath string, sequence uint64, offset int64, chain, summary [sha256.Size]byte) error {
	path := rawAuthorityPath(rawPath)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	want := int64(sequence-1) * rawAuthorityEntryBytes
	info, err := file.Stat()
	if err != nil || info.Size() < want {
		return errors.New("raw authority index is stale")
	}
	encoded := encodeRawAuthority(offset, chain, summary)
	if info.Size() >= want+rawAuthorityEntryBytes {
		existing := make([]byte, rawAuthorityEntryBytes)
		if _, err := file.ReadAt(existing, want); err != nil || !bytes.Equal(existing, encoded) {
			return errors.New("raw authority entry mismatch")
		}
		return nil
	}
	if info.Size() != want {
		return errors.New("raw authority index has partial entry")
	}
	if _, err := file.Seek(want, io.SeekStart); err != nil {
		return err
	}
	return writeComplete(file, encoded)
}

func readRawAuthority(rawPath string, sequence uint64) (int64, [sha256.Size]byte, [sha256.Size]byte, bool) {
	var chain, summary [sha256.Size]byte
	if sequence == 0 {
		return 0, chain, summary, true
	}
	file, err := os.Open(rawAuthorityPath(rawPath))
	if err != nil {
		return 0, chain, summary, false
	}
	defer file.Close()
	entry := make([]byte, rawAuthorityEntryBytes)
	offset := int64(sequence-1) * rawAuthorityEntryBytes
	if _, err := file.ReadAt(entry, offset); err != nil {
		return 0, chain, summary, false
	}
	copy(chain[:], entry[8:8+sha256.Size])
	copy(summary[:], entry[8+sha256.Size:])
	return int64(binary.BigEndian.Uint64(entry[:8])), chain, summary, true
}

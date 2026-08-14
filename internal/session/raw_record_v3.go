package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
)

const maxEncodedRecordBytes = 4*((maxRawBodyBytes+2)/3) + 4096

// DecodeRecordV3 validates one complete, newline-free V3 frame and returns
// owned exact request bytes. Callers must release the record after processing.
func DecodeRecordV3(line []byte, sessionID string, sequence uint64) (*Record, error) {
	if len(line) == 0 || len(line) > maxEncodedRecordBytes {
		return nil, errors.New("V3 frame size invalid")
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	open, err := dec.Token()
	if err != nil || open != json.Delim('{') {
		return nil, errors.New("V3 frame is not an object")
	}
	keys := [...]string{"schema_version", "session_id", "sequence", "received_at", "source", "raw_encoding", "raw_byte_length", "raw_base64", "raw_payload_sha256"}
	values := make([]any, len(keys))
	for i, want := range keys {
		if !dec.More() {
			return nil, fmt.Errorf("missing V3 member %s", want)
		}
		key, err := dec.Token()
		if err != nil || key != want {
			return nil, fmt.Errorf("invalid V3 member order at %s", want)
		}
		value, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid V3 member %s", want)
		}
		values[i] = value
	}
	if dec.More() {
		return nil, errors.New("unknown or duplicate V3 member")
	}
	closeToken, err := dec.Token()
	if err != nil || closeToken != json.Delim('}') {
		return nil, errors.New("invalid V3 object close")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing V3 data")
	}
	if numberText(values[0]) != "3" {
		return nil, errors.New("invalid V3 schema_version")
	}
	sid, ok := values[1].(string)
	if !ok || sid != sessionID || !isSafeSessionID(sid) {
		return nil, errors.New("invalid V3 session_id")
	}
	seq, err := parseCanonicalUint(values[2])
	if err != nil || seq == 0 || seq != sequence {
		return nil, errors.New("invalid V3 sequence")
	}
	receivedText, ok := values[3].(string)
	if !ok {
		return nil, errors.New("invalid V3 received_at")
	}
	received, err := time.Parse(time.RFC3339Nano, receivedText)
	if err != nil || received.UTC().Format(time.RFC3339Nano) != receivedText {
		return nil, errors.New("noncanonical V3 received_at")
	}
	if values[4] != "gsi" || values[5] != "base64_std" {
		return nil, errors.New("invalid V3 source or encoding")
	}
	rawLength, err := parseCanonicalUint(values[6])
	if err != nil || rawLength > maxRawBodyBytes {
		return nil, errors.New("invalid V3 raw_byte_length")
	}
	encoded, ok := values[7].(string)
	if !ok || len(encoded) > base64.StdEncoding.EncodedLen(maxRawBodyBytes) {
		return nil, errors.New("invalid V3 raw_base64")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(raw) != encoded || uint64(len(raw)) != rawLength {
		return nil, errors.New("invalid V3 base64 or length")
	}
	hashText, ok := values[8].(string)
	if !ok || len(hashText) != 64 {
		return nil, errors.New("invalid V3 hash")
	}
	hashBytes, err := hex.DecodeString(hashText)
	if err != nil || hex.EncodeToString(hashBytes) != hashText {
		return nil, errors.New("noncanonical V3 hash")
	}
	sum := sha256.Sum256(raw)
	if !bytes.Equal(sum[:], hashBytes) {
		return nil, errors.New("V3 integrity mismatch")
	}
	payload, result, code, reason, err := decodeBoundedGSI(raw)
	if err != nil {
		return nil, err
	}
	record := &Record{SchemaVersion: 3, SessionID: sid, Sequence: seq, ReceivedAt: received, Source: "gsi", Payload: payload, Raw: raw, ProjectionResult: result, ProjectionCode: code, ProjectionReason: reason}
	return record, nil
}

func numberText(v any) string {
	if n, ok := v.(json.Number); ok {
		return n.String()
	}
	return ""
}
func parseCanonicalUint(v any) (uint64, error) {
	s := numberText(v)
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, errors.New("noncanonical uint")
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != s {
		return 0, errors.New("noncanonical uint")
	}
	return n, nil
}

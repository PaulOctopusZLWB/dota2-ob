package session

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	RawRecordSchemaV3Descriptor      = "raw-record-v3|schema_version,session_id,sequence,received_at,source,raw_encoding,raw_byte_length,raw_base64,raw_payload_sha256"
	RawRecordSchemaV3Identity        = "sha256:b46440704ac91e03d88abc28f89ff1fb3015f6c3fb3cfbe99182ffd923fe096c"
	RawRecordFramingV3Descriptor     = "raw-record-v3-framing|json-object|ordered-members|base64-std|lf-commit"
	RawRecordFramingV3Identity       = "sha256:e9b1fae191e03965811571dfe3077c5b9ae2d22779b8334de93a91c818e8b82f"
	RawPayloadSchemaV3Descriptor     = "gsi-raw-payload-v3|encoding-json-decoder-use-number|one-value|10485760"
	RawPayloadSchemaV3Identity       = "sha256:be2cf3ac041acbcdf9ddd982ebb56dfc093a101b1a36bf69e85fa68551faaec1"
	GSIProjectionMappingV3Descriptor = "gsi-projection-v3|provider,league,map,player,hero,items,abilities,buildings|final-last-wins|retained-values-only"
	GSIProjectionMappingV3Identity   = "sha256:723722566fad1f6b350238172d3ea3926098f3388368253d1c6d687b9a9671d4"
)

var captureLineageV3 = []byte(`{"schema_version":3,"raw_record_schema":"` + RawRecordSchemaV3Identity + `","framing":"` + RawRecordFramingV3Identity + `","raw_payload_schema":"` + RawPayloadSchemaV3Identity + `","projection_mapping":"` + GSIProjectionMappingV3Identity + `"}` + "\n")

func ensureCaptureLineageV3(sessionDir string, existingSequence uint64) error {
	path := filepath.Join(sessionDir, "capture_lineage_v3.json")
	data, err := os.ReadFile(path)
	if err == nil {
		if !bytes.Equal(data, captureLineageV3) {
			return errors.New("V3 capture lineage mismatch")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read V3 capture lineage: %w", err)
	}
	if existingSequence != 0 {
		return errors.New("existing raw session lacks V3 capture lineage")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create V3 capture lineage: %w", err)
	}
	writeErr := writeComplete(file, captureLineageV3)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write V3 capture lineage: %w", writeErr)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close V3 capture lineage: %w", closeErr)
	}
	return nil
}

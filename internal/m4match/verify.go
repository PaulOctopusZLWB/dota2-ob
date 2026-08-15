package m4match

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func Verify(ctx context.Context, root, repo, expect string) (Readiness, error) {
	_ = ctx
	abs, err := safeRoot(root, repo)
	if err != nil {
		return Readiness{}, err
	}
	readinessPath := filepath.Join(abs, "evidence/readiness.json")
	payload, err := os.ReadFile(readinessPath)
	if err != nil {
		return Readiness{}, err
	}
	var readiness Readiness
	if err := json.Unmarshal(payload, &readiness); err != nil {
		return Readiness{}, err
	}
	canonicalReadiness, err := canonical(readiness)
	if err != nil || !bytes.Equal(payload, canonicalReadiness) {
		return Readiness{}, errors.New("readiness result is not canonical")
	}
	if readiness.SchemaVersion != ReadinessSchemaVersion || readiness.Mode != expect || readiness.ClaimsP4 || readiness.HumanInstruction != HumanInstruction {
		return Readiness{}, errors.New("readiness contract mismatch")
	}
	indexPath := filepath.Join(abs, "evidence/canonical/evidence-index.json")
	indexPayload, err := os.ReadFile(indexPath)
	if err != nil {
		return Readiness{}, err
	}
	if payloadSHA(indexPayload) != readiness.EvidenceIndexSHA256 {
		return Readiness{}, errors.New("evidence index hash mismatch")
	}
	var evidence Evidence
	if err := json.Unmarshal(indexPayload, &evidence); err != nil {
		return Readiness{}, err
	}
	canonicalIndex, err := canonical(evidence)
	if err != nil || !bytes.Equal(indexPayload, canonicalIndex) {
		return Readiness{}, errors.New("evidence index is not canonical")
	}
	if evidence.SchemaVersion != SchemaVersion || evidence.Mode != expect || evidence.AcceptedBase != AcceptedFunctionalBase || evidence.AcceptedSpec != AcceptedP4Spec || evidence.ClaimsP4 || !evidence.NonResumable {
		return Readiness{}, errors.New("evidence identity or safety contract mismatch")
	}
	if evidence.FixtureSHA256 != CapturedScheduleSHA256 || evidence.GoldenSHA256 != ProductionGoldenSHA256 {
		return Readiness{}, errors.New("accepted synthetic evidence identity mismatch")
	}
	for _, artifact := range evidence.Artifacts {
		clean := filepath.Clean(filepath.FromSlash(artifact.Path))
		if artifact.Path == "" || filepath.IsAbs(clean) || clean == ".." || len(clean) >= 3 && clean[:3] == ".."+string(os.PathSeparator) {
			return Readiness{}, errors.New("prepared artifact path escapes evidence root")
		}
		path := filepath.Join(abs, clean)
		hash, size, err := fileSHA(path)
		if err != nil || hash != artifact.SHA256 || size != artifact.Bytes {
			return Readiness{}, fmt.Errorf("prepared artifact mismatch: %s", artifact.Path)
		}
	}
	failures := make([]string, 0)
	for _, check := range evidence.Checks {
		if !check.Passed {
			failures = append(failures, check.ID)
		}
	}
	if len(failures) != len(readiness.Failures) {
		return Readiness{}, errors.New("readiness failure set mismatch")
	}
	for index := range failures {
		if failures[index] != readiness.Failures[index] {
			return Readiness{}, errors.New("readiness failure order mismatch")
		}
	}
	if readiness.Ready != (len(failures) == 0) {
		return Readiness{}, errors.New("readiness decision mismatch")
	}
	if expect == "preflight" && !evidence.SyntheticOnly {
		return Readiness{}, errors.New("preflight incorrectly represented as live evidence")
	}
	return readiness, nil
}

func Cleanup(root, repo, confirmation string) error {
	abs, err := safeRoot(root, repo)
	if err != nil {
		return err
	}
	readiness, err := Verify(context.Background(), abs, repo, "preflight")
	if err != nil {
		readiness, err = Verify(context.Background(), abs, repo, "live")
	}
	if err != nil {
		return fmt.Errorf("refusing cleanup of unverifiable root: %w", err)
	}
	if confirmation == "" || confirmation != readiness.EvidenceIndexSHA256 {
		return errors.New("cleanup confirmation must equal the evidence index SHA-256")
	}
	return os.RemoveAll(abs)
}

package main

import (
	"bytes"
	"errors"
	"os"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

const acceptedLiveOnlyScopeCommit = "264fed3cdef4ef845b815581b4011a6d293ecbaa"

type liveOnlyPolicyArtifacts struct {
	History contracts.HistoryAvailabilityBindingV1
	Lineage contracts.PolicyLineageManifestV3
	Release contracts.LiveOnlyReleaseBindingV1
}

func loadLiveOnlyPolicyArtifacts(historyPath, lineagePath, releasePath, sessionID string) (liveOnlyPolicyArtifacts, error) {
	var artifacts liveOnlyPolicyArtifacts
	if strings.TrimSpace(historyPath) == "" || strings.TrimSpace(lineagePath) == "" || strings.TrimSpace(releasePath) == "" {
		return artifacts, errors.New("live-only history, lineage, and release files are required")
	}
	if err := readCanonicalContract(historyPath, contracts.MaxHistoryBindingBytes, &artifacts.History); err != nil {
		return liveOnlyPolicyArtifacts{}, errors.New("live-only history binding is invalid")
	}
	if err := readCanonicalContract(lineagePath, contracts.MaxPolicyLineageManifestBytes, &artifacts.Lineage); err != nil {
		return liveOnlyPolicyArtifacts{}, errors.New("live-only policy lineage is invalid")
	}
	if err := readCanonicalContract(releasePath, contracts.MaxLiveOnlyReleaseBindingBytes, &artifacts.Release); err != nil {
		return liveOnlyPolicyArtifacts{}, errors.New("live-only release binding is invalid")
	}
	if artifacts.History.Validate() != nil || artifacts.Lineage.Validate() != nil || artifacts.Release.Validate() != nil ||
		artifacts.Release.SourceCommit != acceptedLiveOnlyScopeCommit ||
		artifacts.Release.ValidateAgainst(artifacts.History, artifacts.Lineage) != nil ||
		!matchesProductLineageV3(artifacts.Lineage, artifacts.History, sessionID) {
		return liveOnlyPolicyArtifacts{}, errors.New("live-only policy artifact coherence mismatch")
	}
	return artifacts, nil
}

func readCanonicalContract(path string, maximum int, target any) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > int64(maximum) {
		return errors.New("contract unavailable or oversized")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := contracts.DecodeStrict(payload, target); err != nil {
		return err
	}
	canonical, err := contracts.MarshalCanonical(target)
	if err != nil || !bytes.Equal(payload, canonical) {
		return errors.New("contract is not canonical")
	}
	return nil
}

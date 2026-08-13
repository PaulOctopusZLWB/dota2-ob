package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

const maximumPersistedRecordBytes = 12 << 20

type observationResolver struct {
	rawPath   string
	sessionID string
	lineage   contracts.PolicyLineageManifestV2
	previous  *contracts.LiveObservationV1
	file      *os.File
	scanner   *bufio.Scanner
	sequence  uint64
}

func newObservationResolver(rawPath, sessionID string, lineage contracts.PolicyLineageManifestV2) *observationResolver {
	return &observationResolver{rawPath: rawPath, sessionID: sessionID, lineage: lineage}
}

func (r *observationResolver) Close() error {
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	r.scanner = nil
	return err
}

func (r *observationResolver) resolve(commit contracts.PolicyCommitV2) ([]contracts.InsightCandidateV1, error) {
	record, err := r.readCommittedRecord(commit.ObservationSequence)
	if err != nil {
		return nil, err
	}
	observation, err := capture.MapLiveObservationV1(record)
	if err != nil {
		return nil, err
	}
	if commit.ObservationEvidence == nil || !canonicalEqual(observation.Evidence, *commit.ObservationEvidence) ||
		commit.RawRecordSHA256 != observation.Evidence.RawPayloadSHA256 {
		return nil, errors.New("committed observation evidence mismatch")
	}
	liveHash, err := contracts.CanonicalSHA256(observation)
	if err != nil || liveHash != commit.LiveObservationSHA256 {
		return nil, errors.New("committed live observation hash mismatch")
	}
	policyTimeMS := commit.ResultingPolicyTimeMS
	if len(commit.AuditEvents) > 0 {
		policyTimeMS = commit.AuditEvents[0].PolicyTimeMS
	}
	candidates := insight.Evaluate(insight.Input{
		Observation: observation, Previous: r.previous, Lineage: &r.lineage, PolicyTimeMS: policyTimeMS,
	}, insight.DefaultConfig())
	copyObservation := observation
	r.previous = &copyObservation
	return candidates, nil
}

func newProductionReplayVerifier(rawPath, sessionID string, lineage contracts.PolicyLineageManifestV2, config policy.Config) (commitlog.ReplayVerifierV2, *observationResolver) {
	resolver := newObservationResolver(rawPath, sessionID, lineage)
	engine := policy.New(sessionID, config)
	staged := make(map[uint64][]contracts.InsightCandidateV1)
	return commitlog.ReplayVerifierV2{
		VerifyObservation: func(commit contracts.PolicyCommitV2) error {
			candidates, err := resolver.resolve(commit)
			if err == nil {
				staged[commit.CommitSequence] = candidates
			}
			return err
		},
		VerifyCommand: verifyCommittedCommand,
		Reevaluate: func(commit contracts.PolicyCommitV2) error {
			candidates := staged[commit.CommitSequence]
			delete(staged, commit.CommitSequence)
			return engine.ReplayCommit(commit, candidates)
		},
	}, resolver
}

func verifyCommittedCommand(commit contracts.PolicyCommitV2) error {
	if commit.Command == nil || commit.CommandResult == nil || commit.CommandID != commit.Command.CommandID || commit.CommandID != commit.CommandResult.CommandID {
		return errors.New("incomplete committed command")
	}
	return nil
}

func (r *observationResolver) readCommittedRecord(sequence uint64) (*session.Record, error) {
	if sequence <= r.sequence {
		return nil, errors.New("committed observation sequence is not ordered")
	}
	if r.scanner == nil {
		file, err := os.Open(r.rawPath)
		if err != nil {
			return nil, err
		}
		r.file = file
		r.scanner = bufio.NewScanner(file)
		r.scanner.Buffer(make([]byte, 64<<10), maximumPersistedRecordBytes)
	}
	for r.scanner.Scan() {
		decoder := json.NewDecoder(bytes.NewReader(r.scanner.Bytes()))
		decoder.UseNumber()
		var record session.Record
		if err := decoder.Decode(&record); err != nil {
			return nil, err
		}
		if record.SessionID != r.sessionID || record.Sequence == 0 || record.Sequence <= r.sequence {
			return nil, errors.New("persisted record identity mismatch")
		}
		r.sequence = record.Sequence
		if record.Sequence == sequence {
			return &record, nil
		}
		if record.Sequence > sequence {
			break
		}
	}
	if err := r.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("committed record %d unavailable", sequence)
}

func canonicalEqual(left, right any) bool {
	a, errA := contracts.MarshalCanonical(left)
	b, errB := contracts.MarshalCanonical(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

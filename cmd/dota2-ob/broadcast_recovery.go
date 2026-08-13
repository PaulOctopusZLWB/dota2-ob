package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	index     *os.File
	indexPath string
	rawSize   int64
	maximum   uint64
}

func newObservationResolver(rawPath, sessionID string, lineage contracts.PolicyLineageManifestV2) *observationResolver {
	return &observationResolver{rawPath: rawPath, sessionID: sessionID, lineage: lineage}
}

func (r *observationResolver) Close() error {
	var errs []error
	if r.file != nil {
		errs = append(errs, r.file.Close())
		r.file = nil
	}
	if r.index != nil {
		errs = append(errs, r.index.Close())
		r.index = nil
	}
	if r.indexPath != "" {
		if err := os.Remove(r.indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
		r.indexPath = ""
	}
	return errors.Join(errs...)
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

func recoverProductionApplication(store *commitlog.StoreV2, sessionID string, lineage contracts.PolicyLineageManifestV2, config policy.Config, resolver *observationResolver) (*policy.Application, error) {
	engine := policy.New(sessionID, config)
	err := store.VisitAll(func(committed commitlog.CommittedV2) error {
		var candidates []contracts.InsightCandidateV1
		if committed.Commit.ObservationEvidence != nil {
			var err error
			candidates, err = resolver.resolve(committed.Commit)
			if err != nil {
				return err
			}
		}
		return engine.ReplayCommit(committed.Commit, candidates)
	})
	if err != nil {
		return nil, err
	}
	return policy.NewBoundApplication(engine, store, lineage)
}

func (r *observationResolver) readCommittedRecord(sequence uint64) (*session.Record, error) {
	if sequence == 0 {
		return nil, errors.New("committed observation sequence is invalid")
	}
	if err := r.ensureIndex(); err != nil {
		return nil, err
	}
	if sequence > r.maximum {
		return nil, fmt.Errorf("committed record %d unavailable", sequence)
	}
	offset, err := r.indexOffset(sequence)
	if err != nil {
		return nil, err
	}
	end := r.rawSize
	if sequence < r.maximum {
		end, err = r.indexOffset(sequence + 1)
		if err != nil {
			return nil, err
		}
	}
	if end <= offset || end-offset > maximumPersistedRecordBytes {
		return nil, errors.New("persisted record frame is invalid")
	}
	payload := make([]byte, end-offset)
	if _, err := r.file.ReadAt(payload, offset); err != nil {
		return nil, err
	}
	payload = bytes.TrimSuffix(payload, []byte{'\n'})
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var record session.Record
	if err := decoder.Decode(&record); err != nil {
		return nil, err
	}
	if record.SessionID != r.sessionID || record.Sequence != sequence {
		return nil, errors.New("persisted record identity mismatch")
	}
	return &record, nil
}

func (r *observationResolver) ensureIndex() (resultErr error) {
	if r.index != nil {
		return nil
	}
	raw, err := os.Open(r.rawPath)
	if err != nil {
		return err
	}
	index, err := os.CreateTemp("", "dota2-ob-policy-raw-index-*")
	if err != nil {
		_ = raw.Close()
		return err
	}
	indexPath := index.Name()
	defer func() {
		if resultErr != nil {
			_ = raw.Close()
			_ = index.Close()
			_ = os.Remove(indexPath)
		}
	}()
	scanner := bufio.NewScanner(raw)
	scanner.Buffer(make([]byte, 64<<10), maximumPersistedRecordBytes)
	var offset int64
	var sequence uint64
	for scanner.Scan() {
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.UseNumber()
		var record session.Record
		if err := decoder.Decode(&record); err != nil {
			return err
		}
		sequence++
		if record.SessionID != r.sessionID || record.Sequence != sequence {
			return errors.New("persisted record identity mismatch")
		}
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], uint64(offset))
		if _, err := index.Write(encoded[:]); err != nil {
			return err
		}
		offset += int64(len(scanner.Bytes()) + 1)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	info, err := raw.Stat()
	if err != nil {
		return err
	}
	r.file, r.index, r.indexPath = raw, index, indexPath
	r.rawSize, r.maximum = info.Size(), sequence
	return nil
}

func (r *observationResolver) indexOffset(sequence uint64) (int64, error) {
	var encoded [8]byte
	if _, err := r.index.ReadAt(encoded[:], int64(sequence-1)*int64(len(encoded))); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("committed record %d unavailable", sequence)
		}
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(encoded[:])), nil
}

func canonicalEqual(left, right any) bool {
	a, errA := contracts.MarshalCanonical(left)
	b, errB := contracts.MarshalCanonical(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

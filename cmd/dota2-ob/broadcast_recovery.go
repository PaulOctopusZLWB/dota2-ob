package main

import (
	"bytes"
	"encoding/binary"
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

const observationIndexEntryBytes = 16

type observationResolver struct {
	rawPath   string
	sessionID string
	lineage   contracts.PolicyLineageManifestV2
	previous  *contracts.LiveObservationV1
	data      *os.File
	index     *os.File
	indexPath string
	dataPath  string
	maximum   uint64
}

func newObservationResolver(rawPath, sessionID string, lineage contracts.PolicyLineageManifestV2) *observationResolver {
	return &observationResolver{rawPath: rawPath, sessionID: sessionID, lineage: lineage}
}

func (r *observationResolver) Close() error {
	var errs []error
	if r.data != nil {
		errs = append(errs, r.data.Close())
		r.data = nil
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
	if r.dataPath != "" {
		if err := os.Remove(r.dataPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
		r.dataPath = ""
	}
	return errors.Join(errs...)
}

func (r *observationResolver) resolve(commit contracts.PolicyCommitV2) ([]contracts.InsightCandidateV1, error) {
	observation, err := r.readCommittedObservation(commit.ObservationSequence)
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
		Observation: *observation, Previous: r.previous, Lineage: &r.lineage, PolicyTimeMS: policyTimeMS,
	}, insight.DefaultConfig())
	copyObservation := *observation
	r.previous = &copyObservation
	return candidates, nil
}

func newProductionReplayVerifier(resolver *observationResolver, sessionID string, config policy.Config) commitlog.ReplayVerifierV2 {
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
	}
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

func (r *observationResolver) readCommittedObservation(sequence uint64) (*contracts.LiveObservationV1, error) {
	if sequence == 0 {
		return nil, errors.New("committed observation sequence is invalid")
	}
	if err := r.ensureIndex(); err != nil {
		return nil, err
	}
	if sequence > r.maximum {
		return nil, fmt.Errorf("committed record %d unavailable", sequence)
	}
	offset, length, err := r.indexEntry(sequence)
	if err != nil {
		return nil, err
	}
	if length == 0 || length > contracts.MaxLiveObservationBytes {
		return nil, errors.New("committed sequence has no produced observation")
	}
	payload := make([]byte, length)
	if _, err := r.data.ReadAt(payload, int64(offset)); err != nil {
		return nil, err
	}
	var observation contracts.LiveObservationV1
	if err := contracts.DecodeStrict(payload, &observation); err != nil {
		return nil, err
	}
	if err := observation.Validate(); err != nil {
		return nil, err
	}
	if observation.Evidence.SessionID != r.sessionID || observation.Evidence.Sequence != sequence {
		return nil, errors.New("persisted observation identity mismatch")
	}
	return &observation, nil
}

func (r *observationResolver) ensureIndex() (resultErr error) {
	if r.index != nil {
		return nil
	}
	index, err := os.CreateTemp("", "dota2-ob-policy-raw-index-*")
	if err != nil {
		return err
	}
	indexPath := index.Name()
	if err := os.Remove(indexPath); err != nil {
		_ = index.Close()
		return err
	}
	indexPath = ""
	data, err := os.CreateTemp("", "dota2-ob-policy-observations-*")
	if err != nil {
		_ = index.Close()
		return err
	}
	dataPath := data.Name()
	if err := os.Remove(dataPath); err != nil {
		_ = index.Close()
		_ = data.Close()
		return err
	}
	dataPath = ""
	defer func() {
		if resultErr != nil {
			_ = index.Close()
			_ = data.Close()
			_ = os.Remove(indexPath)
			_ = os.Remove(dataPath)
		}
	}()
	var offset uint64
	err = session.StreamRecords(r.rawPath, r.sessionID, func(record *session.Record) error {
		var payload []byte
		if record.ProjectionResult == session.ProjectionProduced {
			observation, mapErr := capture.MapLiveObservationV1(record)
			if mapErr != nil {
				return mapErr
			}
			payload, mapErr = contracts.MarshalCanonical(observation)
			if mapErr != nil {
				return mapErr
			}
			if len(payload) > contracts.MaxLiveObservationBytes {
				return errors.New("persisted observation exceeds contract bound")
			}
		}
		var entry [observationIndexEntryBytes]byte
		binary.BigEndian.PutUint64(entry[:8], offset)
		binary.BigEndian.PutUint64(entry[8:], uint64(len(payload)))
		if _, writeErr := index.Write(entry[:]); writeErr != nil {
			return writeErr
		}
		if len(payload) > 0 {
			if _, writeErr := data.Write(payload); writeErr != nil {
				return writeErr
			}
			offset += uint64(len(payload))
		}
		r.maximum = record.Sequence
		return nil
	})
	if err != nil {
		return err
	}
	r.data, r.index, r.indexPath, r.dataPath = data, index, indexPath, dataPath
	return nil
}

func (r *observationResolver) indexEntry(sequence uint64) (uint64, uint64, error) {
	var encoded [observationIndexEntryBytes]byte
	if _, err := r.index.ReadAt(encoded[:], int64(sequence-1)*observationIndexEntryBytes); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, 0, fmt.Errorf("committed record %d unavailable", sequence)
		}
		return 0, 0, err
	}
	return binary.BigEndian.Uint64(encoded[:8]), binary.BigEndian.Uint64(encoded[8:]), nil
}

func canonicalEqual(left, right any) bool {
	a, errA := contracts.MarshalCanonical(left)
	b, errB := contracts.MarshalCanonical(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

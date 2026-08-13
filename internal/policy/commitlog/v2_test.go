package commitlog_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
)

func TestV2SealsManifestBeforeFirstCommitAndConstructsWithoutHashCycle(t *testing.T) {
	var stages []string
	hooks := commitlog.Hooks{
		SyncFile: func(f *os.File) error { stages = append(stages, "sync:"+filepath.Base(f.Name())); return f.Sync() },
		SyncDir:  func(path string) error { stages = append(stages, "syncdir:"+filepath.Base(path)); return nil },
		Write: func(f *os.File, p []byte) (int, error) {
			stages = append(stages, "write:"+filepath.Base(f.Name()))
			return f.Write(p)
		},
	}
	root := t.TempDir()
	manifest := validManifestForStore()
	store, state, err := verifiedOpenV2(root, "session", manifest, commitlog.WithV2Hooks(hooks))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if state.LineageManifestID != manifest.MustContentID() {
		t.Fatalf("lineage ID = %q", state.LineageManifestID)
	}
	commit, checkpoint := firstV2CommandAndCheckpoint(t, manifest)
	committed, err := store.Append(commit)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.CommandLocators = []contracts.PolicyCommandLocatorV2{committed.Locator}
	if got, _ := checkpoint.ComputeStateHash(); got != commit.ResultingStateHash {
		t.Fatal("application locator changed pre-frame semantic state hash")
	}
	manifestWrite, frameWrite := indexStage(stages, "write:.lineage.v2.json.tmp"), indexStage(stages, "write:00000000000000000001.pcl2")
	if manifestWrite < 0 || frameWrite < 0 || manifestWrite >= frameWrite {
		t.Fatalf("manifest was not sealed before frame append: %v", stages)
	}
}

func TestV2DuplicateLookupReturnsExactFrameAfterRestart(t *testing.T) {
	root := t.TempDir()
	manifest := validManifestForStore()
	store, _, err := verifiedOpenV2(root, "session", manifest)
	if err != nil {
		t.Fatal(err)
	}
	commit, _ := firstV2CommandAndCheckpoint(t, manifest)
	first, err := store.Append(commit)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	reopened, state, err := verifiedOpenV2(root, "session", manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if state.CommandResults[commit.CommandID] == "" || state.CommandLocators[commit.CommandID].FrameSHA256 != first.Hash {
		t.Fatalf("rebuilt indexes = %#v %#v", state.CommandResults, state.CommandLocators)
	}
	storedResult, found, err := reopened.LookupCommand(commit.CommandID)
	storedResultHash, _ := contracts.CanonicalSHA256(storedResult)
	firstResultHash, _ := contracts.CanonicalSHA256(*first.Commit.CommandResult)
	if err != nil || !found || storedResultHash != firstResultHash {
		t.Fatalf("direct duplicate lookup result=%#v found=%v err=%v", storedResult, found, err)
	}
	before, _ := os.Stat(filepath.Join(root, "session", first.Locator.SegmentID))
	duplicate, err := reopened.Append(commit)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(root, "session", first.Locator.SegmentID))
	if duplicate.Hash != first.Hash || !reflect.DeepEqual(duplicate.Commit.CommandResult, first.Commit.CommandResult) || before.Size() != after.Size() {
		t.Fatal("duplicate did not return exact committed frame without append")
	}
}

func TestV2CheckpointValidatesLocatorsAndReplaysLaterFrames(t *testing.T) {
	root := t.TempDir()
	manifest := validManifestForStore()
	store, _, err := verifiedOpenV2(root, "session", manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	commit, checkpoint := firstV2CommandAndCheckpoint(t, manifest)
	first, err := store.Append(commit)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.CommandLocators = []contracts.PolicyCommandLocatorV2{first.Locator}
	checkpoint.ReferencedCommitSHA256 = first.Hash
	if err := store.WriteCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}

	observation := validObservationCommitV2(t, manifest, 2, commit)
	if _, err := store.Append(observation); err != nil {
		t.Fatal(err)
	}
	loaded, later, err := store.LoadCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.CommitSequence != 1 || len(later) != 1 || later[0].Commit.CommitSequence != 2 {
		t.Fatalf("loaded=%#v later=%#v", loaded, later)
	}
}

func TestV2MissingOrSyntacticallyCorruptCheckpointUsesStreamingOpenReplay(t *testing.T) {
	root := t.TempDir()
	manifest := validManifestForStore()
	store, _, _ := verifiedOpenV2(root, "session", manifest)
	commit, checkpoint := firstV2CommandAndCheckpoint(t, manifest)
	first, err := store.Append(commit)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "session", "checkpoint.v2.json")
	checkpoint.CommandLocators = []contracts.PolicyCommandLocatorV2{first.Locator}
	checkpoint.ReferencedCommitSHA256 = first.Hash
	checkpoint.StateHash = strings.Repeat("f", 64)
	semanticCorruption, _ := contracts.MarshalCanonical(checkpoint)
	for _, data := range [][]byte{nil, []byte("not-json"), semanticCorruption} {
		if data == nil {
			_ = os.Remove(path)
		} else if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, replay, err := store.LoadCheckpoint()
		if err != nil || loaded != nil || len(replay) != 0 {
			t.Fatalf("loaded=%#v replay=%d err=%v", loaded, len(replay), err)
		}
	}
}

func TestV2CheckpointLocatorMissingDuplicateSubstitutionOrCorruptionFailsClosed(t *testing.T) {
	mutations := map[string]func(*contracts.PolicyCheckpointV2){
		"missing": func(cp *contracts.PolicyCheckpointV2) { cp.CommandLocators = nil },
		"duplicate": func(cp *contracts.PolicyCheckpointV2) {
			cp.CommandLocators = append(cp.CommandLocators, cp.CommandLocators[0])
		},
		"substitution": func(cp *contracts.PolicyCheckpointV2) { cp.CommandLocators[0].CommandID = "other-command" },
		"corruption":   func(cp *contracts.PolicyCheckpointV2) { cp.CommandLocators[0].FrameSHA256 = strings.Repeat("f", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			manifest := validManifestForStore()
			store, _, _ := verifiedOpenV2(root, "session", manifest)
			commit, cp := firstV2CommandAndCheckpoint(t, manifest)
			first, err := store.Append(commit)
			if err != nil {
				t.Fatal(err)
			}
			cp.CommandLocators = []contracts.PolicyCommandLocatorV2{first.Locator}
			cp.ReferencedCommitSHA256 = first.Hash
			mutate(&cp)
			raw, _ := contracts.MarshalCanonical(cp)
			if err := os.WriteFile(filepath.Join(root, "session", "checkpoint.v2.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.LoadCheckpoint(); !errors.Is(err, commitlog.ErrInvalidCheckpoint) {
				t.Fatalf("locator mutation error = %v", err)
			}
		})
	}
}

func TestV2RecoveryRejectsMixedLineageAndTruncatesTornTail(t *testing.T) {
	manifest := validManifestForStore()
	t.Run("mixed v1", func(t *testing.T) {
		root := t.TempDir()
		v1, _, _ := commitlog.Open(root, "session")
		if _, err := v1.Append(validCommit(1, 0, 1, "legacy")); err != nil {
			t.Fatal(err)
		}
		_ = v1.Close()
		if _, _, err := verifiedOpenV2(root, "session", manifest); !errors.Is(err, commitlog.ErrMixedLineage) {
			t.Fatalf("mixed lineage error = %v", err)
		}
	})
	t.Run("torn v2 tail", func(t *testing.T) {
		root := t.TempDir()
		store, _, _ := verifiedOpenV2(root, "session", manifest)
		commit, _ := firstV2CommandAndCheckpoint(t, manifest)
		first, _ := store.Append(commit)
		_ = store.Close()
		path := filepath.Join(root, "session", first.Locator.SegmentID)
		clean, _ := os.ReadFile(path)
		if err := os.WriteFile(path, append(clean, []byte{0, 0, 1}...), 0o600); err != nil {
			t.Fatal(err)
		}
		reopened, state, err := verifiedOpenV2(root, "session", manifest)
		if err != nil {
			t.Fatal(err)
		}
		_ = reopened.Close()
		got, _ := os.ReadFile(path)
		if state.CommitSequence != 1 || len(got) != len(clean) {
			t.Fatalf("state=%#v size=%d want=%d", state, len(got), len(clean))
		}
	})
}

func TestV2ReplayVerifiesObservationSourceBeforeEvaluation(t *testing.T) {
	manifest := validManifestForStore()
	first, _ := firstV2CommandAndCheckpoint(t, manifest)
	observation := validObservationCommitV2(t, manifest, 2, first)
	for _, mismatch := range []string{"evidence mismatch", "raw record hash mismatch", "live observation projection hash mismatch"} {
		t.Run(mismatch, func(t *testing.T) {
			evaluated := false
			err := commitlog.VerifyReplayV2([]contracts.PolicyCommitV2{observation}, commitlog.ReplayVerifierV2{
				VerifyObservation: func(contracts.PolicyCommitV2) error { return errors.New(mismatch) },
				Reevaluate:        func(contracts.PolicyCommitV2) error { evaluated = true; return nil },
			})
			if err == nil || evaluated {
				t.Fatalf("err=%v evaluated=%v", err, evaluated)
			}
		})
	}
}

func TestV2RejectedAdmittedCommandPersistsIndexWithoutRevisionOrTimeAdvance(t *testing.T) {
	root := t.TempDir()
	manifest := validManifestForStore()
	store, _, _ := verifiedOpenV2(root, "session", manifest)
	defer store.Close()
	first, checkpoint := firstV2CommandAndCheckpoint(t, manifest)
	if _, err := store.Append(first); err != nil {
		t.Fatal(err)
	}
	command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "command-2", SessionID: "session", Action: contracts.ActionEnableRule, TargetRuleID: "rule-1", ExpectedPolicyRevision: 1, PolicyTimeMS: 9}
	result := contracts.OperatorCommandResultV1{SchemaVersion: contracts.OperatorCommandResultSchemaV1, CommandID: command.CommandID, SessionID: "session", Status: contracts.CommandRejected, PreviousRevision: 1, ResultingRevision: 1, DecisionIDs: []string{}, Reason: "out_of_order_policy_time"}
	resultHash, _ := contracts.CanonicalSHA256(result)
	checkpoint.CommandResults = append(checkpoint.CommandResults, contracts.PolicyCommandResultRefV2{CommandID: command.CommandID, ResultSHA256: resultHash})
	resultingHash, _ := checkpoint.ComputeStateHash()
	second := contracts.PolicyCommitV2{
		SchemaVersion: contracts.PolicyCommitSchemaV2, LineageManifestID: manifest.MustContentID(), LineageManifestSHA256: manifest.MustContentID(), SessionID: "session", CommitSequence: 2,
		CommandID: command.CommandID, Command: &command, PriorPolicyRevision: 1, ResultingPolicyRevision: 1, PriorStateHash: first.ResultingStateHash, ResultingStateHash: resultingHash,
		ResultingPolicyTimeMS: 10, Decisions: []contracts.BroadcastDecisionV1{}, CommandResult: &result,
		AuditEvents: []contracts.AuditEventV1{{SchemaVersion: contracts.AuditEventSchemaV1, EventID: "audit-command-2", SessionID: "session", EventType: "command_rejected", PolicyTimeMS: 9, CommandID: command.CommandID, Reason: result.Reason}},
		Publication: contracts.PublicationUnchanged,
	}
	if _, err := store.Append(second); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	reopened, state, err := verifiedOpenV2(root, "session", manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if state.PolicyRevision != 1 || state.LastPolicyTimeMS != 10 || len(state.CommandResults) != 2 || state.StateHash == first.ResultingStateHash {
		t.Fatalf("recovered state = %#v", state)
	}
}

func TestV2MissingManifestWithExistingFramesFailsClosed(t *testing.T) {
	root := t.TempDir()
	manifest := validManifestForStore()
	store, _, _ := verifiedOpenV2(root, "session", manifest)
	commit, _ := firstV2CommandAndCheckpoint(t, manifest)
	if _, err := store.Append(commit); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	if err := os.Remove(filepath.Join(root, "session", "lineage.v2.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifiedOpenV2(root, "session", manifest); !errors.Is(err, commitlog.ErrLineage) {
		t.Fatalf("missing manifest error = %v", err)
	}
}

func TestV2ManifestSubstitutionFailsClosed(t *testing.T) {
	root := t.TempDir()
	manifest := validManifestForStore()
	store, _, err := verifiedOpenV2(root, "session", manifest)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	changed := manifest
	changed.Config.ContentSHA256 = strings.Repeat("9", 64)
	if _, _, err := verifiedOpenV2(root, "session", changed); !errors.Is(err, commitlog.ErrLineage) {
		t.Fatalf("manifest substitution error = %v", err)
	}
}

func TestV1OpenRejectsExistingV2Frames(t *testing.T) {
	root := t.TempDir()
	manifest := validManifestForStore()
	store, _, _ := verifiedOpenV2(root, "session", manifest)
	commit, _ := firstV2CommandAndCheckpoint(t, manifest)
	if _, err := store.Append(commit); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	if _, _, err := commitlog.Open(root, "session"); !errors.Is(err, commitlog.ErrMixedLineage) {
		t.Fatalf("v1 mixed lineage error = %v", err)
	}
}

func TestV2CheckpointCannotOmitBothSemanticEntryAndLocator(t *testing.T) {
	root := t.TempDir()
	manifest := validManifestForStore()
	store, _, _ := verifiedOpenV2(root, "session", manifest)
	defer store.Close()
	commit, cp := firstV2CommandAndCheckpoint(t, manifest)
	first, err := store.Append(commit)
	if err != nil {
		t.Fatal(err)
	}
	cp.ReferencedCommitSHA256 = first.Hash
	cp.CommandResults = nil
	cp.CommandLocators = nil
	cp.StateHash, _ = cp.ComputeStateHash()
	commit.ResultingStateHash = cp.StateHash
	if err := store.WriteCheckpoint(cp); !errors.Is(err, commitlog.ErrInvalidCheckpoint) {
		t.Fatalf("incomplete checkpoint error = %v", err)
	}
}

func TestV2SyncFailureRollsBackAndRollbackFailureSeals(t *testing.T) {
	manifest := validManifestForStore()
	t.Run("sync failure rolls back", func(t *testing.T) {
		root := t.TempDir()
		failFrameSync := false
		hooks := commitlog.Hooks{SyncFile: func(f *os.File) error {
			if failFrameSync && strings.HasSuffix(f.Name(), ".pcl2") {
				failFrameSync = false
				return errors.New("sync failure")
			}
			return f.Sync()
		}}
		store, _, err := verifiedOpenV2(root, "session", manifest, commitlog.WithV2Hooks(hooks))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		failFrameSync = true
		commit, _ := firstV2CommandAndCheckpoint(t, manifest)
		if _, err := store.Append(commit); !errors.Is(err, commitlog.ErrAppendFailed) {
			t.Fatalf("append error = %v", err)
		}
		path := filepath.Join(root, "session", "00000000000000000001.pcl2")
		info, err := os.Stat(path)
		if err != nil || info.Size() != 0 {
			t.Fatalf("rollback stat=%v err=%v", info, err)
		}
	})
	t.Run("rollback failure seals", func(t *testing.T) {
		hooks := commitlog.Hooks{
			Write: func(f *os.File, p []byte) (int, error) {
				if strings.HasSuffix(f.Name(), ".pcl2") {
					n, _ := f.Write(p[:4])
					return n, errors.New("short write")
				}
				return f.Write(p)
			},
			Truncate: func(*os.File, int64) error { return errors.New("truncate failure") },
		}
		store, _, err := verifiedOpenV2(t.TempDir(), "session", manifest, commitlog.WithV2Hooks(hooks))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		commit, _ := firstV2CommandAndCheckpoint(t, manifest)
		if _, err := store.Append(commit); !errors.Is(err, commitlog.ErrSealed) {
			t.Fatalf("append error = %v", err)
		}
		if _, err := store.Append(commit); !errors.Is(err, commitlog.ErrSealed) {
			t.Fatalf("sealed retry error = %v", err)
		}
	})
}

func TestV2OpenRequiresReplayVerifierBeforeReturningTrustedState(t *testing.T) {
	manifest := validManifestForStore()
	store, state, err := commitlog.OpenV2(t.TempDir(), "session", manifest)
	if err == nil || store != nil || state.SessionID != "" {
		if store != nil {
			_ = store.Close()
		}
		t.Fatalf("unverified open returned store=%v state=%#v err=%v", store != nil, state, err)
	}
}

func TestV2RecoveryStreamsFramesWithoutRetainingCanonicalPayloads(t *testing.T) {
	root := t.TempDir()
	manifest := validManifestForStore()
	store, _, err := verifiedOpenV2(root, "session", manifest)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := firstV2CommandAndCheckpoint(t, manifest)
	if _, err := store.Append(first); err != nil {
		t.Fatal(err)
	}
	second := validObservationCommitV2(t, manifest, 2, first)
	if _, err := store.Append(second); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	verified := 0
	verifier := trustedVerifierV2()
	verifier.Reevaluate = func(contracts.PolicyCommitV2) error { verified++; return nil }
	reopened, recovered, err := commitlog.OpenV2(root, "session", manifest, commitlog.WithV2ReplayVerifier(verifier))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if verified != 2 {
		t.Fatalf("streamed verifier calls = %d, want 2", verified)
	}
	if len(recovered.Commits) != 0 {
		t.Fatalf("recovery retained %d complete payloads/decoded commits", len(recovered.Commits))
	}
}

func TestV2ReopenReestablishesFailedSegmentDirectorySyncBeforeRetry(t *testing.T) {
	root := t.TempDir()
	manifest := validManifestForStore()
	failSegmentDirSync := false
	hooks := commitlog.Hooks{SyncDir: func(string) error {
		if failSegmentDirSync {
			failSegmentDirSync = false
			return errors.New("simulated power-crash window")
		}
		return nil
	}}
	store, _, err := verifiedOpenV2(root, "session", manifest, commitlog.WithV2Hooks(hooks))
	if err != nil {
		t.Fatal(err)
	}
	commit, _ := firstV2CommandAndCheckpoint(t, manifest)
	failSegmentDirSync = true
	if _, err := store.Append(commit); !errors.Is(err, commitlog.ErrSealed) {
		t.Fatalf("segment directory sync failure error = %v", err)
	}
	_ = store.Close()

	reopenDirSyncs := 0
	reopened, _, err := verifiedOpenV2(root, "session", manifest, commitlog.WithV2Hooks(commitlog.Hooks{
		SyncDir: func(string) error { reopenDirSyncs++; return nil },
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopenDirSyncs == 0 {
		t.Fatal("restart did not re-establish the parent-directory durability barrier")
	}
	if _, err := reopened.Append(commit); err != nil {
		t.Fatalf("retry after verified directory barrier: %v", err)
	}
}

func TestV2RecoveryMismatchReturnsNoStateAndNoAppendCapableStore(t *testing.T) {
	root := t.TempDir()
	manifest := validManifestForStore()
	store, _, err := verifiedOpenV2(root, "session", manifest)
	if err != nil {
		t.Fatal(err)
	}
	command, _ := firstV2CommandAndCheckpoint(t, manifest)
	if _, err := store.Append(command); err != nil {
		t.Fatal(err)
	}
	observation := validObservationCommitV2(t, manifest, 2, command)
	if _, err := store.Append(observation); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	for _, mismatch := range []string{
		"evidence", "raw_record", "live_projection", "command_result",
		"decision", "audit", "publication", "state",
	} {
		t.Run(mismatch, func(t *testing.T) {
			verifier := trustedVerifierV2()
			if mismatch == "evidence" || mismatch == "raw_record" || mismatch == "live_projection" {
				verifier.VerifyObservation = func(contracts.PolicyCommitV2) error { return errors.New(mismatch + " mismatch") }
			} else {
				verifier.Reevaluate = func(commit contracts.PolicyCommitV2) error {
					if commit.CommandID != "" {
						return errors.New(mismatch + " mismatch")
					}
					return nil
				}
			}
			reopened, recovered, err := commitlog.OpenV2(root, "session", manifest, commitlog.WithV2ReplayVerifier(verifier))
			if err == nil || reopened != nil || recovered.SessionID != "" || recovered.CommitSequence != 0 || len(recovered.Commits) != 0 {
				if reopened != nil {
					_ = reopened.Close()
				}
				t.Fatalf("mismatch exposed store=%v state=%#v err=%v", reopened != nil, recovered, err)
			}
		})
	}
}

func firstV2CommandAndCheckpoint(t *testing.T, manifest contracts.PolicyLineageManifestV2) (contracts.PolicyCommitV2, contracts.PolicyCheckpointV2) {
	t.Helper()
	command := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "command-1", SessionID: "session", Action: contracts.ActionDisableRule, TargetRuleID: "rule-1", PolicyTimeMS: 10}
	result := contracts.OperatorCommandResultV1{SchemaVersion: contracts.OperatorCommandResultSchemaV1, CommandID: command.CommandID, SessionID: "session", Status: contracts.CommandAccepted, PreviousRevision: 0, ResultingRevision: 1, DecisionIDs: []string{}, Reason: "accepted"}
	resultHash, _ := contracts.CanonicalSHA256(result)
	cp := contracts.PolicyCheckpointV2{
		SchemaVersion: contracts.PolicyCheckpointSchemaV2, LineageManifestID: manifest.MustContentID(), LineageManifestSHA256: manifest.MustContentID(),
		SessionID: "session", CommitSequence: 1, ReferencedCommitSHA256: strings.Repeat("a", 64), PolicyRevision: 1, LastPolicyTimeMS: 10, CreatedTimeMS: 10,
		Preview: []contracts.InsightCandidateV1{}, DisabledRuleIDs: []string{"rule-1"}, Cooldowns: []contracts.RuleCooldownV2{}, Pins: []contracts.PolicyPinV2{},
		CommandResults: []contracts.PolicyCommandResultRefV2{{CommandID: command.CommandID, ResultSHA256: resultHash}}, CommandLocators: []contracts.PolicyCommandLocatorV2{}, CandidateTombstones: []contracts.PolicyCandidateTombstoneV2{},
	}
	cp.StateHash, _ = cp.ComputeStateHash()
	commit := contracts.PolicyCommitV2{
		SchemaVersion: contracts.PolicyCommitSchemaV2, LineageManifestID: manifest.MustContentID(), LineageManifestSHA256: manifest.MustContentID(), SessionID: "session", CommitSequence: 1,
		CommandID: command.CommandID, Command: &command, ResultingPolicyRevision: 1, ResultingStateHash: cp.StateHash, ResultingPolicyTimeMS: 10,
		Decisions: []contracts.BroadcastDecisionV1{}, CommandResult: &result,
		AuditEvents: []contracts.AuditEventV1{{SchemaVersion: contracts.AuditEventSchemaV1, EventID: "audit-command-1", SessionID: "session", EventType: "command", PolicyTimeMS: 10, CommandID: command.CommandID, Reason: "accepted"}},
		Publication: contracts.PublicationUnchanged,
	}
	return commit, cp
}

func validObservationCommitV2(t *testing.T, manifest contracts.PolicyLineageManifestV2, sequence uint64, prior contracts.PolicyCommitV2) contracts.PolicyCommitV2 {
	t.Helper()
	evidence := contracts.EvidenceRefV1{RecordSchemaVersion: 2, SessionID: "session", Sequence: 1, ReceiveTime: time.Unix(1, 0).UTC(), Source: "gsi", ProviderVersion: contracts.Absent[int64](), RawPayloadSHA256: strings.Repeat("4", 64)}
	return contracts.PolicyCommitV2{
		SchemaVersion: contracts.PolicyCommitSchemaV2, LineageManifestID: manifest.MustContentID(), LineageManifestSHA256: manifest.MustContentID(), SessionID: "session", CommitSequence: sequence,
		ObservationSequence: 1, ObservationEvidence: &evidence, RawRecordSHA256: strings.Repeat("5", 64), LiveObservationSHA256: strings.Repeat("6", 64),
		PriorPolicyRevision: prior.ResultingPolicyRevision, ResultingPolicyRevision: prior.ResultingPolicyRevision, PriorStateHash: prior.ResultingStateHash, ResultingStateHash: prior.ResultingStateHash,
		ResultingObservationSequence: 1, ResultingPolicyTimeMS: prior.ResultingPolicyTimeMS,
		Decisions: []contracts.BroadcastDecisionV1{}, AuditEvents: []contracts.AuditEventV1{{SchemaVersion: contracts.AuditEventSchemaV1, EventID: "audit-observation", SessionID: "session", EventType: "observation", PolicyTimeMS: 10, CandidateID: "candidate-none", Reason: "unchanged"}},
		Publication: contracts.PublicationUnchanged,
	}
}

func validManifestForStore() contracts.PolicyLineageManifestV2 {
	artifact := func(version, digit string) contracts.PolicyArtifactIdentityV2 {
		return contracts.PolicyArtifactIdentityV2{Version: version, ContentSHA256: strings.Repeat(digit, 64)}
	}
	return contracts.PolicyLineageManifestV2{
		SchemaVersion: contracts.PolicyLineageManifestSchemaV2, SessionID: "session", RawRecordSchema: artifact("raw.v2", "1"), RawRecordFraming: artifact("jsonl.v1", "2"),
		RawPayloadSchema: artifact("gsi.v1", "3"), LiveObservationSchema: artifact(contracts.LiveObservationSchemaV1, "4"), ProjectionMapping: artifact("mapping.v1", "5"),
		TournamentScopeID: strings.Repeat("6", 64), TournamentScopeSHA256: strings.Repeat("6", 64), HistoricalSnapshotID: strings.Repeat("7", 64), HistoricalSnapshotSHA256: strings.Repeat("7", 64), EligibleBaselineSHA256: []string{strings.Repeat("8", 64)},
		Rules: artifact("rules.v1", "a"), Config: artifact("config.v1", "b"), Catalog: artifact("catalog.v1", "c"), Terminology: artifact("terminology.v1", "d"), LocalizationParameterMapping: artifact("params.v1", "e"), EngineBuild: artifact("build.v1", "f"),
	}
}

func indexStage(stages []string, want string) int {
	for i, stage := range stages {
		if stage == want {
			return i
		}
	}
	return -1
}

func verifiedOpenV2(root, sessionID string, manifest contracts.PolicyLineageManifestV2, opts ...commitlog.V2Option) (*commitlog.StoreV2, commitlog.StateV2, error) {
	verified := []commitlog.V2Option{commitlog.WithV2ReplayVerifier(trustedVerifierV2())}
	verified = append(verified, opts...)
	return commitlog.OpenV2(root, sessionID, manifest, verified...)
}

func trustedVerifierV2() commitlog.ReplayVerifierV2 {
	return commitlog.ReplayVerifierV2{
		VerifyObservation: func(contracts.PolicyCommitV2) error { return nil },
		VerifyCommand:     func(contracts.PolicyCommitV2) error { return nil },
		Reevaluate:        func(contracts.PolicyCommitV2) error { return nil },
	}
}

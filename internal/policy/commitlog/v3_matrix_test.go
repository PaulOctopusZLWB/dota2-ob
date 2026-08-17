package commitlog_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
)

var _ policy.CommitAppenderV3 = (*commitlog.StoreV3)(nil)
var _ policy.CommandCommitLookupV3 = (*commitlog.StoreV3)(nil)

func TestV3MissingRetainedArtifactsWithFramesFailsClosed(t *testing.T) {
	for _, name := range []string{"history-binding.v1.json", "lineage.v3.json"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			binding, manifest := validLineageV3()
			store, _, err := openV3(root, binding, manifest)
			if err != nil {
				t.Fatal(err)
			}
			engine := engineV3(manifest)
			commit := commandCommitV3(engine, "durable", 1)
			if _, err := store.Append(commit); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "session", name)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if _, _, err := openV3(root, binding, manifest); !errors.Is(err, commitlog.ErrLineage) {
				t.Fatalf("missing retained %s reopened: %v", name, err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing %s recreated", name)
			}
		})
	}
	for _, name := range []string{"history-binding.v1.json", "lineage.v3.json"} {
		t.Run("substituted_"+name, func(t *testing.T) {
			root := t.TempDir()
			binding, manifest := validLineageV3()
			store, _, err := openV3(root, binding, manifest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Append(commandCommitV3(engineV3(manifest), "durable", 1)); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "session", name)
			if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := openV3(root, binding, manifest); !errors.Is(err, commitlog.ErrLineage) {
				t.Fatalf("substituted retained %s reopened: %v", name, err)
			}
			retained, err := os.ReadFile(path)
			if err != nil || string(retained) != "{}" {
				t.Fatalf("substituted %s was replaced", name)
			}
		})
	}
}

func TestV3AppendRollbackSyncFailureAndSeal(t *testing.T) {
	binding, manifest := validLineageV3()
	engine := engineV3(manifest)
	commit := commandCommitV3(engine, "rollback", 1)
	for _, tc := range []struct {
		name       string
		hooks      commitlog.Hooks
		wantSealed bool
	}{
		{"short_write", commitlog.Hooks{Write: func(f *os.File, p []byte) (int, error) {
			if strings.HasSuffix(f.Name(), ".pcl3") {
				return 0, errors.New("write")
			}
			return f.Write(p)
		}}, false},
		{"sync_once", func() commitlog.Hooks {
			failed := false
			return commitlog.Hooks{SyncFile: func(f *os.File) error {
				if strings.HasSuffix(f.Name(), ".pcl3") && !failed {
					failed = true
					return errors.New("sync")
				}
				return f.Sync()
			}}
		}(), false},
		{"rollback_sync", func() commitlog.Hooks {
			calls := 0
			return commitlog.Hooks{SyncFile: func(f *os.File) error {
				if strings.HasSuffix(f.Name(), ".pcl3") {
					calls++
					if calls <= 2 {
						return errors.New("sync")
					}
				}
				return f.Sync()
			}}
		}(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, _, err := commitlog.OpenV3(root, "session", binding, manifest, commitlog.WithV3ReplayVerifier(trustedVerifierV3()), commitlog.WithV3Hooks(tc.hooks))
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.Append(commit)
			if err == nil {
				t.Fatal("faulted append succeeded")
			}
			count := 0
			_ = store.VisitAll(func(commitlog.CommittedV3) error { count++; return nil })
			if count != 0 {
				t.Fatal("failed frame became visible")
			}
			if tc.wantSealed {
				if _, err := store.Append(commit); !errors.Is(err, commitlog.ErrSealed) {
					t.Fatalf("unverifiable rollback not sealed: %v", err)
				}
			}
		})
	}
}

func TestV3InitialSealFailureCreatesNoFrames(t *testing.T) {
	root := t.TempDir()
	binding, manifest := validLineageV3()
	hooks := commitlog.Hooks{SyncFile: func(f *os.File) error {
		if strings.Contains(f.Name(), "history-binding.v1.json") {
			return errors.New("seal sync")
		}
		return f.Sync()
	}}
	if _, _, err := commitlog.OpenV3(root, "session", binding, manifest, commitlog.WithV3ReplayVerifier(trustedVerifierV3()), commitlog.WithV3Hooks(hooks)); err == nil {
		t.Fatal("faulted initial seal succeeded")
	}
	frames, err := filepath.Glob(filepath.Join(root, "session", "*.pcl3"))
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 0 {
		t.Fatalf("initial seal failure created frames: %v", frames)
	}
}

func TestV3IncompleteTailRestartAndCleanRebuild(t *testing.T) {
	binding, manifest := validLineageV3()
	root := t.TempDir()
	store, _, err := openV3(root, binding, manifest)
	if err != nil {
		t.Fatal(err)
	}
	commit := commandCommitV3(engineV3(manifest), "tail", 1)
	if _, err := store.Append(commit); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	segment := filepath.Join(root, "session", "00000000000000000001.pcl3")
	before, _ := os.Stat(segment)
	f, err := os.OpenFile(segment, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte{1, 2, 3})
	_ = f.Close()
	reopened, state, err := openV3(root, binding, manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, _ := os.Stat(segment)
	if state.CommitSequence != 1 || after.Size() != before.Size() {
		t.Fatal("incomplete tail was not durably truncated")
	}
	clean := t.TempDir()
	fresh, _, err := openV3(clean, binding, manifest)
	if err != nil {
		t.Fatal(err)
	}
	_ = fresh.Close()
	for _, name := range []string{"history-binding.v1.json", "lineage.v3.json"} {
		a, _ := os.ReadFile(filepath.Join(root, "session", name))
		b, _ := os.ReadFile(filepath.Join(clean, "session", name))
		if string(a) != string(b) {
			t.Fatalf("clean rebuild %s differs", name)
		}
	}
}

func TestV3RejectsMixedVersionsAndBounds(t *testing.T) {
	binding, manifest := validLineageV3()
	for _, suffix := range []string{".pcl", ".pcl2"} {
		t.Run(suffix, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "session")
			_ = os.MkdirAll(dir, 0o700)
			_ = os.WriteFile(filepath.Join(dir, "00000000000000000001"+suffix), nil, 0o600)
			if _, _, err := openV3(root, binding, manifest); !errors.Is(err, commitlog.ErrMixedLineage) {
				t.Fatalf("mixed %s accepted: %v", suffix, err)
			}
		})
	}
	root := t.TempDir()
	store, _, err := commitlog.OpenV3(root, "session", binding, manifest, commitlog.WithV3ReplayVerifier(trustedVerifierV3()), commitlog.WithV3SessionLimits(32, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(commandCommitV3(engineV3(manifest), "capacity", 1)); !errors.Is(err, commitlog.ErrCapacity) {
		t.Fatalf("session byte bound: %v", err)
	}
	segmentRoot := t.TempDir()
	engine := engineV3(manifest)
	first := commandCommitV3(engine, "segment-one", 1)
	second := commandCommitV3(engine, "segment-two", 2)
	firstPayload, _ := contracts.MarshalCanonical(first)
	secondPayload, _ := contracts.MarshalCanonical(second)
	limit := len(firstPayload)
	if len(secondPayload) > limit {
		limit = len(secondPayload)
	}
	segmentStore, _, err := commitlog.OpenV3(segmentRoot, "session", binding, manifest,
		commitlog.WithV3ReplayVerifier(trustedVerifierV3()),
		commitlog.WithV3SegmentLimit(int64(limit+44)),
		commitlog.WithV3SessionLimits(1<<20, 1),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := segmentStore.Append(first); err != nil {
		t.Fatal(err)
	}
	if _, err := segmentStore.Append(second); !errors.Is(err, commitlog.ErrCapacity) {
		t.Fatalf("segment count bound: %v", err)
	}
}

func TestV3CheckpointLocatorCorruptionStateMismatchAndRestart(t *testing.T) {
	binding, manifest := validLineageV3()
	root := t.TempDir()
	store, _, err := openV3(root, binding, manifest)
	if err != nil {
		t.Fatal(err)
	}
	engine := engineV3(manifest)
	commit := commandCommitV3(engine, "checkpoint", 1)
	committed, err := store.Append(commit)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := checkpointV3(engine, committed)
	if err := store.WriteCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	bad := checkpoint
	bad.StateHash = strings.Repeat("f", 64)
	if err := store.WriteCheckpoint(bad); !errors.Is(err, commitlog.ErrInvalidCheckpoint) {
		t.Fatalf("state mismatch accepted: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	bad = checkpoint
	bad.CommandLocators = append([]contracts.PolicyCommandLocatorV2(nil), checkpoint.CommandLocators...)
	bad.CommandLocators[0].SegmentID = "00000000000000000001.pcl2"
	payload, _ := contracts.MarshalCanonical(bad)
	if err := os.WriteFile(filepath.Join(root, "session", "checkpoint.v3.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, _, err := openV3(root, binding, manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.LoadCheckpoint(func(commitlog.CommittedV3) error { return nil }); !errors.Is(err, commitlog.ErrInvalidCheckpoint) {
		t.Fatalf("locator corruption accepted: %v", err)
	}
}

func TestV3ReplayMismatchDuplicateRejectedOutOfOrderAndConcurrency(t *testing.T) {
	binding, manifest := validLineageV3()
	root := t.TempDir()
	store, _, err := openV3(root, binding, manifest)
	if err != nil {
		t.Fatal(err)
	}
	engine := engineV3(manifest)
	first := commandCommitV3(engine, "duplicate", 1)
	original, err := store.Append(first)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.Append(first)
	if err != nil || duplicate.Hash != original.Hash {
		t.Fatalf("durable duplicate: %v", err)
	}
	second := engine.ApplyCommandV3(contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "rejected", SessionID: "session", Action: contracts.ActionShow, TargetCandidateID: "missing-candidate", ExpectedPolicyRevision: engine.State().PolicyRevision, PolicyTimeMS: 2})
	if second.CommandResult == nil || second.CommandResult.Status != contracts.CommandRejected {
		t.Fatalf("rejected fixture: %#v", second)
	}
	if _, err := store.Append(second); err != nil {
		t.Fatal(err)
	}
	outOfOrder := commandCommitV3(engine, "out-of-order", 3)
	outOfOrder.CommitSequence = 2
	if _, err := store.Append(outOfOrder); !errors.Is(err, commitlog.ErrSequence) {
		t.Fatalf("out-of-order sequence accepted: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	badVerifier := commitlog.WithV3ReplayVerifier(commitlog.ReplayVerifierV3{VerifyObservation: func(contracts.PolicyCommitV3) error { return nil }, VerifyCommand: func(contracts.PolicyCommitV3) error { return nil }, Reevaluate: func(contracts.PolicyCommitV3) error { return errors.New("state mismatch") }})
	if _, _, err := commitlog.OpenV3(root, "session", binding, manifest, badVerifier); !errors.Is(err, commitlog.ErrCorrupt) {
		t.Fatalf("replay mismatch accepted: %v", err)
	}
	concurrentRoot := t.TempDir()
	concurrent, _, err := openV3(concurrentRoot, binding, manifest)
	if err != nil {
		t.Fatal(err)
	}
	same := commandCommitV3(engineV3(manifest), "concurrent", 1)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := concurrent.Append(same); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	_ = concurrent.VisitAll(func(commitlog.CommittedV3) error { count++; return nil })
	if count != 1 {
		t.Fatalf("concurrent duplicate frames=%d", count)
	}
}

func TestV3CheckpointBoundedIndexes(t *testing.T) {
	cp := contracts.PolicyCheckpointV3{SchemaVersion: contracts.PolicyCheckpointSchemaV3, CommandResults: make([]contracts.PolicyCommandResultRefV2, contracts.MaxCheckpointCommandResults+1)}
	if err := cp.Validate(); err == nil {
		t.Fatal("over-bound V3 checkpoint accepted")
	}
}

func openV3(root string, binding contracts.HistoryAvailabilityBindingV1, manifest contracts.PolicyLineageManifestV3) (*commitlog.StoreV3, commitlog.StateV3, error) {
	return commitlog.OpenV3(root, "session", binding, manifest, commitlog.WithV3ReplayVerifier(trustedVerifierV3()))
}
func engineV3(manifest contracts.PolicyLineageManifestV3) *policy.Engine {
	config := policy.DefaultConfig()
	config.LineageID = manifest.MustContentID()
	return policy.New("session", config)
}
func commandCommitV3(engine *policy.Engine, id string, at int64) contracts.PolicyCommitV3 {
	action := contracts.ActionDisableRule
	if len(engine.State().DisabledRuleIDs) > 0 {
		action = contracts.ActionEnableRule
	}
	return engine.ApplyCommandV3(contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: id, SessionID: "session", Action: action, TargetRuleID: "draft.v1", ExpectedPolicyRevision: engine.State().PolicyRevision, PolicyTimeMS: at})
}
func checkpointV3(engine *policy.Engine, committed commitlog.CommittedV3) contracts.PolicyCheckpointV3 {
	state := engine.State()
	return contracts.PolicyCheckpointV3{SchemaVersion: contracts.PolicyCheckpointSchemaV3, LineageManifestID: committed.Commit.LineageManifestID, LineageManifestSHA256: committed.Commit.LineageManifestSHA256, SessionID: "session", CommitSequence: committed.Commit.CommitSequence, ReferencedCommitSHA256: committed.Hash, LastObservationSequence: state.LastObservationSequence, PolicyRevision: state.PolicyRevision, LastPolicyTimeMS: state.LastPolicyTimeMS, StateHash: engine.StateHash(), CreatedTimeMS: state.LastPolicyTimeMS, Preview: state.Preview, DisabledRuleIDs: state.DisabledRuleIDs, Cooldowns: state.Cooldowns, Pins: state.Pins, EmergencyHide: state.EmergencyHide, ActivePrimary: state.ActivePrimary, CommandResults: state.CommandResults, CommandLocators: []contracts.PolicyCommandLocatorV2{committed.Locator}, CandidateTombstones: state.CandidateTombstones}
}

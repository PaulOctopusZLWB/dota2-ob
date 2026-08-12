package commitlog_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
)

func checkpointFor(commit commitlog.Committed) contracts.PolicyCheckpointV1 {
	return contracts.PolicyCheckpointV1{
		SchemaVersion: contracts.PolicyCheckpointSchemaV1, SessionID: "session",
		CommitSequence: commit.Commit.CommitSequence, PolicyRevision: commit.Commit.ResultingPolicyRevision,
		RuleVersion: "rules.v1", ConfigVersion: "config.v1", StateHash: commit.Commit.ResultingStateHash,
		ReferencedCommitSHA256: commit.Hash, CreatedTimeMS: 1,
		CommandResults:      []contracts.OperatorCommandResultV1{*commit.Commit.CommandResult},
		PreviewCandidateIDs: []string{}, Cooldowns: []contracts.RuleCooldownV1{}, Pins: []contracts.PolicyPinV1{},
	}
}

func TestCheckpointIsSyncedCacheAndLaterFramesReplay(t *testing.T) {
	root := t.TempDir()
	store, _, err := commitlog.Open(root, "session")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Append(validCommit(1, 0, 1, "command-1"))
	if err != nil {
		t.Fatal(err)
	}
	cp := checkpointFor(first)
	if err = store.WriteCheckpoint(cp); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(validCommit(2, 1, 2, "command-2")); err != nil {
		t.Fatal(err)
	}
	loaded, later, err := store.LoadCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.CommitSequence != 1 || len(later) != 1 || later[0].Commit.CommitSequence != 2 {
		t.Fatalf("loaded=%#v later=%#v", loaded, later)
	}
	info, err := os.Stat(filepath.Join(root, "session", "checkpoint.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}

func TestMissingCorruptOrMismatchedCheckpointRebuildsFromLog(t *testing.T) {
	root := t.TempDir()
	store, _, _ := commitlog.Open(root, "session")
	first, _ := store.Append(validCommit(1, 0, 1, "command-1"))
	path := filepath.Join(root, "session", "checkpoint.v1.json")
	for _, data := range [][]byte{nil, []byte("not-json"), func() []byte {
		cp := checkpointFor(first)
		cp.ReferencedCommitSHA256 = strings.Repeat("f", 64)
		b, _ := contracts.MarshalCanonical(cp)
		return b
	}()} {
		if data == nil {
			_ = os.Remove(path)
		} else if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, replay, err := store.LoadCheckpoint()
		if err != nil {
			t.Fatal(err)
		}
		if loaded != nil || len(replay) != 1 {
			t.Fatalf("loaded=%#v replay=%d", loaded, len(replay))
		}
	}
}

func TestCheckpointRejectsIncompleteIndexAndRenameFailure(t *testing.T) {
	root := t.TempDir()
	store, _, _ := commitlog.Open(root, "session")
	first, _ := store.Append(validCommit(1, 0, 1, "command-1"))
	cp := checkpointFor(first)
	cp.CommandResults = nil
	if err := store.WriteCheckpoint(cp); !errors.Is(err, commitlog.ErrInvalidCheckpoint) {
		t.Fatalf("incomplete index error=%v", err)
	}
	root2 := t.TempDir()
	hooks := commitlog.Hooks{Rename: func(old, new string) error {
		if filepath.Base(new) == "checkpoint.v1.json" {
			return errors.New("rename failure")
		}
		return os.Rename(old, new)
	}}
	store2, _, _ := commitlog.Open(root2, "session", commitlog.WithHooks(hooks))
	first2, _ := store2.Append(validCommit(1, 0, 1, "command-1"))
	if err := store2.WriteCheckpoint(checkpointFor(first2)); err == nil {
		t.Fatal("rename failure accepted")
	}
}

func rejectedCommit(sequence, revision uint64, commandID string) contracts.PolicyCommitV1 {
	commit := validCommit(sequence, revision, revision, commandID)
	if sequence > 1 {
		commit.PriorStateHash = strings.Repeat("a", 64)
	}
	commit.CommandResult.Status = contracts.CommandRejected
	commit.CommandResult.Reason = "stale_revision"
	commit.AuditEvents[0].Reason = "stale_revision"
	return commit
}

func TestCheckpointCanonicalizesSameRevisionRejectedResultsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	store, _, err := commitlog.Open(root, "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(rejectedCommit(1, 0, "z-command")); err != nil {
		t.Fatal(err)
	}
	last, err := store.Append(rejectedCommit(2, 0, "a-command"))
	if err != nil {
		t.Fatal(err)
	}
	cp := checkpointFor(last)
	cp.CommandResults = []contracts.OperatorCommandResultV1{
		*rejectedCommit(2, 0, "a-command").CommandResult,
		*rejectedCommit(1, 0, "z-command").CommandResult,
	}
	if err = cp.ValidateAgainstCommit(last.Commit); err != nil {
		t.Fatalf("canonical fixture invalid: %v", err)
	}
	if err = store.WriteCheckpoint(cp); err != nil {
		t.Fatalf("canonical checkpoint rejected: %v", err)
	}
	_ = store.Close()

	reopened, state, err := commitlog.Open(root, "session")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(state.CommandResults) != 2 {
		t.Fatalf("recovered command index=%d", len(state.CommandResults))
	}
	loaded, later, err := reopened.LoadCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || len(later) != 0 {
		t.Fatalf("checkpoint=%#v later=%d", loaded, len(later))
	}
	if got := []string{loaded.CommandResults[0].CommandID, loaded.CommandResults[1].CommandID}; got[0] != "a-command" || got[1] != "z-command" {
		t.Fatalf("canonical order=%v", got)
	}
}

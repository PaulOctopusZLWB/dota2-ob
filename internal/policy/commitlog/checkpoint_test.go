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

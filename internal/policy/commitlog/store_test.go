package commitlog_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
)

func validCommit(sequence, prior, resulting uint64, commandID string) contracts.PolicyCommitV1 {
	result := &contracts.OperatorCommandResultV1{SchemaVersion: contracts.OperatorCommandResultSchemaV1, CommandID: commandID, SessionID: "session", Status: contracts.CommandAccepted, PreviousRevision: prior, ResultingRevision: resulting, Reason: "accepted"}
	return contracts.PolicyCommitV1{
		SchemaVersion: contracts.PolicyCommitSchemaV1, SessionID: "session", CommitSequence: sequence,
		CommandID: commandID, PriorPolicyRevision: prior, ResultingPolicyRevision: resulting,
		PriorStateHash: func() string {
			if prior == 0 {
				return ""
			}
			return strings.Repeat("a", 64)
		}(),
		ResultingStateHash: strings.Repeat("a", 64), CommandResult: result,
		AuditEvents: []contracts.AuditEventV1{{SchemaVersion: contracts.AuditEventSchemaV1, EventID: "audit-" + commandID, SessionID: "session", EventType: "command", PolicyTimeMS: int64(sequence), CommandID: commandID, Reason: "accepted"}},
		Publication: contracts.PublicationUnchanged,
	}
}

func TestAppendReturnsOnlyAfterSegmentSyncAndDuplicateReplaysStoredResult(t *testing.T) {
	var synced bool
	hooks := commitlog.Hooks{SyncFile: func(*os.File) error { synced = true; return nil }}
	root := t.TempDir()
	store, state, err := commitlog.Open(root, "session", commitlog.WithHooks(hooks))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if state.CommitSequence != 0 {
		t.Fatalf("initial sequence = %d", state.CommitSequence)
	}

	commit := validCommit(1, 0, 1, "command-1")
	committed, err := store.Append(commit)
	if err != nil {
		t.Fatal(err)
	}
	if !synced {
		t.Fatal("commit became observable before segment sync")
	}
	if committed.Hash == "" || committed.Commit.CommandResult == nil {
		t.Fatalf("incomplete committed value: %#v", committed)
	}
	segment := filepath.Join(root, "session", "00000000000000000001.pcl")
	before, err := os.Stat(segment)
	if err != nil {
		t.Fatal(err)
	}

	synced = false
	replay, err := store.Append(commit)
	if err != nil {
		t.Fatal(err)
	}
	if synced {
		t.Fatal("duplicate command appended another frame")
	}
	if replay.Hash != committed.Hash || !reflect.DeepEqual(replay.Commit.CommandResult, committed.Commit.CommandResult) {
		t.Fatal("duplicate did not return exact stored terminal result")
	}
	after, err := os.Stat(segment)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("live duplicate appended bytes: %d -> %d", before.Size(), after.Size())
	}
}

func TestInvalidNestedCommitIsRejectedBeforeCreatingStorage(t *testing.T) {
	root := t.TempDir()
	commit := validCommit(1, 0, 1, "command-1")
	commit.AuditEvents[0].SessionID = "other"
	store, _, err := commitlog.Open(root, "session")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Append(commit); !errors.Is(err, commitlog.ErrInvalidCommit) {
		t.Fatalf("Append error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "session"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid commit created files: %v", entries)
	}
}

func TestCommittedFrameIsImmutableAcrossCallerMutation(t *testing.T) {
	store, _, err := commitlog.Open(t.TempDir(), "session")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	commit := validCommit(1, 0, 1, "command-1")
	committed, err := store.Append(commit)
	if err != nil {
		t.Fatal(err)
	}
	commit.AuditEvents[0].Reason = "mutated input"
	committed.Commit.AuditEvents[0].Reason = "mutated output"
	replay, err := store.Append(validCommit(1, 0, 1, "command-1"))
	if err != nil {
		t.Fatal(err)
	}
	if replay.Commit.AuditEvents[0].Reason != "accepted" {
		t.Fatalf("stored frame was mutable: %q", replay.Commit.AuditEvents[0].Reason)
	}
}

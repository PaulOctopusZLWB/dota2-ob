package commitlog_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
)

func TestSyncFailureRollsBackBeforeAnyCommittedValue(t *testing.T) {
	calls := 0
	hooks := commitlog.Hooks{SyncFile: func(f *os.File) error {
		calls++
		if calls == 2 {
			return errors.New("data sync failed")
		}
		return f.Sync()
	}}
	root := t.TempDir()
	store, _, err := commitlog.Open(root, "session", commitlog.WithHooks(hooks))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.Append(validCommit(1, 0, 1, "command-1")); !errors.Is(err, commitlog.ErrAppendFailed) {
		t.Fatalf("Append error=%v", err)
	}
	info, _ := os.Stat(onlySegment(t, root))
	if info.Size() != 0 {
		t.Fatalf("failed unsynced frame remains: %d", info.Size())
	}
}

func TestAmbiguousRollbackVerificationSealsAndHides(t *testing.T) {
	hooks := commitlog.Hooks{Write: func(f *os.File, p []byte) (int, error) { return f.Write(p[:8]) }, Truncate: func(*os.File, int64) error { return nil }}
	store, _, err := commitlog.Open(t.TempDir(), "session", commitlog.WithHooks(hooks))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.Append(validCommit(1, 0, 1, "command-1")); !errors.Is(err, commitlog.ErrSealed) || !strings.Contains(err.Error(), "rollback_unverified") {
		t.Fatalf("Append error=%v", err)
	}
}

func TestSegmentRenameAndDirectorySyncFailuresFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hooks commitlog.Hooks
	}{
		{"rename", commitlog.Hooks{Rename: func(string, string) error { return errors.New("rename") }}},
		{"directory sync", commitlog.Hooks{SyncDir: func(string) error { return errors.New("dir sync") }}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, err := commitlog.Open(t.TempDir(), "session", commitlog.WithHooks(tc.hooks))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, err = store.Append(validCommit(1, 0, 1, "command-1")); !errors.Is(err, commitlog.ErrSealed) {
				t.Fatalf("Append error=%v", err)
			}
		})
	}
}

func TestRecoveryTailTruncateOrSyncFailureFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hooks commitlog.Hooks
	}{
		{"truncate", commitlog.Hooks{Truncate: func(*os.File, int64) error { return errors.New("truncate") }}},
		{"sync", commitlog.Hooks{SyncFile: func(*os.File) error { return errors.New("sync") }}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, _, _ := commitlog.Open(root, "session")
			_, _ = store.Append(validCommit(1, 0, 1, "command-1"))
			_ = store.Close()
			path := onlySegment(t, root)
			f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			_, _ = f.Write([]byte{0, 1})
			_ = f.Close()
			if _, _, err := commitlog.Open(root, "session", commitlog.WithHooks(tc.hooks)); !errors.Is(err, commitlog.ErrSealed) {
				t.Fatalf("Open error=%v", err)
			}
		})
	}
}

func TestCanonicalPayloadAndHashMatchAcceptedGolden(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "contracts", "testdata", "policy_commit_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var commit contracts.PolicyCommitV1
	if err = contracts.DecodeStrict(payload, &commit); err != nil {
		t.Fatal(err)
	}
	canonical, err := contracts.MarshalCanonical(commit)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := os.ReadFile(filepath.Join("..", "..", "contracts", "testdata", "policy_commit_v1.json.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	store, _, err := commitlog.Open(t.TempDir(), commit.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.Append(commit)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Payload, canonical) || got.Hash != strings.TrimSpace(string(wantHash)) {
		t.Fatalf("adapter canonical/hash mismatch: %s", got.Hash)
	}
}

func TestInterruptionBeforeSyncNeverAuthorizesPublication(t *testing.T) {
	hooks := commitlog.Hooks{Interrupt: func(stage string) error {
		if stage == "before_sync" {
			return errors.New("power loss")
		}
		return nil
	}}
	store, _, err := commitlog.Open(t.TempDir(), "session", commitlog.WithHooks(hooks))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	commit := validCommit(1, 0, 1, "command-1")
	commit.Publication = contracts.PublicationHide
	if committed, err := store.Append(commit); err == nil || committed.Hash != "" {
		t.Fatalf("unsynced commit observable: %#v, %v", committed, err)
	}
}

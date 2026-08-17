package session_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

func TestCaptureLineageV3IdentitiesAreContentAddressed(t *testing.T) {
	for _, pair := range []struct{ descriptor, identity string }{
		{session.RawRecordSchemaV3Descriptor, session.RawRecordSchemaV3Identity},
		{session.RawRecordFramingV3Descriptor, session.RawRecordFramingV3Identity},
		{session.RawPayloadSchemaV3Descriptor, session.RawPayloadSchemaV3Identity},
		{session.GSIProjectionMappingV3Descriptor, session.GSIProjectionMappingV3Identity},
	} {
		if got := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(pair.descriptor))); got != pair.identity {
			t.Fatalf("identity=%s want=%s", pair.identity, got)
		}
	}
}

func TestStoreRequiresBoundV3LineageToContinueExistingV3Session(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewStore(root, session.WithSessionID("bound"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.NewStore(root, session.WithSessionID("bound")); err != nil {
		t.Fatalf("bound continuation: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "bound", "capture_lineage_v3.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := session.NewStore(root, session.WithSessionID("bound")); err == nil {
		t.Fatal("continued V3 session without lineage binding")
	}
}

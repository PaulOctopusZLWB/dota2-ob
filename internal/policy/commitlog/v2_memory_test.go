package commitlog_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
)

const (
	v2MemoryChildEnv = "DOTA2_OB_V2_MEMORY_CHILD"
	v2MemoryRootEnv  = "DOTA2_OB_V2_MEMORY_ROOT"
)

func TestV2RecoveryMemoryBoundAtRepresentativeScale(t *testing.T) {
	if os.Getenv(v2MemoryChildEnv) == "1" {
		runV2RecoveryMemoryChild(t)
		return
	}

	root := t.TempDir()
	manifest := validManifestForStore()
	hooks := commitlog.Hooks{
		SyncFile: func(*os.File) error { return nil },
		SyncDir:  func(string) error { return nil },
	}
	store, _, err := commitlog.OpenV2(root, "session", manifest, commitlog.WithV2Hooks(hooks), commitlog.WithV2ReplayVerifier(trustedVerifierV2()))
	if err != nil {
		t.Fatal(err)
	}
	priorHash := ""
	for sequence := uint64(1); sequence <= 800; sequence++ {
		commit := scaledObservationCommitV2(manifest, sequence, priorHash)
		if _, err := store.Append(commit); err != nil {
			t.Fatalf("append scaled frame %d: %v", sequence, err)
		}
		priorHash = commit.ResultingStateHash
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var retainedBytes int64
	err = filepath.Walk(filepath.Join(root, "session"), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".pcl2") {
			retainedBytes += info.Size()
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if retainedBytes < 64<<20 {
		t.Fatalf("representative retained log = %d bytes, want at least 64 MiB", retainedBytes)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestV2RecoveryMemoryBoundAtRepresentativeScale$", "-test.v")
	cmd.Env = append(os.Environ(), v2MemoryChildEnv+"=1", v2MemoryRootEnv+"="+root)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("recovery memory child: %v\n%s", err, output)
	}
	const prefix = "V2_RECOVERY_MAX_RSS_KIB="
	var maxRSSKiB int64
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, prefix) {
			maxRSSKiB, err = strconv.ParseInt(strings.TrimPrefix(line, prefix), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if maxRSSKiB == 0 {
		t.Fatalf("child omitted RSS measurement:\n%s", output)
	}
	// Recovery gets half of the accepted 384 MiB combined live-process gate;
	// capture/projector/gateway and allocator/platform variation retain the other half.
	if maxRSSKiB > 192<<10 {
		t.Fatalf("streaming recovery max RSS = %d KiB, exceeds 192 MiB component budget (log=%d bytes)", maxRSSKiB, retainedBytes)
	}
	t.Logf("retained_log_bytes=%d recovery_max_rss_kib=%d combined_gate_kib=%d", retainedBytes, maxRSSKiB, 384<<10)
}

func runV2RecoveryMemoryChild(t *testing.T) {
	root := os.Getenv(v2MemoryRootEnv)
	manifest := validManifestForStore()
	expectedSequence := uint64(0)
	priorHash := ""
	verifier := commitlog.ReplayVerifierV2{
		VerifyObservation: func(commit contracts.PolicyCommitV2) error {
			if commit.ObservationEvidence == nil || commit.ObservationEvidence.Sequence != commit.ObservationSequence || commit.RawRecordSHA256 != storedMatrixSHA(commit.ObservationSequence+1000) || commit.LiveObservationSHA256 != storedMatrixSHA(commit.ObservationSequence+2000) {
				return fmt.Errorf("scaled observation source mismatch")
			}
			return nil
		},
		VerifyCommand: func(contracts.PolicyCommitV2) error { return fmt.Errorf("unexpected command") },
		Reevaluate: func(commit contracts.PolicyCommitV2) error {
			expectedSequence++
			if commit.CommitSequence != expectedSequence || commit.ObservationSequence != expectedSequence || commit.PriorStateHash != priorHash || commit.ResultingStateHash != storedMatrixSHA(expectedSequence+3000) {
				return fmt.Errorf("scaled semantic replay mismatch at %d", expectedSequence)
			}
			priorHash = commit.ResultingStateHash
			return nil
		},
	}
	store, state, err := commitlog.OpenV2(root, "session", manifest, commitlog.WithV2ReplayVerifier(verifier))
	if err != nil {
		t.Fatal(err)
	}
	if state.CommitSequence != 800 || len(state.Commits) != 0 || expectedSequence != 800 {
		t.Fatalf("streaming state=%#v verified=%d", state, expectedSequence)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("V2_RECOVERY_MAX_RSS_KIB=%d\n", usage.Maxrss)
}

func scaledObservationCommitV2(manifest contracts.PolicyLineageManifestV2, sequence uint64, priorHash string) contracts.PolicyCommitV2 {
	evidence := contracts.EvidenceRefV1{RecordSchemaVersion: 2, SessionID: "session", Sequence: sequence, ReceiveTime: time.Unix(int64(sequence), 0).UTC(), Source: "gsi", ProviderVersion: contracts.Absent[int64](), RawPayloadSHA256: storedMatrixSHA(sequence)}
	audits := make([]contracts.AuditEventV1, 10)
	for i := range audits {
		audits[i] = contracts.AuditEventV1{SchemaVersion: contracts.AuditEventSchemaV1, EventID: fmt.Sprintf("audit-%04d-%02d", sequence, i), SessionID: "session", EventType: "observation", PolicyTimeMS: int64(sequence), CandidateID: fmt.Sprintf("candidate-%04d-%02d", sequence, i), Reason: strings.Repeat("x", 8<<10)}
	}
	return contracts.PolicyCommitV2{
		SchemaVersion: contracts.PolicyCommitSchemaV2, LineageManifestID: manifest.MustContentID(), LineageManifestSHA256: manifest.MustContentID(), SessionID: "session", CommitSequence: sequence,
		ObservationSequence: sequence, ObservationEvidence: &evidence, RawRecordSHA256: storedMatrixSHA(sequence + 1000), LiveObservationSHA256: storedMatrixSHA(sequence + 2000),
		PriorStateHash: priorHash, ResultingStateHash: storedMatrixSHA(sequence + 3000), ResultingObservationSequence: sequence, ResultingPolicyTimeMS: int64(sequence),
		Decisions: []contracts.BroadcastDecisionV1{}, AuditEvents: audits, Publication: contracts.PublicationSuppressedV2,
	}
}

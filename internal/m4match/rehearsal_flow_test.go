package m4match

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rehearsalTestHead = "1111111111111111111111111111111111111111"

func TestRefusedRehearsalSealsVerifiesAndCleansExactly(t *testing.T) {
	repo := repositoryRoot(t)
	var terminalPayloads [][]byte
	var receiptPayloads [][]byte
	var roots []string
	for iteration := 0; iteration < 2; iteration++ {
		root := freshIsolatedPath(t, "dot84-refused")
		roots = append(roots, root)
		readiness, err := RehearsalPreflight(context.Background(), refusedPreflightConfig(root, repo, false))
		if err != nil {
			t.Fatal(err)
		}
		if readiness.Ready || readiness.ConsoleState != rehearsalRefused || readiness.HumanInstruction != "" || !equalStringSlice(readiness.Failures, []string{"remote_branch_head"}) {
			t.Fatalf("unsafe refusal: %+v", readiness)
		}
		receipt, err := RehearsalAttempt(context.Background(), RehearsalAttemptConfig{DataRoot: root, RepoRoot: repo, Request: RehearsalAttemptRequestV1{SchemaVersion: RehearsalAttemptSchemaVersion}})
		if err != nil || receipt.Outcome != "rehearsal_failed" || receipt.FailureCode != "preflight_refused" || !receipt.CleanupAdmissible {
			t.Fatalf("refusal was not terminal: %+v %v", receipt, err)
		}
		verified, err := VerifyRehearsalTerminal(context.Background(), root, repo)
		if err != nil || verified != receipt {
			t.Fatalf("refusal verification failed: %+v %v", verified, err)
		}
		if _, err = Verify(context.Background(), root, repo, "preflight"); err == nil {
			t.Fatal("P4 verifier admitted rehearsal-purpose root")
		}
		if err = CleanupRehearsal(root, repo, strings.Repeat("0", 64)); err == nil {
			t.Fatal("wrong token removed refusal evidence")
		}
		if _, err = os.Stat(root); err != nil {
			t.Fatalf("wrong token did not preserve root: %v", err)
		}
		terminalPayloads = append(terminalPayloads, mustRead(t, filepath.Join(root, "evidence/canonical/terminal.json")))
		receiptPayloads = append(receiptPayloads, mustRead(t, filepath.Join(root, "evidence/terminal-receipt.json")))
	}
	if string(terminalPayloads[0]) != string(terminalPayloads[1]) || string(receiptPayloads[0]) != string(receiptPayloads[1]) {
		t.Fatal("fresh refused roots were not byte-identical")
	}
	for index, root := range roots {
		var receipt RehearsalTerminalReceiptV1
		if err := json.Unmarshal(receiptPayloads[index], &receipt); err != nil {
			t.Fatal(err)
		}
		if err := CleanupRehearsal(root, repo, receipt.TerminalSHA256); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("cleanup left root: %v", err)
		}
	}
}

func TestRemoteAndDraftRefusalAndRegistryResealAttack(t *testing.T) {
	repo := repositoryRoot(t)
	root := freshIsolatedPath(t, "dot84-double-refusal")
	readiness, err := RehearsalPreflight(context.Background(), refusedPreflightConfig(root, repo, true))
	if err != nil {
		t.Fatal(err)
	}
	if !equalStringSlice(readiness.Failures, []string{"draft_pr_head", "remote_branch_head"}) {
		t.Fatalf("unexpected failures: %v", readiness.Failures)
	}
	if _, err = RehearsalAttempt(context.Background(), RehearsalAttemptConfig{DataRoot: root, RepoRoot: repo, Request: RehearsalAttemptRequestV1{SchemaVersion: RehearsalAttemptSchemaVersion}}); err != nil {
		t.Fatal(err)
	}
	receipt, err := VerifyRehearsalTerminal(context.Background(), root, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = CleanupRehearsal(root, repo, receipt.TerminalSHA256); err != nil {
		t.Fatal(err)
	}
	listenerRefused := freshIsolatedPath(t, "dot84-listener-refusal")
	listenerConfig := refusedPreflightConfig(listenerRefused, repo, false)
	listenerConfig.Listen = func(string) (io.Closer, error) { return nil, errors.New("occupied") }
	readiness, err = RehearsalPreflight(context.Background(), listenerConfig)
	if err != nil || checkStateFromFailures(readiness.Failures, "exclusive_localhost_gsi_listener") == false {
		t.Fatalf("listener refusal not sealed: %+v %v", readiness, err)
	}
	if _, err = RehearsalAttempt(context.Background(), RehearsalAttemptConfig{DataRoot: listenerRefused, RepoRoot: repo, Request: RehearsalAttemptRequestV1{SchemaVersion: RehearsalAttemptSchemaVersion}}); err != nil {
		t.Fatalf("listener refusal did not terminalize after listener became free: %v", err)
	}
	receipt, err = VerifyRehearsalTerminal(context.Background(), listenerRefused, repo)
	if err != nil || CleanupRehearsal(listenerRefused, repo, receipt.TerminalSHA256) != nil {
		t.Fatalf("listener refusal was not verifier-cleanable: %v", err)
	}

	tampered := freshIsolatedPath(t, "dot84-registry-tamper")
	config := refusedPreflightConfig(tampered, repo, false)
	config.Listen = func(string) (io.Closer, error) { return nil, errors.New("occupied") }
	readiness, err = RehearsalPreflight(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(tampered, "evidence/canonical/evidence-index.json")
	var evidence RehearsalEvidenceV1
	if err = json.Unmarshal(mustRead(t, indexPath), &evidence); err != nil {
		t.Fatal(err)
	}
	filtered := evidence.Checks[:0]
	for _, check := range evidence.Checks {
		if check.ID != "exclusive_localhost_gsi_listener" {
			filtered = append(filtered, check)
		}
	}
	evidence.Checks = filtered
	indexPayload, _ := canonical(evidence)
	if err = os.WriteFile(indexPath, indexPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	readiness.Ready = true
	readiness.ConsoleState = rehearsalReady
	readiness.HumanInstruction = RehearsalHumanInstruction
	readiness.Failures = nil
	readiness.EvidenceIndexSHA256 = payloadSHA(indexPayload)
	readinessPayload, _ := canonical(readiness)
	if err = os.WriteFile(filepath.Join(tampered, "evidence/readiness.json"), readinessPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyRehearsalPreflight(context.Background(), tampered, repo); err == nil {
		t.Fatal("deleted failed listener check plus reseal created readiness")
	}
}

func checkStateFromFailures(failures []string, wanted string) bool {
	for _, failure := range failures {
		if failure == wanted {
			return true
		}
	}
	return false
}

func refusedPreflightConfig(root, repo string, draftFailure bool) RehearsalPreflightConfig {
	run := func(_ context.Context, _ string, name string, args ...string) (string, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "rev-parse HEAD^{tree}"):
			return strings.Repeat("2", 40), nil
		case strings.Contains(joined, "rev-parse HEAD"):
			return rehearsalTestHead, nil
		case strings.Contains(joined, "--format=%P"):
			return RequiredRehearsalParent, nil
		case strings.Contains(joined, "status --porcelain"):
			return "", nil
		case strings.Contains(joined, "remote get-url"):
			return ExpectedRemoteURL, nil
		case strings.Contains(joined, "ls-remote"):
			return "", nil
		default:
			return "", errors.New("unexpected command: " + joined)
		}
	}
	return RehearsalPreflightConfig{
		DataRoot: root,
		RepoRoot: repo,
		run:      run,
		pr: func(context.Context, string) (rehearsalPR, error) {
			if draftFailure {
				return rehearsalPR{}, errors.New("missing draft")
			}
			return rehearsalPR{Number: 27, URL: "https://github.com/PaulOctopusZLWB/dota2-ob/pull/27", Head: rehearsalTestHead, Draft: true}, nil
		},
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(dir, "../.."))
}

func freshIsolatedPath(t *testing.T, prefix string) string {
	t.Helper()
	parent, err := os.MkdirTemp("/var/tmp", prefix+"-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	return parent
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

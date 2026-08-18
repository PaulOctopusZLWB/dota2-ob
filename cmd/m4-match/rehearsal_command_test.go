package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/m4match"
)

func TestRehearsalAttemptReceiptAndExitStatusAgree(t *testing.T) {
	previousAttempt, previousVerify := runRehearsalAttempt, runRehearsalVerify
	t.Cleanup(func() { runRehearsalAttempt, runRehearsalVerify = previousAttempt, previousVerify })
	cases := []struct {
		name   string
		result m4match.RehearsalAttemptResultV1
		verify error
		status int
	}{
		{"refused", m4match.RehearsalAttemptResultV1{Outcome: "rehearsal_failed", FailureCode: "preflight_refused", TerminalSHA256: "failed", CleanupToken: "failed"}, nil, 1},
		{"typed_failure", m4match.RehearsalAttemptResultV1{Outcome: "rehearsal_failed", FailureCode: "dota_identity_unavailable", TerminalSHA256: "failed", CleanupToken: "failed"}, nil, 1},
		{"verified_completion", m4match.RehearsalAttemptResultV1{Outcome: "rehearsal_completed", TerminalSHA256: "complete", CleanupToken: "complete"}, nil, 0},
		{"unverified_completion", m4match.RehearsalAttemptResultV1{Outcome: "rehearsal_completed", TerminalSHA256: "complete", CleanupToken: "complete"}, errors.New("verification failed"), 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			runRehearsalAttempt = func(context.Context, m4match.RehearsalAttemptConfig) (m4match.RehearsalAttemptResultV1, error) {
				return test.result, nil
			}
			runRehearsalVerify = func(context.Context, string, string) (m4match.RehearsalAttemptResultV1, error) {
				return test.result, test.verify
			}
			var stdout, stderr bytes.Buffer
			if status := run([]string{"rehearsal-attempt", "--data-root", "/var/tmp/command-status"}, &stdout, &stderr); status != test.status {
				t.Fatalf("status=%d want=%d stderr=%q", status, test.status, stderr.String())
			}
			var receipt m4match.RehearsalAttemptResultV1
			if json.Unmarshal(stdout.Bytes(), &receipt) != nil || receipt != test.result {
				t.Fatalf("receipt=%q want=%#v", stdout.String(), test.result)
			}
		})
	}
}

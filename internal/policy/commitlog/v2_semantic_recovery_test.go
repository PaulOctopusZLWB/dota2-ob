package commitlog_test

import (
	"bytes"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
)

type storedSemanticStepV2 struct {
	name string
	kind string
	time int64
}

type storedSemanticEngineV2 struct {
	checkpoint contracts.PolicyCheckpointV2
}

func TestV2RecoveredAndUninterruptedSemanticMatrixCrossesProductionStorage(t *testing.T) {
	steps := []storedSemanticStepV2{
		{name: "01-accepted-disable", kind: "accepted_disable", time: 10},
		{name: "02-queue-change", kind: "queue", time: 11},
		{name: "03-pin", kind: "pin", time: 12},
		{name: "04-active-primary", kind: "show", time: 13},
		{name: "05-emergency-hide", kind: "emergency_hide", time: 14},
		{name: "06-clear-emergency", kind: "clear_emergency", time: 15},
		{name: "07-unpin", kind: "unpin", time: 16},
		{name: "08-expiry", kind: "expiry", time: 20},
		{name: "09-rejected-enable-stale", kind: "rejected_enable_stale", time: 21},
		{name: "10-rejected-disable-out-of-order", kind: "rejected_disable_out_of_order", time: 19},
		{name: "11-accepted-enable", kind: "accepted_enable", time: 22},
		{name: "12-tombstone-expiry", kind: "tombstone_expiry", time: 40},
	}
	const checkpointAfter = 5
	manifest := validManifestForStore()

	uninterruptedRoot := t.TempDir()
	uninterruptedStore, _, err := commitlog.OpenV2(uninterruptedRoot, "session", manifest, commitlog.WithV2ReplayVerifier(storedMatrixVerifierV2(t, nil)))
	if err != nil {
		t.Fatal(err)
	}
	uninterruptedEngine := newStoredSemanticEngineV2(manifest)
	uninterruptedBytes := appendStoredSemanticStepsV2(t, uninterruptedStore, uninterruptedEngine, manifest, steps)
	if err := uninterruptedStore.Close(); err != nil {
		t.Fatal(err)
	}

	recoveredRoot := t.TempDir()
	recoveredStore, _, err := commitlog.OpenV2(recoveredRoot, "session", manifest, commitlog.WithV2ReplayVerifier(storedMatrixVerifierV2(t, nil)))
	if err != nil {
		t.Fatal(err)
	}
	recoveredEngine := newStoredSemanticEngineV2(manifest)
	recoveredBytes := appendStoredSemanticStepsV2(t, recoveredStore, recoveredEngine, manifest, steps[:checkpointAfter])
	checkpoint := recoveredEngine.checkpoint
	checkpoint.ReferencedCommitSHA256 = recoveredBytes[len(recoveredBytes)-1].hash
	if err := recoveredStore.WriteCheckpoint(checkpoint); err != nil {
		t.Fatalf("write production checkpoint: %v", err)
	}
	if err := recoveredStore.Close(); err != nil {
		t.Fatal(err)
	}

	replayEngine := newStoredSemanticEngineV2(manifest)
	reopened, recoveredState, err := commitlog.OpenV2(recoveredRoot, "session", manifest, commitlog.WithV2ReplayVerifier(storedMatrixVerifierV2(t, steps[:checkpointAfter], replayEngine)))
	if err != nil {
		t.Fatalf("production reopen: %v", err)
	}
	laterCount := 0
	loaded, err := reopened.LoadCheckpoint(func(commitlog.CommittedV2) error { laterCount++; return nil })
	if err != nil || loaded == nil || laterCount != 0 {
		t.Fatalf("load production checkpoint loaded=%v later=%d err=%v", loaded != nil, laterCount, err)
	}
	wantCheckpoint, _ := contracts.MarshalCanonical(checkpoint)
	gotCheckpoint, _ := contracts.MarshalCanonical(*loaded)
	if !bytes.Equal(gotCheckpoint, wantCheckpoint) {
		t.Fatalf("loaded checkpoint differs\nwant %s\n got %s", wantCheckpoint, gotCheckpoint)
	}
	if recoveredState.StateHash != checkpoint.StateHash || replayEngine.checkpoint.StateHash != checkpoint.StateHash {
		t.Fatalf("verified activation state=%s replay=%s checkpoint=%s", recoveredState.StateHash, replayEngine.checkpoint.StateHash, checkpoint.StateHash)
	}
	recoveredEngine.checkpoint = *loaded
	recoveredBytes = append(recoveredBytes, appendStoredSemanticStepsV2(t, reopened, recoveredEngine, manifest, steps[checkpointAfter:])...)

	duplicate, found, err := reopened.LookupCommand("command-01-accepted-disable")
	if err != nil || !found || duplicate.Status != contracts.CommandAccepted || duplicate.ResultingRevision != 1 {
		t.Fatalf("recovered idempotency lookup result=%#v found=%v err=%v", duplicate, found, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	finalReplayEngine := newStoredSemanticEngineV2(manifest)
	finalStore, finalState, err := commitlog.OpenV2(recoveredRoot, "session", manifest, commitlog.WithV2ReplayVerifier(storedMatrixVerifierV2(t, steps, finalReplayEngine)))
	if err != nil {
		t.Fatalf("final production reopen: %v", err)
	}
	defer finalStore.Close()
	laterCount = 0
	loaded, err = finalStore.LoadCheckpoint(func(commitlog.CommittedV2) error { laterCount++; return nil })
	if err != nil || loaded == nil || laterCount != len(steps)-checkpointAfter {
		t.Fatalf("checkpoint/later-frame recovery loaded=%v later=%d err=%v", loaded != nil, laterCount, err)
	}

	if len(uninterruptedBytes) != len(recoveredBytes) {
		t.Fatalf("commit count uninterrupted=%d recovered=%d", len(uninterruptedBytes), len(recoveredBytes))
	}
	for i := range uninterruptedBytes {
		if !bytes.Equal(uninterruptedBytes[i].canonical, recoveredBytes[i].canonical) || uninterruptedBytes[i].hash != recoveredBytes[i].hash {
			t.Fatalf("%s recovered commit differs\nwant %s\n got %s", steps[i].name, uninterruptedBytes[i].canonical, recoveredBytes[i].canonical)
		}
	}
	wantState, _ := contracts.MarshalCanonical(uninterruptedEngine.checkpoint.StateProjection())
	gotState, _ := contracts.MarshalCanonical(finalReplayEngine.checkpoint.StateProjection())
	if !bytes.Equal(gotState, wantState) || finalState.StateHash != uninterruptedEngine.checkpoint.StateHash || finalReplayEngine.checkpoint.StateHash != uninterruptedEngine.checkpoint.StateHash {
		t.Fatalf("final production recovery differs\nwant_hash=%s state_hash=%s replay_hash=%s\nwant=%s\n got=%s", uninterruptedEngine.checkpoint.StateHash, finalState.StateHash, finalReplayEngine.checkpoint.StateHash, wantState, gotState)
	}
	final := finalReplayEngine.checkpoint
	if final.PolicyRevision != 7 || final.LastPolicyTimeMS != 40 || len(final.CommandResults) != 9 || len(final.CandidateTombstones) != 0 || final.EmergencyHide || final.ActivePrimary != nil || len(final.Pins) != 0 || len(final.DisabledRuleIDs) != 0 {
		t.Fatalf("production matrix terminal semantics: %#v", final.StateProjection())
	}
}

type storedCommitBytesV2 struct {
	canonical []byte
	hash      string
}

func appendStoredSemanticStepsV2(t *testing.T, store *commitlog.StoreV2, engine *storedSemanticEngineV2, manifest contracts.PolicyLineageManifestV2, steps []storedSemanticStepV2) []storedCommitBytesV2 {
	t.Helper()
	result := make([]storedCommitBytesV2, 0, len(steps))
	for _, step := range steps {
		commit := engine.nextCommit(t, manifest, step)
		stored, err := store.Append(commit)
		if err != nil {
			t.Fatalf("append %s: %v", step.name, err)
		}
		if stored.Commit.CommandID != "" {
			engine.setLocator(stored.Locator)
		}
		canonical, _ := contracts.MarshalCanonical(stored.Commit)
		result = append(result, storedCommitBytesV2{canonical: canonical, hash: stored.Hash})
	}
	return result
}

func storedMatrixVerifierV2(t *testing.T, steps []storedSemanticStepV2, engines ...*storedSemanticEngineV2) commitlog.ReplayVerifierV2 {
	t.Helper()
	var engine *storedSemanticEngineV2
	if len(engines) > 0 {
		engine = engines[0]
	}
	index := 0
	return commitlog.ReplayVerifierV2{
		VerifyObservation: func(commit contracts.PolicyCommitV2) error {
			if commit.ObservationEvidence == nil || commit.ObservationEvidence.Sequence != commit.ObservationSequence || commit.RawRecordSHA256 != storedMatrixSHA(commit.ObservationSequence+100) || commit.LiveObservationSHA256 != storedMatrixSHA(commit.ObservationSequence+200) {
				return fmt.Errorf("lineage-bound observation mismatch at %d", commit.CommitSequence)
			}
			return nil
		},
		VerifyCommand: func(commit contracts.PolicyCommitV2) error {
			if commit.Command == nil || commit.CommandResult == nil || commit.Command.CommandID != commit.CommandID || commit.CommandResult.CommandID != commit.CommandID {
				return fmt.Errorf("complete command mismatch at %d", commit.CommitSequence)
			}
			return nil
		},
		Reevaluate: func(commit contracts.PolicyCommitV2) error {
			if index >= len(steps) || engine == nil {
				return fmt.Errorf("unexpected replay commit %d", commit.CommitSequence)
			}
			expected := engine.nextCommit(t, validManifestForStore(), steps[index])
			want, _ := contracts.MarshalCanonical(expected)
			got, _ := contracts.MarshalCanonical(commit)
			if !bytes.Equal(got, want) {
				return fmt.Errorf("%s canonical replay mismatch: want %s got %s", steps[index].name, want, got)
			}
			index++
			return nil
		},
	}
}

func newStoredSemanticEngineV2(manifest contracts.PolicyLineageManifestV2) *storedSemanticEngineV2 {
	return &storedSemanticEngineV2{checkpoint: contracts.PolicyCheckpointV2{
		SchemaVersion: contracts.PolicyCheckpointSchemaV2, LineageManifestID: manifest.MustContentID(), LineageManifestSHA256: manifest.MustContentID(), SessionID: "session",
		Preview: []contracts.InsightCandidateV1{}, DisabledRuleIDs: []string{}, Cooldowns: []contracts.RuleCooldownV2{}, Pins: []contracts.PolicyPinV2{},
		CommandResults: []contracts.PolicyCommandResultRefV2{}, CommandLocators: []contracts.PolicyCommandLocatorV2{}, CandidateTombstones: []contracts.PolicyCandidateTombstoneV2{},
	}}
}

func (e *storedSemanticEngineV2) nextCommit(t *testing.T, manifest contracts.PolicyLineageManifestV2, step storedSemanticStepV2) contracts.PolicyCommitV2 {
	t.Helper()
	priorRevision, priorHash := e.checkpoint.PolicyRevision, e.checkpoint.StateHash
	candidate := storedMatrixCandidateV2()
	decisions := []contracts.BroadcastDecisionV1{}
	audits := []contracts.AuditEventV1{}
	publication := contracts.PublicationUnchanged
	var command *contracts.OperatorCommandV1
	var commandResult *contracts.OperatorCommandResultV1
	var evidence *contracts.EvidenceRefV1
	var rawHash, observationHash string

	switch step.kind {
	case "queue":
		e.checkpoint.LastObservationSequence++
		e.checkpoint.LastPolicyTimeMS = step.time
		e.checkpoint.Preview = []contracts.InsightCandidateV1{candidate}
		decision := storedMatrixDecisionV2("decision-queue", candidate.CandidateID, "", e.checkpoint.PolicyRevision, contracts.DecisionRejected, contracts.DecisionQueued, step.time, "queued")
		decisions = append(decisions, decision)
		audits = append(audits, storedMatrixAuditV2("audit-queue", "candidate_queued", candidate.CandidateID, "", decision.DecisionID, step.time, "queued"))
		publication = contracts.PublicationSuppressedV2
	case "expiry":
		e.checkpoint.LastObservationSequence++
		e.checkpoint.LastPolicyTimeMS = step.time
		e.checkpoint.Preview = []contracts.InsightCandidateV1{}
		e.checkpoint.ActivePrimary = nil
		decision := storedMatrixDecisionV2("decision-expiry", candidate.CandidateID, "", e.checkpoint.PolicyRevision, contracts.DecisionShown, contracts.DecisionExpired, step.time, "expired")
		decisions = append(decisions, decision)
		audits = append(audits, storedMatrixAuditV2("audit-expiry", "candidate_expired", candidate.CandidateID, "", decision.DecisionID, step.time, "expired"))
		e.checkpoint.CandidateTombstones = []contracts.PolicyCandidateTombstoneV2{{CandidateID: candidate.CandidateID, RuleID: candidate.RuleVersion, TerminalDecisionID: decision.DecisionID, TerminalState: contracts.DecisionExpired, SuppressUntilPolicyTimeMS: 30}}
		publication = contracts.PublicationSuppressedV2
	case "tombstone_expiry":
		e.checkpoint.LastObservationSequence++
		e.checkpoint.LastPolicyTimeMS = step.time
		e.checkpoint.CandidateTombstones = contracts.ExpirePolicyTombstonesV2(e.checkpoint.CandidateTombstones, step.time)
		audits = append(audits, storedMatrixAuditV2("audit-tombstone-expiry", "tombstone_expired", candidate.CandidateID, "", "", step.time, "expired"))
		publication = contracts.PublicationSuppressedV2
	default:
		command, commandResult, decisions, audits, publication = e.applyCommand(t, step, candidate)
	}

	if command == nil {
		sequence := e.checkpoint.LastObservationSequence
		evidence = &contracts.EvidenceRefV1{RecordSchemaVersion: 2, SessionID: "session", Sequence: sequence, ReceiveTime: time.Unix(int64(sequence), 0).UTC(), Source: "gsi", ProviderVersion: contracts.Absent[int64](), RawPayloadSHA256: storedMatrixSHA(sequence)}
		rawHash, observationHash = storedMatrixSHA(sequence+100), storedMatrixSHA(sequence+200)
	}
	e.checkpoint.CommitSequence++
	e.checkpoint.CreatedTimeMS = e.checkpoint.LastPolicyTimeMS
	e.checkpoint.StateHash, _ = e.checkpoint.ComputeStateHash()
	commit := contracts.PolicyCommitV2{
		SchemaVersion: contracts.PolicyCommitSchemaV2, LineageManifestID: manifest.MustContentID(), LineageManifestSHA256: manifest.MustContentID(), SessionID: "session", CommitSequence: e.checkpoint.CommitSequence,
		PriorPolicyRevision: priorRevision, ResultingPolicyRevision: e.checkpoint.PolicyRevision, PriorStateHash: priorHash, ResultingStateHash: e.checkpoint.StateHash,
		ResultingObservationSequence: e.checkpoint.LastObservationSequence, ResultingPolicyTimeMS: e.checkpoint.LastPolicyTimeMS,
		Decisions: decisions, CommandResult: commandResult, AuditEvents: audits, Publication: publication,
	}
	if command != nil {
		commit.CommandID, commit.Command = command.CommandID, command
	} else {
		commit.ObservationSequence, commit.ObservationEvidence = evidence.Sequence, evidence
		commit.RawRecordSHA256, commit.LiveObservationSHA256 = rawHash, observationHash
	}
	if err := commit.Validate(); err != nil {
		t.Fatalf("%s generated commit: %v", step.name, err)
	}
	return commit
}

func (e *storedSemanticEngineV2) applyCommand(t *testing.T, step storedSemanticStepV2, candidate contracts.InsightCandidateV1) (*contracts.OperatorCommandV1, *contracts.OperatorCommandResultV1, []contracts.BroadcastDecisionV1, []contracts.AuditEventV1, string) {
	t.Helper()
	action, targetRule, targetCandidate := "", "", ""
	switch step.kind {
	case "accepted_disable", "rejected_disable_out_of_order":
		action, targetRule = contracts.ActionDisableRule, "rule.v1"
	case "accepted_enable", "rejected_enable_stale":
		action, targetRule = contracts.ActionEnableRule, "rule.v1"
	case "pin":
		action, targetCandidate = contracts.ActionPin, candidate.CandidateID
	case "unpin":
		action, targetCandidate = contracts.ActionUnpin, candidate.CandidateID
	case "show":
		action, targetCandidate = contracts.ActionShow, candidate.CandidateID
	case "emergency_hide":
		action = contracts.ActionEmergencyHide
	case "clear_emergency":
		action = contracts.ActionClearEmergencyHide
	}
	commandID := "command-" + step.name
	command := &contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: commandID, SessionID: "session", Action: action, TargetRuleID: targetRule, TargetCandidateID: targetCandidate, ExpectedPolicyRevision: e.checkpoint.PolicyRevision, PolicyTimeMS: step.time}
	accepted := step.kind != "rejected_enable_stale" && step.kind != "rejected_disable_out_of_order"
	if step.kind == "rejected_enable_stale" {
		command.ExpectedPolicyRevision--
	}
	previous, resulting := e.checkpoint.PolicyRevision, e.checkpoint.PolicyRevision
	status, reason := contracts.CommandRejected, "stale_revision"
	if accepted {
		status, reason = contracts.CommandAccepted, "accepted"
		resulting++
		e.checkpoint.PolicyRevision = resulting
		e.checkpoint.LastPolicyTimeMS = step.time
	}
	if step.kind == "rejected_disable_out_of_order" {
		reason = "out_of_order_policy_time"
	}
	result := &contracts.OperatorCommandResultV1{SchemaVersion: contracts.OperatorCommandResultSchemaV1, CommandID: commandID, SessionID: "session", Status: status, PreviousRevision: previous, ResultingRevision: resulting, DecisionIDs: []string{}, Reason: reason}
	decisions := []contracts.BroadcastDecisionV1{}
	publication := contracts.PublicationUnchanged
	if accepted {
		switch step.kind {
		case "accepted_disable":
			e.checkpoint.DisabledRuleIDs = []string{"rule.v1"}
		case "accepted_enable":
			e.checkpoint.DisabledRuleIDs = []string{}
		case "pin":
			e.checkpoint.Pins = []contracts.PolicyPinV2{{CandidateID: candidate.CandidateID, DecisionID: "decision-pin"}}
		case "unpin":
			e.checkpoint.Pins = []contracts.PolicyPinV2{}
		case "show":
			e.checkpoint.Preview = []contracts.InsightCandidateV1{}
			decision := storedMatrixDecisionV2("decision-show", candidate.CandidateID, commandID, resulting, contracts.DecisionQueued, contracts.DecisionShown, step.time, "shown")
			e.checkpoint.ActivePrimary = &contracts.PolicyActivePrimaryV2{Candidate: candidate, Decision: decision}
			decisions = append(decisions, decision)
			result.DecisionIDs = []string{decision.DecisionID}
			publication = contracts.PublicationPublish
		case "emergency_hide":
			e.checkpoint.EmergencyHide = true
			decision := storedMatrixDecisionV2("decision-emergency-hide", candidate.CandidateID, commandID, resulting, contracts.DecisionShown, contracts.DecisionEmergencyHidden, step.time, "emergency_hide")
			e.checkpoint.ActivePrimary = &contracts.PolicyActivePrimaryV2{Candidate: candidate, Decision: decision}
			decisions = append(decisions, decision)
			result.DecisionIDs = []string{decision.DecisionID}
			publication = contracts.PublicationHide
		case "clear_emergency":
			e.checkpoint.EmergencyHide = false
			decision := storedMatrixDecisionV2("decision-clear-emergency", candidate.CandidateID, commandID, resulting, contracts.DecisionEmergencyHidden, contracts.DecisionShown, step.time, "clear_emergency_hide")
			e.checkpoint.ActivePrimary = &contracts.PolicyActivePrimaryV2{Candidate: candidate, Decision: decision}
			decisions = append(decisions, decision)
			result.DecisionIDs = []string{decision.DecisionID}
			publication = contracts.PublicationPublish
		}
	}
	resultHash, _ := contracts.CanonicalSHA256(*result)
	e.checkpoint.CommandResults = append(e.checkpoint.CommandResults, contracts.PolicyCommandResultRefV2{CommandID: commandID, ResultSHA256: resultHash})
	sort.Slice(e.checkpoint.CommandResults, func(i, j int) bool {
		return e.checkpoint.CommandResults[i].CommandID < e.checkpoint.CommandResults[j].CommandID
	})
	audit := storedMatrixAuditV2("audit-"+step.name, "operator_command", "", commandID, "", step.time, reason)
	return command, result, decisions, []contracts.AuditEventV1{audit}, publication
}

func (e *storedSemanticEngineV2) setLocator(locator contracts.PolicyCommandLocatorV2) {
	e.checkpoint.CommandLocators = append(e.checkpoint.CommandLocators, locator)
	sort.Slice(e.checkpoint.CommandLocators, func(i, j int) bool {
		return e.checkpoint.CommandLocators[i].CommandID < e.checkpoint.CommandLocators[j].CommandID
	})
}

func storedMatrixCandidateV2() contracts.InsightCandidateV1 {
	return contracts.InsightCandidateV1{SchemaVersion: contracts.InsightCandidateSchemaV1, CandidateID: "candidate-1", SessionID: "session", RuleVersion: "rule.v1", ConfigVersion: "config.v1", LocalizationKey: "insight.matrix", Parameters: []contracts.TypedParameterV1{}, Evidence: []contracts.EvidenceRefV1{}, ObservedValues: []contracts.ObservedMetricV1{}, Confidence: "observed", Priority: 100, CreatedTimeMS: 11, ExpiryTimeMS: 20, SourceRequirements: []contracts.SourceRequirementV1{}, Availability: "available"}
}

func storedMatrixDecisionV2(id, candidateID, commandID string, revision uint64, prior, resulting string, policyTime int64, reason string) contracts.BroadcastDecisionV1 {
	return contracts.BroadcastDecisionV1{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: id, SessionID: "session", PolicyRevision: revision, CandidateID: candidateID, CommandID: commandID, PriorState: prior, ResultingState: resulting, PolicyTimeMS: policyTime, Reason: reason}
}

func storedMatrixAuditV2(id, eventType, candidateID, commandID, decisionID string, policyTime int64, reason string) contracts.AuditEventV1 {
	return contracts.AuditEventV1{SchemaVersion: contracts.AuditEventSchemaV1, EventID: id, SessionID: "session", EventType: eventType, PolicyTimeMS: policyTime, CandidateID: candidateID, CommandID: commandID, DecisionID: decisionID, Reason: reason}
}

func storedMatrixSHA(value uint64) string { return fmt.Sprintf("%064x", value) }

// Package policy owns deterministic broadcast selection and operator mutation.
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

type Config struct {
	Version                 string
	LineageID               string
	QueueLimit              int
	TombstoneMS             int64
	CooldownMS              int64
	AutoShowRules           []string
	CandidateConfigVersion  string
	AllowedRuleVersions     []string
	CandidateConfigArtifact contracts.PolicyArtifactIdentityV2
	CandidateRulesArtifact  contracts.PolicyArtifactIdentityV2
}

func DefaultConfig() Config {
	return Config{Version: "policy.v1", LineageID: strings.Repeat("f", 64), QueueLimit: contracts.MaxPreviewCandidates, TombstoneMS: 60_000, CooldownMS: 15_000, CandidateConfigVersion: "config.v1", AllowedRuleVersions: []string{"draft.v1", "item.v1", "lane.v1", "objective.v1", "readiness.v1"}}
}

type Engine struct {
	config         Config
	state          contracts.PolicyStateV2
	sequence       uint64
	hash           string
	commandCommits map[string]contracts.PolicyCommitV2
}

func New(sessionID string, config Config) *Engine {
	defaults := DefaultConfig()
	if config.Version == "" {
		config.Version = defaults.Version
	}
	if config.LineageID == "" {
		config.LineageID = defaults.LineageID
	}
	if config.QueueLimit <= 0 || config.QueueLimit > contracts.MaxPreviewCandidates {
		config.QueueLimit = defaults.QueueLimit
	}
	if config.TombstoneMS <= 0 {
		config.TombstoneMS = defaults.TombstoneMS
	}
	if config.CooldownMS <= 0 {
		config.CooldownMS = defaults.CooldownMS
	}
	if config.CandidateConfigVersion == "" {
		config.CandidateConfigVersion = defaults.CandidateConfigVersion
	}
	if len(config.AllowedRuleVersions) == 0 {
		config.AllowedRuleVersions = append([]string(nil), defaults.AllowedRuleVersions...)
	}
	sort.Strings(config.AllowedRuleVersions)
	e := &Engine{config: config, commandCommits: map[string]contracts.PolicyCommitV2{}}
	e.state = contracts.PolicyStateV2{SchemaVersion: contracts.PolicyStateSchemaV2, SessionID: sessionID, Preview: []contracts.InsightCandidateV1{}, DisabledRuleIDs: []string{}, Cooldowns: []contracts.RuleCooldownV2{}, Pins: []contracts.PolicyPinV2{}, CommandResults: []contracts.PolicyCommandResultRefV2{}, CandidateTombstones: []contracts.PolicyCandidateTombstoneV2{}}
	e.hash = stateHash(e.state)
	return e
}

// NewFromCheckpoint restores the complete pure semantic projection. The
// commit-log adapter must first validate the checkpoint anchor and locators.
func NewFromCheckpoint(checkpoint contracts.PolicyCheckpointV2, config Config) (*Engine, error) {
	if err := checkpoint.Validate(); err != nil {
		return nil, err
	}
	e := New(checkpoint.SessionID, config)
	if checkpoint.LineageManifestID != e.config.LineageID || checkpoint.LineageManifestSHA256 != e.config.LineageID {
		return nil, errors.New("policy checkpoint lineage mismatch")
	}
	e.state = cloneState(checkpoint.StateProjection())
	e.sequence = checkpoint.CommitSequence
	e.hash = checkpoint.StateHash
	return e, nil
}

func (e *Engine) State() contracts.PolicyStateV2 { return cloneState(e.state) }
func (e *Engine) StateHash() string              { return e.hash }

// ReplayCommit re-evaluates one committed causal input against restored state
// and requires exact canonical equivalence before recovery may continue.
func (e *Engine) ReplayCommit(committed contracts.PolicyCommitV2, candidates []contracts.InsightCandidateV1) error {
	if committed.SessionID != e.state.SessionID || committed.LineageManifestID != e.config.LineageID {
		return errors.New("replay lineage mismatch")
	}
	var replayed contracts.PolicyCommitV2
	if committed.Command != nil {
		replayed = e.ApplyCommand(*committed.Command)
	} else if committed.ObservationEvidence != nil {
		replayed = e.EvaluateObservation(committed.ObservationSequence, committed.RawRecordSHA256, committed.LiveObservationSHA256, *committed.ObservationEvidence, candidates, causalPolicyTime(committed))
	} else {
		return errors.New("replay missing causal input")
	}
	want, err := contracts.MarshalCanonical(committed)
	if err != nil {
		return err
	}
	got, err := contracts.MarshalCanonical(replayed)
	if err != nil {
		return err
	}
	if string(want) != string(got) {
		return errors.New("replay canonical mismatch")
	}
	return nil
}

func SortCandidates(values []contracts.InsightCandidateV1) {
	contracts.SortInsightCandidates(values)
}

func (e *Engine) EvaluateObservation(observationSequence uint64, rawRecordHash, liveHash string, evidence contracts.EvidenceRefV1, candidates []contracts.InsightCandidateV1, policyTimeMS int64) contracts.PolicyCommitV2 {
	priorHash, priorRevision := e.hash, e.state.PolicyRevision
	e.sequence++
	commit := e.baseCommit(priorHash, priorRevision)
	commit.ObservationSequence, commit.ObservationEvidence, commit.RawRecordSHA256, commit.LiveObservationSHA256 = observationSequence, &evidence, rawRecordHash, liveHash
	if observationSequence <= e.state.LastObservationSequence {
		commit.AuditEvents = []contracts.AuditEventV1{audit(e.state.SessionID, policyTimeMS, "observation_suppressed", "", "", "", "out_of_order_observation")}
		commit.Publication = contracts.PublicationSuppressedV2
		return e.finish(commit)
	}
	if policyTimeMS < e.state.LastPolicyTimeMS {
		commit.AuditEvents = []contracts.AuditEventV1{audit(e.state.SessionID, policyTimeMS, "observation_suppressed", "", "", "", "out_of_order_policy_time")}
		commit.Publication = contracts.PublicationSuppressedV2
		return e.finish(commit)
	}
	_, activeExpired, _ := e.expire(policyTimeMS, &commit, "", "")
	SortCandidates(candidates)
	changed := false
	for _, candidate := range candidates {
		reason := e.candidateSuppression(candidate, evidence, policyTimeMS)
		if reason == "" && len(e.state.Preview) >= e.config.QueueLimit {
			reason = "queue_full"
		}
		if reason != "" {
			commit.AuditEvents = append(commit.AuditEvents, audit(e.state.SessionID, policyTimeMS, "candidate_suppressed", candidate.CandidateID, "", "", reason))
			continue
		}
		e.state.Preview = append(e.state.Preview, candidate)
		SortCandidates(e.state.Preview)
		changed = true
		decision := contracts.BroadcastDecisionV1{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: id("queued", candidate.CandidateID, strconv.FormatUint(observationSequence, 10)), SessionID: e.state.SessionID, PolicyRevision: priorRevision + 1, CandidateID: candidate.CandidateID, PriorState: contracts.DecisionQueued, ResultingState: contracts.DecisionQueued, PolicyTimeMS: policyTimeMS, Reason: "approval_required"}
		commit.Decisions = append(commit.Decisions, decision)
		commit.AuditEvents = append(commit.AuditEvents, audit(e.state.SessionID, policyTimeMS, "candidate_queued", candidate.CandidateID, decision.DecisionID, "", "approval_required"))
	}
	e.state.LastObservationSequence, e.state.LastPolicyTimeMS = observationSequence, policyTimeMS
	if changed || len(commit.Decisions) > 0 {
		e.state.PolicyRevision++
	}
	if len(commit.AuditEvents) == 0 {
		commit.AuditEvents = []contracts.AuditEventV1{audit(e.state.SessionID, policyTimeMS, "observation_evaluated", "", "", "", "no_candidate")}
	}
	if activeExpired {
		commit.Publication = contracts.PublicationHide
	} else {
		commit.Publication = contracts.PublicationSuppressedV2
	}
	return e.finish(commit)
}

func (e *Engine) ApplyCommand(command contracts.OperatorCommandV1) contracts.PolicyCommitV2 {
	if existing, ok := e.commandCommits[command.CommandID]; ok {
		return existing
	}
	priorHash, priorRevision := e.hash, e.state.PolicyRevision
	e.sequence++
	commit := e.baseCommit(priorHash, priorRevision)
	commit.CommandID, commit.Command = command.CommandID, &command
	status, reason := contracts.CommandAccepted, "accepted"
	if command.ExpectedPolicyRevision != priorRevision {
		status, reason = contracts.CommandRejected, "stale_revision"
	} else if command.PolicyTimeMS < e.state.LastPolicyTimeMS {
		status, reason = contracts.CommandRejected, "out_of_order_policy_time"
	} else if err := command.Validate(); err != nil || command.SessionID != e.state.SessionID {
		status, reason = contracts.CommandRejected, "malformed_operator_command"
	}
	var decisions []contracts.BroadcastDecisionV1
	maintenanceChanged, activeExpired := false, false
	targetExpired := false
	var expiryAudits []contracts.AuditEventV1
	expiryDecisionCount := 0
	if status == contracts.CommandAccepted {
		maintenanceChanged, activeExpired, targetExpired = e.expire(command.PolicyTimeMS, &commit, command.CommandID, command.TargetCandidateID)
		expiryDecisions := append([]contracts.BroadcastDecisionV1(nil), commit.Decisions...)
		expiryDecisionCount = len(expiryDecisions)
		expiryAudits = append([]contracts.AuditEventV1(nil), commit.AuditEvents...)
		mutationDecisions, mutationReason, mutationStatus := e.mutate(command)
		decisions = append(expiryDecisions, mutationDecisions...)
		reason, status = mutationReason, mutationStatus
		if targetExpired && mutationStatus == contracts.CommandRejected && mutationReason == "invalid_target" {
			status = contracts.CommandAccepted
			reason = "candidate_expired"
		}
	}
	resultingRevision := priorRevision
	if status == contracts.CommandAccepted {
		resultingRevision++
		e.state.PolicyRevision = resultingRevision
	}
	if status == contracts.CommandAccepted || maintenanceChanged {
		e.state.LastPolicyTimeMS = command.PolicyTimeMS
	}
	for i := range decisions {
		decisions[i].PolicyRevision = resultingRevision
	}
	if e.state.ActivePrimary != nil {
		for _, decision := range decisions {
			if e.state.ActivePrimary.Candidate.CandidateID == decision.CandidateID {
				e.state.ActivePrimary.Decision = decision
			}
		}
	}
	result := contracts.OperatorCommandResultV1{SchemaVersion: contracts.OperatorCommandResultSchemaV1, CommandID: command.CommandID, SessionID: e.state.SessionID, Status: status, PreviousRevision: priorRevision, ResultingRevision: resultingRevision, Reason: reason}
	for _, decision := range decisions {
		result.DecisionIDs = append(result.DecisionIDs, decision.DecisionID)
	}
	sort.Strings(result.DecisionIDs)
	resultHash, _ := contracts.CanonicalSHA256(result)
	e.state.CommandResults = append(e.state.CommandResults, contracts.PolicyCommandResultRefV2{CommandID: command.CommandID, ResultSHA256: resultHash})
	sort.Slice(e.state.CommandResults, func(i, j int) bool { return e.state.CommandResults[i].CommandID < e.state.CommandResults[j].CommandID })
	commit.Decisions, commit.CommandResult = decisions, &result
	commit.AuditEvents = []contracts.AuditEventV1{audit(e.state.SessionID, command.PolicyTimeMS, "operator_command", command.TargetCandidateID, firstDecision(decisions), command.CommandID, reason)}
	commit.AuditEvents = append(commit.AuditEvents, expiryAudits...)
	transitionStart := expiryDecisionCount
	if expiryDecisionCount == 0 && transitionStart < len(decisions) {
		transitionStart++
	}
	for _, decision := range decisions[transitionStart:] {
		commit.AuditEvents = append(commit.AuditEvents, audit(e.state.SessionID, command.PolicyTimeMS, "policy_transition", decision.CandidateID, decision.DecisionID, command.CommandID, decision.Reason))
	}
	if e.state.EmergencyHide {
		commit.Publication = contracts.PublicationHide
	} else if activeExpired && e.state.ActivePrimary == nil {
		commit.Publication = contracts.PublicationHide
	} else if status == contracts.CommandAccepted && e.state.ActivePrimary != nil {
		commit.Publication = contracts.PublicationPublish
	} else {
		commit.Publication = contracts.PublicationSuppressedV2
	}
	commit = e.finish(commit)
	e.commandCommits[command.CommandID] = commit
	return commit
}

func (e *Engine) mutate(command contracts.OperatorCommandV1) ([]contracts.BroadcastDecisionV1, string, string) {
	switch command.Action {
	case contracts.ActionEmergencyHide:
		e.state.EmergencyHide = true
		return []contracts.BroadcastDecisionV1{e.emergencyDecision(command, true)}, "emergency_hide", contracts.CommandAccepted
	case contracts.ActionClearEmergencyHide:
		e.state.EmergencyHide = false
		return []contracts.BroadcastDecisionV1{e.emergencyDecision(command, false)}, "emergency_hide_cleared", contracts.CommandAccepted
	case contracts.ActionDisableRule:
		if contains(e.state.DisabledRuleIDs, command.TargetRuleID) {
			return nil, "rule_already_disabled", contracts.CommandRejected
		}
		if len(e.state.DisabledRuleIDs) >= contracts.MaxDisabledRules {
			return nil, "disabled_rule_capacity", contracts.CommandRejected
		}
		e.state.DisabledRuleIDs = append(e.state.DisabledRuleIDs, command.TargetRuleID)
		sort.Strings(e.state.DisabledRuleIDs)
		return e.removeRule(command.TargetRuleID, command.PolicyTimeMS, command.CommandID), "rule_disabled", contracts.CommandAccepted
	case contracts.ActionEnableRule:
		if !removeString(&e.state.DisabledRuleIDs, command.TargetRuleID) {
			return nil, "rule_not_disabled", contracts.CommandRejected
		}
		return nil, "rule_enabled", contracts.CommandAccepted
	}
	index := candidateIndex(e.state.Preview, command.TargetCandidateID)
	active := e.state.ActivePrimary != nil && e.state.ActivePrimary.Candidate.CandidateID == command.TargetCandidateID
	if index < 0 && !active {
		return nil, "invalid_target", contracts.CommandRejected
	}
	candidate := contracts.InsightCandidateV1{}
	prior := contracts.DecisionQueued
	if active {
		candidate = e.state.ActivePrimary.Candidate
		prior = e.state.ActivePrimary.Decision.ResultingState
	} else {
		candidate = e.state.Preview[index]
	}
	resulting := contracts.DecisionShown
	switch command.Action {
	case contracts.ActionApprove, contracts.ActionShow:
		if e.state.EmergencyHide {
			return nil, "emergency_hide_active", contracts.CommandRejected
		}
		if !active {
			e.state.Preview = append(e.state.Preview[:index], e.state.Preview[index+1:]...)
		}
	case contracts.ActionReject:
		resulting = contracts.DecisionRejected
		if !active {
			e.state.Preview = append(e.state.Preview[:index], e.state.Preview[index+1:]...)
		} else {
			e.state.ActivePrimary = nil
		}
		removePin(&e.state.Pins, candidate.CandidateID)
	case contracts.ActionPin:
		resulting = contracts.DecisionPinned
		if !containsPin(e.state.Pins, candidate.CandidateID) {
			e.state.Pins = append(e.state.Pins, contracts.PolicyPinV2{CandidateID: candidate.CandidateID, DecisionID: id("pin", candidate.CandidateID, command.CommandID)})
			sort.Slice(e.state.Pins, func(i, j int) bool { return e.state.Pins[i].CandidateID < e.state.Pins[j].CandidateID })
		}
	case contracts.ActionUnpin:
		if !removePin(&e.state.Pins, candidate.CandidateID) {
			return nil, "candidate_not_pinned", contracts.CommandRejected
		}
		resulting = prior
	default:
		return nil, "unsupported_action", contracts.CommandRejected
	}
	decision := contracts.BroadcastDecisionV1{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: id("decision", candidate.CandidateID, command.CommandID), SessionID: e.state.SessionID, CandidateID: candidate.CandidateID, CommandID: command.CommandID, PriorState: prior, ResultingState: resulting, PolicyTimeMS: command.PolicyTimeMS, Reason: command.Action}
	if command.Action == contracts.ActionApprove || command.Action == contracts.ActionShow {
		decisions := []contracts.BroadcastDecisionV1{}
		if e.state.ActivePrimary != nil && e.state.ActivePrimary.Candidate.CandidateID != candidate.CandidateID {
			old := e.state.ActivePrimary.Candidate
			superseded := contracts.BroadcastDecisionV1{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: id("superseded", old.CandidateID, command.CommandID), SessionID: e.state.SessionID, CandidateID: old.CandidateID, CommandID: command.CommandID, PriorState: e.state.ActivePrimary.Decision.ResultingState, ResultingState: contracts.DecisionSuperseded, PolicyTimeMS: command.PolicyTimeMS, Reason: "superseded_by_primary"}
			decisions = append(decisions, superseded)
			e.tombstone(old, superseded, command.PolicyTimeMS)
			e.setCooldown(old.RuleVersion, command.PolicyTimeMS)
			removePin(&e.state.Pins, old.CandidateID)
		}
		e.state.ActivePrimary = &contracts.PolicyActivePrimaryV2{Candidate: candidate, Decision: decision}
		e.setCooldown(candidate.RuleVersion, command.PolicyTimeMS)
		decisions = append(decisions, decision)
		return decisions, command.Action, contracts.CommandAccepted
	}
	if resulting == contracts.DecisionRejected {
		e.tombstone(candidate, decision, command.PolicyTimeMS)
	}
	if command.Action == contracts.ActionApprove || command.Action == contracts.ActionShow || command.Action == contracts.ActionReject {
		e.setCooldown(candidate.RuleVersion, command.PolicyTimeMS)
	}
	return []contracts.BroadcastDecisionV1{decision}, command.Action, contracts.CommandAccepted
}

func (e *Engine) expire(now int64, commit *contracts.PolicyCommitV2, commandID, targetCandidateID string) (bool, bool, bool) {
	changed := false
	activeExpired := false
	targetExpired := false
	priorTombstones := len(e.state.CandidateTombstones)
	e.state.CandidateTombstones = contracts.ExpirePolicyTombstonesV2(e.state.CandidateTombstones, now)
	changed = changed || len(e.state.CandidateTombstones) != priorTombstones
	priorCooldowns := len(e.state.Cooldowns)
	cooldowns := e.state.Cooldowns[:0]
	for _, cooldown := range e.state.Cooldowns {
		if cooldown.UntilPolicyTimeMS > now {
			cooldowns = append(cooldowns, cooldown)
		}
	}
	e.state.Cooldowns = cooldowns
	changed = changed || len(cooldowns) != priorCooldowns
	remaining := e.state.Preview[:0]
	for _, candidate := range e.state.Preview {
		if candidate.ExpiryTimeMS > now {
			remaining = append(remaining, candidate)
			continue
		}
		decision := contracts.BroadcastDecisionV1{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: id("expiry", candidate.CandidateID), SessionID: e.state.SessionID, PolicyRevision: e.state.PolicyRevision + 1, CandidateID: candidate.CandidateID, CommandID: commandID, PriorState: contracts.DecisionQueued, ResultingState: contracts.DecisionExpired, PolicyTimeMS: now, Reason: "expired"}
		commit.Decisions = append(commit.Decisions, decision)
		commit.AuditEvents = append(commit.AuditEvents, audit(e.state.SessionID, now, "candidate_expired", candidate.CandidateID, decision.DecisionID, commandID, "expired"))
		e.tombstone(candidate, decision, now)
		removePin(&e.state.Pins, candidate.CandidateID)
		changed = true
		targetExpired = targetExpired || candidate.CandidateID == targetCandidateID
	}
	e.state.Preview = remaining
	if e.state.ActivePrimary != nil && e.state.ActivePrimary.Candidate.ExpiryTimeMS <= now {
		candidate := e.state.ActivePrimary.Candidate
		decision := contracts.BroadcastDecisionV1{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: id("expiry", candidate.CandidateID), SessionID: e.state.SessionID, PolicyRevision: e.state.PolicyRevision + 1, CandidateID: candidate.CandidateID, CommandID: commandID, PriorState: e.state.ActivePrimary.Decision.ResultingState, ResultingState: contracts.DecisionExpired, PolicyTimeMS: now, Reason: "expired"}
		commit.Decisions = append(commit.Decisions, decision)
		commit.AuditEvents = append(commit.AuditEvents, audit(e.state.SessionID, now, "active_primary_expired", candidate.CandidateID, decision.DecisionID, commandID, "expired"))
		e.tombstone(candidate, decision, now)
		removePin(&e.state.Pins, candidate.CandidateID)
		e.state.ActivePrimary = nil
		activeExpired = true
		changed = true
		targetExpired = targetExpired || candidate.CandidateID == targetCandidateID
	}
	return changed, activeExpired, targetExpired
}

func (e *Engine) candidateSuppression(candidate contracts.InsightCandidateV1, evidence contracts.EvidenceRefV1, now int64) string {
	contentID, identityErr := contracts.InsightCandidateContentID(candidate)
	if identityErr != nil || contentID != candidate.CandidateID {
		return "candidate_identity_mismatch"
	}
	if candidate.Validate() != nil || len(candidate.Evidence) != 1 || !evidenceEqual(candidate.Evidence[0], evidence) {
		return "candidate_evidence_mismatch"
	}
	if candidate.ConfigVersion != e.config.CandidateConfigVersion || !contains(e.config.AllowedRuleVersions, candidate.RuleVersion) {
		return "candidate_artifact_mismatch"
	}
	if candidate.SessionID != e.state.SessionID || candidate.Availability != "available" {
		if candidate.Reason != "" {
			return candidate.Reason
		}
		return "candidate_unavailable"
	}
	if candidate.ExpiryTimeMS <= now {
		return "candidate_expired"
	}
	if contains(e.state.DisabledRuleIDs, candidate.RuleVersion) {
		return "rule_disabled"
	}
	for _, cooldown := range e.state.Cooldowns {
		if cooldown.RuleID == candidate.RuleVersion && cooldown.UntilPolicyTimeMS > now {
			return "rule_cooldown"
		}
	}
	if len(e.state.CandidateTombstones) >= contracts.MaxCandidateTombstones {
		return "candidate_index_capacity"
	}
	if candidateIndex(e.state.Preview, candidate.CandidateID) >= 0 || (e.state.ActivePrimary != nil && e.state.ActivePrimary.Candidate.CandidateID == candidate.CandidateID) {
		return "duplicate_candidate"
	}
	for _, existing := range e.state.Preview {
		if semanticCandidateEqual(existing, candidate) {
			return "duplicate_candidate"
		}
	}
	if e.state.ActivePrimary != nil && semanticCandidateEqual(e.state.ActivePrimary.Candidate, candidate) {
		return "duplicate_candidate"
	}
	for _, value := range e.state.CandidateTombstones {
		if value.CandidateID == candidate.CandidateID {
			return "duplicate_candidate"
		}
	}
	return ""
}

func causalPolicyTime(commit contracts.PolicyCommitV2) int64 {
	if len(commit.AuditEvents) > 0 {
		return commit.AuditEvents[0].PolicyTimeMS
	}
	return commit.ResultingPolicyTimeMS
}
func evidenceEqual(a, b contracts.EvidenceRefV1) bool {
	x, _ := contracts.MarshalCanonical(a)
	y, _ := contracts.MarshalCanonical(b)
	return string(x) == string(y)
}
func semanticCandidateEqual(a, b contracts.InsightCandidateV1) bool {
	a.CandidateID, b.CandidateID = "", ""
	a.Evidence, b.Evidence = nil, nil
	a.CreatedTimeMS, b.CreatedTimeMS = 0, 0
	a.ExpiryTimeMS, b.ExpiryTimeMS = 0, 0
	x, _ := contracts.MarshalCanonical(a)
	y, _ := contracts.MarshalCanonical(b)
	return string(x) == string(y)
}

func (e *Engine) tombstone(candidate contracts.InsightCandidateV1, decision contracts.BroadcastDecisionV1, now int64) {
	e.state.CandidateTombstones, _ = contracts.InsertPolicyTombstoneV2(e.state.CandidateTombstones, contracts.PolicyCandidateTombstoneV2{CandidateID: candidate.CandidateID, RuleID: candidate.RuleVersion, TerminalDecisionID: decision.DecisionID, TerminalState: decision.ResultingState, SuppressUntilPolicyTimeMS: now + e.config.TombstoneMS})
}

func (e *Engine) setCooldown(rule string, now int64) {
	until := now + e.config.CooldownMS
	for i := range e.state.Cooldowns {
		if e.state.Cooldowns[i].RuleID == rule {
			e.state.Cooldowns[i].UntilPolicyTimeMS = until
			return
		}
	}
	e.state.Cooldowns = append(e.state.Cooldowns, contracts.RuleCooldownV2{RuleID: rule, UntilPolicyTimeMS: until})
	sort.Slice(e.state.Cooldowns, func(i, j int) bool { return e.state.Cooldowns[i].RuleID < e.state.Cooldowns[j].RuleID })
}
func (e *Engine) removeRule(rule string, now int64, commandID string) []contracts.BroadcastDecisionV1 {
	decisions := []contracts.BroadcastDecisionV1{}
	remaining := e.state.Preview[:0]
	for _, c := range e.state.Preview {
		if c.RuleVersion == rule {
			d := contracts.BroadcastDecisionV1{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: id("disabled", c.CandidateID, commandID), SessionID: e.state.SessionID, CandidateID: c.CandidateID, CommandID: commandID, PriorState: contracts.DecisionQueued, ResultingState: contracts.DecisionRejected, PolicyTimeMS: now, Reason: "rule_disabled"}
			e.tombstone(c, d, now)
			removePin(&e.state.Pins, c.CandidateID)
			decisions = append(decisions, d)
		} else {
			remaining = append(remaining, c)
		}
	}
	e.state.Preview = remaining
	if e.state.ActivePrimary != nil && e.state.ActivePrimary.Candidate.RuleVersion == rule {
		c := e.state.ActivePrimary.Candidate
		d := contracts.BroadcastDecisionV1{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: id("disabled", c.CandidateID, commandID), SessionID: e.state.SessionID, CandidateID: c.CandidateID, CommandID: commandID, PriorState: e.state.ActivePrimary.Decision.ResultingState, ResultingState: contracts.DecisionSuperseded, PolicyTimeMS: now, Reason: "rule_disabled"}
		e.tombstone(c, d, now)
		removePin(&e.state.Pins, c.CandidateID)
		decisions = append(decisions, d)
		e.state.ActivePrimary = nil
	}
	return decisions
}

func (e *Engine) emergencyDecision(command contracts.OperatorCommandV1, hide bool) contracts.BroadcastDecisionV1 {
	candidate, prior, resulting, reason := "", contracts.DecisionQueued, contracts.DecisionEmergencyHidden, "emergency_hide"
	if e.state.ActivePrimary != nil {
		candidate = e.state.ActivePrimary.Candidate.CandidateID
		prior = e.state.ActivePrimary.Decision.ResultingState
	}
	if !hide {
		prior, resulting, reason = contracts.DecisionEmergencyHidden, contracts.DecisionShown, "emergency_hide_cleared"
	}
	d := contracts.BroadcastDecisionV1{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: id(reason, candidate, command.CommandID), SessionID: e.state.SessionID, CandidateID: candidate, CommandID: command.CommandID, PriorState: prior, ResultingState: resulting, PolicyTimeMS: command.PolicyTimeMS, Reason: reason}
	if e.state.ActivePrimary != nil {
		e.state.ActivePrimary.Decision = d
	}
	return d
}
func (e *Engine) baseCommit(prior string, revision uint64) contracts.PolicyCommitV2 {
	return contracts.PolicyCommitV2{SchemaVersion: contracts.PolicyCommitSchemaV2, LineageManifestID: e.config.LineageID, LineageManifestSHA256: e.config.LineageID, SessionID: e.state.SessionID, CommitSequence: e.sequence, PriorPolicyRevision: revision, PriorStateHash: prior, Decisions: []contracts.BroadcastDecisionV1{}, AuditEvents: []contracts.AuditEventV1{}}
}
func (e *Engine) finish(commit contracts.PolicyCommitV2) contracts.PolicyCommitV2 {
	e.hash = stateHash(e.state)
	commit.ResultingPolicyRevision = e.state.PolicyRevision
	commit.ResultingStateHash = e.hash
	commit.ResultingObservationSequence = e.state.LastObservationSequence
	commit.ResultingPolicyTimeMS = e.state.LastPolicyTimeMS
	return commit
}

func stateHash(state contracts.PolicyStateV2) string {
	value, _ := contracts.CanonicalSHA256(state)
	return value
}
func id(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}
func audit(session string, now int64, kind, candidate, decision, command, reason string) contracts.AuditEventV1 {
	if candidate == "" && decision == "" && command == "" {
		candidate = "observation"
	}
	return contracts.AuditEventV1{SchemaVersion: contracts.AuditEventSchemaV1, EventID: id("audit", session, kind, candidate, decision, command, reason), SessionID: session, EventType: kind, PolicyTimeMS: now, CandidateID: candidate, DecisionID: decision, CommandID: command, Reason: reason}
}
func firstDecision(v []contracts.BroadcastDecisionV1) string {
	if len(v) > 0 {
		return v[0].DecisionID
	}
	return ""
}
func candidateIndex(v []contracts.InsightCandidateV1, id string) int {
	for i := range v {
		if v[i].CandidateID == id {
			return i
		}
	}
	return -1
}
func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}
func removeString(v *[]string, s string) bool {
	for i, x := range *v {
		if x == s {
			*v = append((*v)[:i], (*v)[i+1:]...)
			return true
		}
	}
	return false
}
func containsPin(v []contracts.PolicyPinV2, id string) bool {
	for _, x := range v {
		if x.CandidateID == id {
			return true
		}
	}
	return false
}
func removePin(v *[]contracts.PolicyPinV2, id string) bool {
	for i, x := range *v {
		if x.CandidateID == id {
			*v = append((*v)[:i], (*v)[i+1:]...)
			return true
		}
	}
	return false
}
func cloneState(s contracts.PolicyStateV2) contracts.PolicyStateV2 {
	payload, _ := contracts.MarshalCanonical(s)
	var out contracts.PolicyStateV2
	_ = contracts.DecodeStrict(payload, &out)
	return out
}

// Package policy owns deterministic broadcast selection and operator mutation.
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

type Config struct {
	Version       string
	LineageID     string
	QueueLimit    int
	TombstoneMS   int64
	CooldownMS    int64
	AutoShowRules []string
}

func DefaultConfig() Config {
	return Config{Version: "policy.v1", LineageID: strings.Repeat("f", 64), QueueLimit: contracts.MaxPreviewCandidates, TombstoneMS: 60_000, CooldownMS: 15_000}
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
	e := &Engine{config: config, commandCommits: map[string]contracts.PolicyCommitV2{}}
	e.state = contracts.PolicyStateV2{SchemaVersion: contracts.PolicyStateSchemaV2, SessionID: sessionID, Preview: []contracts.InsightCandidateV1{}, DisabledRuleIDs: []string{}, Cooldowns: []contracts.RuleCooldownV2{}, Pins: []contracts.PolicyPinV2{}, CommandResults: []contracts.PolicyCommandResultRefV2{}, CandidateTombstones: []contracts.PolicyCandidateTombstoneV2{}}
	e.hash = stateHash(e.state)
	return e
}

func (e *Engine) State() contracts.PolicyStateV2 { return cloneState(e.state) }
func (e *Engine) StateHash() string              { return e.hash }

func SortCandidates(values []contracts.InsightCandidateV1) {
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].Priority != values[j].Priority {
			return values[i].Priority > values[j].Priority
		}
		if confidence(values[i].Confidence) != confidence(values[j].Confidence) {
			return confidence(values[i].Confidence) > confidence(values[j].Confidence)
		}
		ti, tj := candidateEvidenceTime(values[i]), candidateEvidenceTime(values[j])
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		if values[i].RuleVersion != values[j].RuleVersion {
			return values[i].RuleVersion < values[j].RuleVersion
		}
		return values[i].CandidateID < values[j].CandidateID
	})
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
	e.expire(policyTimeMS, &commit)
	SortCandidates(candidates)
	changed := false
	for _, candidate := range candidates {
		reason := e.candidateSuppression(candidate, policyTimeMS)
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
		commit.AuditEvents = append(commit.AuditEvents, audit(e.state.SessionID, policyTimeMS, "candidate_queued", candidate.CandidateID, "", "", "approval_required"))
	}
	e.state.LastObservationSequence, e.state.LastPolicyTimeMS = observationSequence, policyTimeMS
	if changed || len(commit.Decisions) > 0 {
		e.state.PolicyRevision++
	}
	if len(commit.AuditEvents) == 0 {
		commit.AuditEvents = []contracts.AuditEventV1{audit(e.state.SessionID, policyTimeMS, "observation_evaluated", "", "", "", "no_candidate")}
	}
	commit.Publication = contracts.PublicationSuppressedV2
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
	if status == contracts.CommandAccepted {
		decisions, reason, status = e.mutate(command)
	}
	resultingRevision := priorRevision
	if status == contracts.CommandAccepted {
		resultingRevision++
		e.state.PolicyRevision = resultingRevision
		e.state.LastPolicyTimeMS = command.PolicyTimeMS
	}
	for i := range decisions {
		decisions[i].PolicyRevision = resultingRevision
	}
	if e.state.ActivePrimary != nil && len(decisions) > 0 && e.state.ActivePrimary.Candidate.CandidateID == decisions[0].CandidateID {
		e.state.ActivePrimary.Decision = decisions[0]
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
	if e.state.EmergencyHide {
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
		return nil, "emergency_hide", contracts.CommandAccepted
	case contracts.ActionClearEmergencyHide:
		e.state.EmergencyHide = false
		return nil, "emergency_hide_cleared", contracts.CommandAccepted
	case contracts.ActionDisableRule:
		if contains(e.state.DisabledRuleIDs, command.TargetRuleID) {
			return nil, "rule_already_disabled", contracts.CommandRejected
		}
		e.state.DisabledRuleIDs = append(e.state.DisabledRuleIDs, command.TargetRuleID)
		sort.Strings(e.state.DisabledRuleIDs)
		e.removeRule(command.TargetRuleID, command.PolicyTimeMS)
		return nil, "rule_disabled", contracts.CommandAccepted
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
		e.state.ActivePrimary = &contracts.PolicyActivePrimaryV2{Candidate: candidate, Decision: decision}
	}
	if resulting == contracts.DecisionRejected {
		e.tombstone(candidate, decision, command.PolicyTimeMS)
	}
	if command.Action == contracts.ActionApprove || command.Action == contracts.ActionShow || command.Action == contracts.ActionReject {
		e.setCooldown(candidate.RuleVersion, command.PolicyTimeMS)
	}
	return []contracts.BroadcastDecisionV1{decision}, command.Action, contracts.CommandAccepted
}

func (e *Engine) expire(now int64, commit *contracts.PolicyCommitV2) {
	e.state.CandidateTombstones = contracts.ExpirePolicyTombstonesV2(e.state.CandidateTombstones, now)
	cooldowns := e.state.Cooldowns[:0]
	for _, cooldown := range e.state.Cooldowns {
		if cooldown.UntilPolicyTimeMS > now {
			cooldowns = append(cooldowns, cooldown)
		}
	}
	e.state.Cooldowns = cooldowns
	remaining := e.state.Preview[:0]
	for _, candidate := range e.state.Preview {
		if candidate.ExpiryTimeMS > now {
			remaining = append(remaining, candidate)
			continue
		}
		decision := contracts.BroadcastDecisionV1{SchemaVersion: contracts.BroadcastDecisionSchemaV1, DecisionID: id("expiry", candidate.CandidateID), SessionID: e.state.SessionID, PolicyRevision: e.state.PolicyRevision + 1, CandidateID: candidate.CandidateID, PriorState: contracts.DecisionQueued, ResultingState: contracts.DecisionExpired, PolicyTimeMS: now, Reason: "expired"}
		commit.Decisions = append(commit.Decisions, decision)
		commit.AuditEvents = append(commit.AuditEvents, audit(e.state.SessionID, now, "candidate_expired", candidate.CandidateID, decision.DecisionID, "", "expired"))
		e.tombstone(candidate, decision, now)
	}
	e.state.Preview = remaining
}

func (e *Engine) candidateSuppression(candidate contracts.InsightCandidateV1, now int64) string {
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
	for _, value := range e.state.CandidateTombstones {
		if value.CandidateID == candidate.CandidateID {
			return "duplicate_candidate"
		}
	}
	return ""
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
func (e *Engine) removeRule(rule string, now int64) {
	remaining := e.state.Preview[:0]
	for _, c := range e.state.Preview {
		if c.RuleVersion == rule {
			d := contracts.BroadcastDecisionV1{DecisionID: id("disabled", c.CandidateID), ResultingState: contracts.DecisionRejected}
			e.tombstone(c, d, now)
		} else {
			remaining = append(remaining, c)
		}
	}
	e.state.Preview = remaining
	if e.state.ActivePrimary != nil && e.state.ActivePrimary.Candidate.RuleVersion == rule {
		e.state.ActivePrimary = nil
	}
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
func confidence(v string) int {
	switch v {
	case "high", "verified":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	}
	return 0
}
func candidateEvidenceTime(c contracts.InsightCandidateV1) (z time.Time) {
	if len(c.Evidence) > 0 {
		return c.Evidence[0].ReceiveTime
	}
	return z
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

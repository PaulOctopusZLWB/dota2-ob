package m4_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/delivery"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/gsi"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/policy/commitlog"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/presentation"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

const (
	fixtureSHA256 = "2c87c90fe9bb472ff8ad44efd5838b9ea20eab9b932f20df26719785cc4ae30e"
	origin        = "http://127.0.0.1:43211"
	token         = "m4-test-only-loopback-token"
)

type schedule struct {
	SchemaVersion string            `json:"schema_version"`
	SessionID     string            `json:"session_id"`
	Updates       []scheduledUpdate `json:"updates"`
}
type scheduledUpdate struct {
	ReceivedAt string          `json:"received_at"`
	PolicyTime int64           `json:"policy_time_ms"`
	Body       json.RawMessage `json:"body"`
}

type candidateSummary struct {
	Sequence     uint64 `json:"sequence"`
	CandidateID  string `json:"candidate_id"`
	RuleVersion  string `json:"rule_version"`
	Availability string `json:"availability"`
	Reason       string `json:"reason,omitempty"`
	SHA256       string `json:"sha256"`
}
type commitSummary struct {
	Sequence    uint64         `json:"sequence"`
	Publication string         `json:"publication"`
	CommandID   string         `json:"command_id,omitempty"`
	Result      string         `json:"result,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	Audit       []auditSummary `json:"audit"`
	SHA256      string         `json:"sha256"`
}
type auditSummary struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}
type replayOutput struct {
	FixtureSHA256     string                              `json:"fixture_sha256"`
	RawSHA256         []string                            `json:"raw_sha256"`
	ObservationSHA256 []string                            `json:"observation_sha256"`
	Candidates        []candidateSummary                  `json:"candidates"`
	Commits           []commitSummary                     `json:"commits"`
	CommandResults    []contracts.OperatorCommandResultV1 `json:"command_results"`
	OperatorState     delivery.OperatorState              `json:"operator_state"`
	OverlayStates     []contracts.OverlayStateV1          `json:"overlay_states"`
}

func TestCapturedGSIReplayIsDeterministicAcrossCleanExecutions(t *testing.T) {
	fixture := readSchedule(t)
	first := runSchedule(t, fixture)
	second := runSchedule(t, fixture)
	a, err := contracts.MarshalCanonical(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := contracts.MarshalCanonical(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two clean captured-GSI executions differ")
	}
	goldenPath := filepath.Join("testdata", "replay_output.golden.json")
	if os.Getenv("UPDATE_M4_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, a, 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		golden, err := os.ReadFile(goldenPath)
		if err != nil || !bytes.Equal(golden, a) {
			t.Fatalf("replay golden mismatch (set UPDATE_M4_GOLDEN=1 after review): %v", err)
		}
	}
	publications := map[string]bool{}
	for _, commit := range first.Commits {
		publications[commit.Publication] = true
	}
	for _, want := range []string{contracts.PublicationUnchanged, contracts.PublicationSuppressedV2, contracts.PublicationPublish, contracts.PublicationHide} {
		if !publications[want] {
			t.Fatalf("missing terminal publication %q: %#v", want, publications)
		}
	}
	firstResult, _ := contracts.MarshalCanonical(first.CommandResults[0])
	duplicateResult, _ := contracts.MarshalCanonical(first.CommandResults[1])
	if len(first.CommandResults) != 4 || first.CommandResults[0].Status != contracts.CommandAccepted ||
		!bytes.Equal(firstResult, duplicateResult) || first.CommandResults[2].Reason != "stale_revision" ||
		first.CommandResults[3].Status != contracts.CommandAccepted {
		t.Fatalf("command sequence mismatch: %#v", first.CommandResults)
	}
	if len(first.OverlayStates) != 2 || first.OverlayStates[0].Visibility != "visible" || first.OverlayStates[1].Visibility != "hidden" {
		t.Fatalf("overlay sequence mismatch: %#v", first.OverlayStates)
	}
}

func readSchedule(t *testing.T) schedule {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", "captured_gsi_schedule.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != fixtureSHA256 {
		t.Fatalf("fixture identity changed: %x", sum)
	}
	var value schedule
	if err := json.Unmarshal(payload, &value); err != nil || value.SchemaVersion != "m4_gsi_schedule.v1" || len(value.Updates) != 4 {
		t.Fatalf("invalid schedule: %v", err)
	}
	return value
}

func runSchedule(t *testing.T, fixture schedule) replayOutput {
	t.Helper()
	root := t.TempDir()
	times := make([]time.Time, len(fixture.Updates))
	for i, update := range fixture.Updates {
		parsed, err := time.Parse(time.RFC3339Nano, update.ReceivedAt)
		if err != nil {
			t.Fatal(err)
		}
		times[i] = parsed
	}
	clock := &fixtureClock{values: times}
	raw, err := session.NewStore(root, session.WithSessionID(fixture.SessionID), session.WithClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	history, lineage := liveOnlyLineage(fixture.SessionID)
	config := policy.DefaultConfig()
	config.LineageID = lineage.MustContentID()
	config.CandidateConfigVersion, config.CandidateConfigArtifact, config.CandidateRulesArtifact = lineage.Config.Version, lineage.Config, lineage.Rules
	verifier := commitlog.WithV3ReplayVerifier(commitlog.ReplayVerifierV3{
		VerifyObservation: func(contracts.PolicyCommitV3) error { return nil },
		VerifyCommand:     func(contracts.PolicyCommitV3) error { return nil },
		Reevaluate:        func(contracts.PolicyCommitV3) error { return nil },
	})
	policyStore, _, err := commitlog.OpenV3(root, fixture.SessionID, history, lineage, verifier)
	if err != nil {
		t.Fatal(err)
	}
	app, err := policy.NewBoundApplicationV3(policy.New(fixture.SessionID, config), policyStore, history, lineage)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fixtureRuntime{app: app, history: history, lineage: lineage, policyTimes: fixturePolicyTimes(fixture), nowMS: fixture.Updates[len(fixture.Updates)-1].PolicyTime}
	handler := gsi.NewServer(raw, gsi.WithLiveProjections(runtime))
	for _, update := range fixture.Updates {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/gsi", bytes.NewReader(update.Body)))
		if response.Code != http.StatusOK {
			t.Fatalf("GSI status=%d body=%s", response.Code, response.Body.String())
		}
	}
	handler.Wait()

	gateway, err := delivery.NewGateway(delivery.Config{
		BearerToken: token, AllowedOrigin: origin, Commands: commandPort{runtime}, Operator: operatorPort{runtime},
		Overlay: overlayPort{runtime}, ReadAsset: func(string) ([]byte, error) { return nil, os.ErrNotExist },
		Now: func() time.Time { return time.UnixMilli(runtime.nowMS).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	preview := runtime.firstAvailableCandidate()
	if preview == "" {
		t.Fatal("captured GSI schedule produced no approvable live-only candidate")
	}
	revision := runtime.app.State().PolicyRevision
	approve := contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "approve-live", SessionID: fixture.SessionID, Action: contracts.ActionApprove, TargetCandidateID: preview, ExpectedPolicyRevision: revision, PolicyTimeMS: runtime.nowMS + 1}
	approved := postCommand(t, gateway, approve)
	visible := getOverlay(t, gateway)
	duplicate := postCommand(t, gateway, approve)
	conflict := postCommand(t, gateway, contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "revision-conflict", SessionID: fixture.SessionID, Action: contracts.ActionEmergencyHide, ExpectedPolicyRevision: 0, PolicyTimeMS: runtime.nowMS + 2})
	hide := postCommand(t, gateway, contracts.OperatorCommandV1{SchemaVersion: contracts.OperatorCommandSchemaV1, CommandID: "emergency-hide", SessionID: fixture.SessionID, Action: contracts.ActionEmergencyHide, ExpectedPolicyRevision: runtime.app.State().PolicyRevision, PolicyTimeMS: runtime.nowMS + 3})
	hidden := getOverlay(t, gateway)
	operator := getOperator(t, gateway)

	output := replayOutput{
		FixtureSHA256: fixtureSHA256, RawSHA256: append([]string(nil), runtime.rawHashes...),
		ObservationSHA256: append([]string(nil), runtime.observationHashes...), Candidates: append([]candidateSummary(nil), runtime.candidates...),
		CommandResults: []contracts.OperatorCommandResultV1{approved, duplicate, conflict, hide}, OperatorState: operator,
		OverlayStates: []contracts.OverlayStateV1{visible, hidden},
	}
	if err := policyStore.VisitAll(func(committed commitlog.CommittedV3) error {
		summary := commitSummary{Sequence: committed.Commit.CommitSequence, Publication: committed.Commit.Publication, CommandID: committed.Commit.CommandID, SHA256: committed.Hash}
		if committed.Commit.CommandResult != nil {
			summary.Result, summary.Reason = committed.Commit.CommandResult.Status, committed.Commit.CommandResult.Reason
		}
		for _, audit := range committed.Commit.AuditEvents {
			summary.Audit = append(summary.Audit, auditSummary{Type: audit.EventType, Reason: audit.Reason})
		}
		output.Commits = append(output.Commits, summary)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := policyStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	return output
}

type fixtureClock struct {
	mu     sync.Mutex
	values []time.Time
	next   int
}

func (c *fixtureClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	value := c.values[c.next]
	if c.next < len(c.values)-1 {
		c.next++
	}
	return value
}

type fixtureRuntime struct {
	mu                sync.Mutex
	app               *policy.ApplicationV3
	history           contracts.HistoryAvailabilityBindingV1
	lineage           contracts.PolicyLineageManifestV3
	policyTimes       map[uint64]int64
	nowMS             int64
	previous          *contracts.LiveObservationV1
	overlay           contracts.OverlayStateV1
	rawHashes         []string
	observationHashes []string
	candidates        []candidateSummary
}

func (r *fixtureRuntime) Apply(ctx context.Context, record *session.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	observation, err := capture.MapLiveObservationV1(record)
	if err != nil {
		return err
	}
	policyTime := r.policyTimes[record.Sequence]
	candidates := insight.EvaluateLiveOnly(insight.LiveOnlyInput{Observation: observation, Previous: r.previous, History: r.history, Lineage: r.lineage, PolicyTimeMS: policyTime}, insight.DefaultConfig())
	liveHash, err := contracts.CanonicalSHA256(observation)
	if err != nil {
		return err
	}
	commit, err := r.app.EvaluateObservation(record.Sequence, observation.Evidence.RawPayloadSHA256, liveHash, observation.Evidence, candidates, policyTime)
	if err != nil {
		return err
	}
	r.rawHashes = append(r.rawHashes, observation.Evidence.RawPayloadSHA256)
	r.observationHashes = append(r.observationHashes, liveHash)
	for _, candidate := range candidates {
		hash, _ := contracts.CanonicalSHA256(candidate)
		r.candidates = append(r.candidates, candidateSummary{Sequence: record.Sequence, CandidateID: candidate.CandidateID, RuleVersion: candidate.RuleVersion, Availability: candidate.Availability, Reason: candidate.Reason, SHA256: hash})
	}
	copyObservation := observation
	r.previous = &copyObservation
	r.publish(commit)
	return nil
}

func (r *fixtureRuntime) Execute(_ context.Context, command contracts.OperatorCommandV1) (contracts.OperatorCommandResultV1, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	commit, err := r.app.ApplyCommand(command)
	if err != nil {
		return contracts.OperatorCommandResultV1{}, err
	}
	if commit.CommandResult == nil {
		return contracts.OperatorCommandResultV1{}, errors.New("missing command result")
	}
	if command.PolicyTimeMS > r.nowMS {
		r.nowMS = command.PolicyTimeMS
	}
	r.publish(commit)
	return *commit.CommandResult, nil
}

func (r *fixtureRuntime) publish(commit contracts.PolicyCommitV3) {
	state := r.app.State()
	if commit.Publication == contracts.PublicationHide || state.EmergencyHide || state.ActivePrimary == nil {
		r.overlay, _ = presentation.Hidden(state.SessionID, r.nowMS, r.nowMS+2000, "policy_hidden")
		return
	}
	deadline := state.ActivePrimary.Candidate.ExpiryTimeMS
	publication := r.nowMS
	if state.ActivePrimary.Decision.PolicyTimeMS > publication {
		publication = state.ActivePrimary.Decision.PolicyTimeMS
	}
	r.overlay, _ = presentation.Build(presentation.BuildInput{Locale: "zh-CN", Candidate: state.ActivePrimary.Candidate, Decision: state.ActivePrimary.Decision, PublicationTimeMS: publication, StaleDeadlineMS: deadline})
}

func (r *fixtureRuntime) firstAvailableCandidate() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, candidate := range r.app.State().Preview {
		if candidate.Availability == "available" {
			return candidate.CandidateID
		}
	}
	return ""
}

func (r *fixtureRuntime) CurrentOperator(context.Context) (delivery.OperatorState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.app.State()
	previews := make([]delivery.OperatorPreview, 0, len(state.Preview))
	for _, candidate := range state.Preview {
		claim, err := presentation.Preview("zh-CN", candidate)
		if err != nil {
			claim = presentation.UnavailablePreview("zh-CN")
		}
		previews = append(previews, delivery.OperatorPreview{CandidateID: candidate.CandidateID, RuleID: insight.Family(candidate.RuleVersion), RuleVersion: candidate.RuleVersion, Confidence: candidate.Confidence, SampleSize: candidate.SampleSize, ExpiresAtMS: candidate.ExpiryTimeMS, Claim: claim})
	}
	sort.Slice(previews, func(i, j int) bool { return previews[i].CandidateID < previews[j].CandidateID })
	return delivery.OperatorState{SchemaVersion: "operator_state.v1", SessionID: state.SessionID, PolicyRevision: state.PolicyRevision, EmergencyHidden: state.EmergencyHide, Previews: previews}, nil
}

func (r *fixtureRuntime) CurrentOverlay(context.Context) (contracts.OverlayStateV1, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.overlay, nil
}

type commandPort struct{ runtime *fixtureRuntime }

func (p commandPort) Execute(ctx context.Context, c contracts.OperatorCommandV1) (contracts.OperatorCommandResultV1, error) {
	return p.runtime.Execute(ctx, c)
}

type operatorPort struct{ runtime *fixtureRuntime }

func (p operatorPort) Current(ctx context.Context) (delivery.OperatorState, error) {
	return p.runtime.CurrentOperator(ctx)
}

type overlayPort struct{ runtime *fixtureRuntime }

func (p overlayPort) Current(ctx context.Context) (contracts.OverlayStateV1, error) {
	return p.runtime.CurrentOverlay(ctx)
}

func postCommand(t *testing.T, gateway http.Handler, command contracts.OperatorCommandV1) contracts.OperatorCommandResultV1 {
	t.Helper()
	payload, _ := contracts.MarshalCanonical(command)
	request := httptest.NewRequest(http.MethodPost, "/v1/operator/commands", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", delivery.JSONContentType)
	request.Header.Set(delivery.CSRFHeader, delivery.CSRFValue)
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("command status=%d body=%s", response.Code, response.Body.String())
	}
	var result contracts.OperatorCommandResultV1
	if err := contracts.DecodeStrict(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func getOverlay(t *testing.T, gateway http.Handler) contracts.OverlayStateV1 {
	t.Helper()
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/overlay/state", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("overlay status=%d body=%s", response.Code, response.Body.String())
	}
	var state contracts.OverlayStateV1
	if err := contracts.DecodeStrict(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func getOperator(t *testing.T, gateway http.Handler) delivery.OperatorState {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/operator/state", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Origin", origin)
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("operator status=%d body=%s", response.Code, response.Body.String())
	}
	var state delivery.OperatorState
	if err := contracts.DecodeStrict(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func fixturePolicyTimes(value schedule) map[uint64]int64 {
	result := make(map[uint64]int64, len(value.Updates))
	for i, update := range value.Updates {
		result[uint64(i+1)] = update.PolicyTime
	}
	return result
}

func liveOnlyLineage(sessionID string) (contracts.HistoryAvailabilityBindingV1, contracts.PolicyLineageManifestV3) {
	history := contracts.HistoryAvailabilityBindingV1{SchemaVersion: contracts.HistoryAvailabilityBindingSchemaV1, Mode: contracts.HistoryModeNoGo, TerminalOutcome: contracts.HistoricalNoGoOutcome, CodeFoundationCommit: contracts.AcceptedHistoryCodeCommit, EvidenceCommit: contracts.AcceptedHistoryEvidenceCommit, EvidenceIndexSHA256: contracts.AcceptedEvidenceIndexSHA256, ArtifactTreeSHA256: contracts.AcceptedArtifactTreeSHA256, ReplayGateAuditSHA256: contracts.AcceptedReplayGateAuditSHA256, SourceProvenanceSHA256: contracts.AcceptedSourceProvenanceSHA256, DisabledFamilies: contracts.HistoricalDisabledFamiliesV1(), TournamentScopeID: contracts.AcceptedTournamentScopeID, TournamentScopeSHA256: contracts.AcceptedTournamentScopeSHA256, Cutoff: "2026-08-12T00:00:00Z", Trailing90Start: "2026-05-14T00:00:00Z", Trailing180Start: "2026-02-13T00:00:00Z", PatchID: "60", DotaPatch: "7.41"}
	artifact := contracts.PolicyArtifactIdentityV2{Version: "m4.v1", ContentSHA256: strings.Repeat("a", 64)}
	bindingID := history.MustContentID()
	lineage := contracts.PolicyLineageManifestV3{SchemaVersion: contracts.PolicyLineageManifestSchemaV3, SessionID: sessionID, RawRecordSchema: artifact, RawRecordFraming: artifact, RawPayloadSchema: artifact, LiveObservationSchema: artifact, ProjectionMapping: artifact, TournamentScopeID: strings.Repeat("b", 64), TournamentScopeSHA256: strings.Repeat("b", 64), HistoryAvailabilityBindingID: bindingID, HistoryAvailabilityBindingSHA256: bindingID, Rules: insight.RulesArtifact(), Config: insight.ConfigArtifact(insight.DefaultConfig()), Catalog: artifact, Terminology: artifact, LocalizationParameterMapping: artifact, EngineBuild: artifact}
	return history, lineage
}

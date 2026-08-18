package m4match

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

const requiredRehearsalParent = "86a91b827e861703908843d0ccff4a8ef46a316e"

type rehearsalPreflightProbe struct {
	commit, parent, branch, remoteHead, prHead             string
	goExecutable, goExecutableSHA256                       string
	clean, ancestry, fixture, listener, toolchain, draftPR bool
}

var rehearsalPreflightInspector = inspectRehearsalPreflight

func RehearsalPreflight(ctx context.Context, config RehearsalPreflightConfig) (RehearsalPreflightV1, error) {
	repo, err := filepath.Abs(config.RepoRoot)
	if err != nil {
		return RehearsalPreflightV1{}, err
	}
	lease, err := acquireFreshRoot(config.DataRoot, repo)
	if err != nil {
		return RehearsalPreflightV1{}, err
	}
	defer lease.Close()
	probe := rehearsalPreflightInspector(ctx, repo)
	sessionID := "dot84-rehearsal-invalid"
	if len(probe.commit) == 40 {
		sessionID = "dot84-rehearsal-" + probe.commit[:12]
	}
	checksByID := map[string]RehearsalCheckV1{
		"accepted_ancestry":  {ID: "accepted_ancestry", Passed: probe.ancestry, Code: passCode(probe.ancestry, "accepted_functional_ancestry_missing")},
		"candidate_commit":   {ID: "candidate_commit", Passed: len(probe.commit) == 40, Code: passCode(len(probe.commit) == 40, "candidate_commit_invalid")},
		"candidate_parent":   {ID: "candidate_parent", Passed: probe.parent == requiredRehearsalParent, Code: passCode(probe.parent == requiredRehearsalParent, "candidate_parent_mismatch")},
		"captured_schedule":  {ID: "captured_schedule", Passed: probe.fixture, Code: passCode(probe.fixture, "captured_schedule_mismatch")},
		"clean_tree":         {ID: "clean_tree", Passed: probe.clean, Code: passCode(probe.clean, "worktree_dirty")},
		"draft_pr_head":      {ID: "draft_pr_head", Passed: probe.draftPR && probe.prHead == probe.commit, Code: passCode(probe.draftPR && probe.prHead == probe.commit, "draft_pr_head_mismatch")},
		"exclusive_listener": {ID: "exclusive_listener", Passed: probe.listener, Code: passCode(probe.listener, "capture_listener_occupied")},
		"remote_branch_head": {ID: "remote_branch_head", Passed: probe.remoteHead == probe.commit, Code: passCode(probe.remoteHead == probe.commit, "remote_branch_head_mismatch")},
		"rehearsal_identity": {ID: "rehearsal_identity", Passed: AcceptedRehearsalSpec == "1d793bc3d1d38b3ce3fd9e97005a5d4e45e928b2" && AcceptedP4Spec == "271cc47d503828528b7c69212deb4d22683cb715", Code: "ok"},
		"toolchain":          {ID: "toolchain", Passed: probe.toolchain, Code: passCode(probe.toolchain, "toolchain_unavailable")},
		"gsi_arm":            {ID: "gsi_arm", Passed: false, Code: "preflight_checks_failed_before_arm"},
	}
	preflight := RehearsalPreflightV1{
		SchemaVersion: RehearsalPreflightSchemaV1, Purpose: RehearsalPurpose, MatchClass: RehearsalMatchClass,
		AcceptedSpec: AcceptedRehearsalSpec, AcceptedP4Spec: AcceptedP4Spec, CandidateCommit: probe.commit, CandidateParent: probe.parent,
		SessionID: sessionID, ConsoleState: RehearsalReady, ClaimsP4: false, QualifyingMatch: false, AcceptanceEligible: false, AcceptanceGate: "none",
		GoExecutable: probe.goExecutable, GoExecutableSHA256: probe.goExecutableSHA256,
	}
	for _, dir := range []string{"rehearsal", filepath.Join("data", "sessions", sessionID)} {
		if err := rootMkdirAll(filepath.Join(lease.abs, dir), 0o700); err != nil {
			return RehearsalPreflightV1{}, err
		}
	}
	seedRawPath := filepath.Join(lease.abs, "data", "sessions", sessionID, "raw.jsonl")
	if err := rootWriteFile(seedRawPath, nil, 0o600); err != nil {
		return RehearsalPreflightV1{}, err
	}
	store, err := session.NewStore(filepath.Join(lease.abs, "data", "sessions"), session.WithSessionID(sessionID))
	if err != nil {
		return RehearsalPreflightV1{}, err
	}
	if err := store.Close(); err != nil {
		return RehearsalPreflightV1{}, err
	}
	rawPath := store.RawPath()
	rawInfo, err := os.Stat(rawPath)
	if err != nil || !rawInfo.Mode().IsRegular() {
		return RehearsalPreflightV1{}, errors.New("harness raw identity unavailable")
	}
	owner := RehearsalRootOwnerV1{
		SchemaVersion: "rehearsal_root_owner.v1", Purpose: RehearsalPurpose, SessionID: sessionID, CandidateCommit: probe.commit,
		RawRelativePath: filepath.ToSlash(filepath.Join("data", "sessions", sessionID, "raw.jsonl")),
	}
	preflight.RootOwnerSHA256, err = payloadSHAFromCanonical(owner)
	if err != nil {
		return RehearsalPreflightV1{}, err
	}
	if err := writeJSON(filepath.Join(lease.abs, "rehearsal", "owner.json"), owner, 0o600); err != nil {
		return RehearsalPreflightV1{}, err
	}
	if _, err := Prepare(lease.abs, 1920, 1080); err != nil {
		return RehearsalPreflightV1{}, err
	}
	readyForArm := true
	var armed *RehearsalArmV1
	rollback := func(primary error) (RehearsalPreflightV1, error) {
		if armed == nil {
			return RehearsalPreflightV1{}, primary
		}
		return preflight, errors.Join(primary, rollbackArmedGSI(lease.abs, *armed))
	}
	for id, check := range checksByID {
		if id != "gsi_arm" {
			readyForArm = readyForArm && check.Passed
		}
	}
	if readyForArm {
		arm, armErr := armRehearsalGSI(lease.abs, preflight)
		if armErr == nil {
			armed = &arm
			preflight.ArmSHA256, armErr = rehearsalArmBindingID(arm)
			if armErr != nil {
				return rollback(armErr)
			}
		}
		if armErr == nil {
			checksByID["gsi_arm"] = RehearsalCheckV1{ID: "gsi_arm", Passed: true, Code: "ok"}
		} else {
			checksByID["gsi_arm"] = RehearsalCheckV1{ID: "gsi_arm", Passed: false, Code: boundedFailure(armErr)}
		}
	}
	for _, id := range rehearsalCheckRegistry {
		check := checksByID[id]
		preflight.Checks = append(preflight.Checks, check)
		if !check.Passed {
			preflight.ConsoleState = RehearsalRefused
		}
	}
	if preflight.ConsoleState == RehearsalReady {
		preflight.HumanInstruction = RehearsalInstruction
	}
	if armed != nil {
		if err := rehearsalArmFault("preflight_after_arm"); err != nil {
			return rollback(err)
		}
		if err := rehearsalArmFault("preflight_content_id"); err != nil {
			return rollback(err)
		}
	}
	preflight.PreflightSHA256, err = rehearsalPreflightContentID(preflight)
	if err != nil {
		return rollback(err)
	}
	if armed != nil {
		if err := rehearsalArmFault("preflight_json"); err != nil {
			return rollback(err)
		}
	}
	if err := writeJSON(filepath.Join(lease.abs, "rehearsal", "preflight.json"), preflight, 0o600); err != nil {
		return rollback(err)
	}
	if armed != nil {
		if err := rehearsalArmFault("preflight_seal"); err != nil {
			return rollback(err)
		}
	}
	if err := writePrivate(filepath.Join(lease.abs, "rehearsal", "preflight.sha256"), []byte(preflight.PreflightSHA256+"\n")); err != nil {
		return rollback(err)
	}
	if armed != nil {
		if err := commitArmedGSI(lease.abs, *armed); err != nil {
			return rollback(err)
		}
	}
	return preflight, nil
}

func inspectRehearsalPreflight(ctx context.Context, repo string) rehearsalPreflightProbe {
	var p rehearsalPreflightProbe
	p.commit, _ = runText(ctx, repo, "git", "rev-parse", "HEAD")
	parents, _ := runText(ctx, repo, "git", "show", "-s", "--format=%P", "HEAD")
	if len(strings.Fields(parents)) == 1 {
		p.parent = parents
	}
	p.branch, _ = runText(ctx, repo, "git", "branch", "--show-current")
	status, statusErr := runText(ctx, repo, "git", "status", "--porcelain=v1", "--untracked-files=all")
	p.clean = statusErr == nil && status == ""
	_, ancestryErr := runText(ctx, repo, "git", "merge-base", "--is-ancestor", AcceptedFunctionalBase, "HEAD")
	p.ancestry = ancestryErr == nil
	fixtureHash, _, fixtureErr := fileSHA(filepath.Join(repo, "internal/integration/m4/testdata/captured_gsi_schedule.json"))
	p.fixture = fixtureErr == nil && fixtureHash == CapturedScheduleSHA256
	remote, _ := runText(ctx, repo, "git", "ls-remote", "origin", "refs/heads/"+p.branch)
	if fields := strings.Fields(remote); len(fields) == 2 {
		p.remoteHead = fields[0]
	}
	prJSON, prErr := runText(ctx, repo, "gh", "pr", "view", p.branch, "--json", "headRefOid,isDraft,state,title")
	if prErr == nil {
		var pr struct {
			Head  string `json:"headRefOid"`
			Draft bool   `json:"isDraft"`
			State string `json:"state"`
			Title string `json:"title"`
		}
		if json.Unmarshal([]byte(prJSON), &pr) == nil {
			p.prHead, p.draftPR = pr.Head, pr.Draft && pr.State == "OPEN" && strings.Contains(pr.Title, "DOT-87")
		}
	}
	listener, listenErr := net.Listen("tcp", CaptureAddress)
	if listenErr == nil {
		p.listener = true
		_ = listener.Close()
	}
	p.goExecutable, p.goExecutableSHA256, _ = resolveRehearsalGo()
	if p.goExecutable != "" {
		_, toolErr := runText(ctx, repo, p.goExecutable, "version")
		p.toolchain = toolErr == nil
	}
	return p
}

func rehearsalPreflightContentID(value RehearsalPreflightV1) (string, error) {
	value.PreflightSHA256 = ""
	return payloadSHAFromCanonical(value)
}

func payloadSHAFromCanonical(value any) (string, error) {
	payload, err := canonical(value)
	if err != nil {
		return "", err
	}
	return payloadSHA(payload), nil
}

func passCode(passed bool, failure string) string {
	if passed {
		return "ok"
	}
	return failure
}

func readRehearsalPreflight(root string) (RehearsalPreflightV1, error) {
	var value RehearsalPreflightV1
	payload, err := rootReadFile(filepath.Join(root, "rehearsal", "preflight.json"))
	if err != nil || json.Unmarshal(payload, &value) != nil {
		return value, errors.New("rehearsal preflight unavailable")
	}
	want, err := rehearsalPreflightContentID(value)
	seal, sealErr := rootReadFile(filepath.Join(root, "rehearsal", "preflight.sha256"))
	if err != nil || sealErr != nil || value.PreflightSHA256 != want || string(seal) != want+"\n" {
		return value, errors.New("rehearsal preflight seal mismatch")
	}
	if err := validateRehearsalPreflight(value); err != nil {
		return value, err
	}
	var owner RehearsalRootOwnerV1
	ownerPayload, ownerErr := rootReadFile(filepath.Join(root, "rehearsal", "owner.json"))
	if ownerErr != nil || json.Unmarshal(ownerPayload, &owner) != nil {
		return value, errors.New("rehearsal root owner unavailable")
	}
	ownerCanonical, canonicalErr := canonical(owner)
	ownerSHA, shaErr := payloadSHAFromCanonical(owner)
	if canonicalErr != nil || shaErr != nil || string(ownerCanonical) != string(ownerPayload) || ownerSHA != value.RootOwnerSHA256 || owner.SchemaVersion != "rehearsal_root_owner.v1" || owner.Purpose != RehearsalPurpose || owner.SessionID != value.SessionID || owner.CandidateCommit != value.CandidateCommit || owner.RawRelativePath != filepath.ToSlash(filepath.Join("data", "sessions", value.SessionID, "raw.jsonl")) {
		return value, errors.New("rehearsal root owner mismatch")
	}
	rawInfo, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(owner.RawRelativePath)))
	if statErr != nil || !rawInfo.Mode().IsRegular() {
		return value, errors.New("harness raw ownership changed")
	}
	return value, nil
}

func validateRehearsalPreflight(value RehearsalPreflightV1) error {
	if value.SchemaVersion != RehearsalPreflightSchemaV1 || value.Purpose != RehearsalPurpose || value.MatchClass != RehearsalMatchClass ||
		value.AcceptedSpec != AcceptedRehearsalSpec || value.AcceptedP4Spec != AcceptedP4Spec || value.ClaimsP4 || value.QualifyingMatch || value.AcceptanceEligible || value.AcceptanceGate != "none" || !validLowerSHA256(value.RootOwnerSHA256) || len(value.Checks) != len(rehearsalCheckRegistry) || (value.GoExecutable != "" && (!filepath.IsAbs(value.GoExecutable) || !validLowerSHA256(value.GoExecutableSHA256))) {
		return errors.New("rehearsal preflight contract mismatch")
	}
	ready := true
	for index, id := range rehearsalCheckRegistry {
		if value.Checks[index].ID != id || value.Checks[index].Code == "" {
			return errors.New("rehearsal check registry mismatch")
		}
		ready = ready && value.Checks[index].Passed
	}
	if ready && (value.ConsoleState != RehearsalReady || value.HumanInstruction != RehearsalInstruction) {
		return errors.New("ready rehearsal preflight state mismatch")
	}
	if ready && (!validLowerSHA256(value.ArmSHA256) || value.GoExecutable == "" || !validLowerSHA256(value.GoExecutableSHA256)) {
		return errors.New("ready rehearsal startup binding absent")
	}
	if !ready && (value.ConsoleState != RehearsalRefused || value.HumanInstruction != "") {
		return errors.New("refused rehearsal preflight state mismatch")
	}
	return nil
}

var _ = os.ErrNotExist

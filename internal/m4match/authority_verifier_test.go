package m4match

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthorityLocatorUsesDeclaredJSONPointerAndTokenBoundaries(t *testing.T) {
	payload := []byte(`{"result":{"games":[{"match_id":"123","note":"123","league_id":"14173"}]}}`)
	artifact := AuthorityArtifactV1{EndpointID: "valve_live_league_games", RawSHA256: payloadSHA(payload)}
	bindings, err := locateAuthorityFact("match_id", "123", artifact, payload)
	if err != nil || len(bindings) != 1 || bindings[0].JSONPointer != "/result/games/0/match_id" {
		t.Fatalf("declared pointer extraction=%+v err=%v", bindings, err)
	}
	bindings, _ = locateAuthorityFact("match_id", "12", artifact, payload)
	if len(bindings) != 0 {
		t.Fatal("substring/longer-token match passed")
	}
	bindings, _ = locateAuthorityFact("match_id", "123", AuthorityArtifactV1{EndpointID: "valve_dota2_event_record", RawSHA256: payloadSHA(payload)}, []byte("prefix123suffix"))
	if len(bindings) != 0 {
		t.Fatal("unrelated HTML token match passed")
	}
}

func TestAuthorityRootRejectsEstablishingRecordOriginSubstitution(t *testing.T) {
	root, err := EmbeddedAuthorityRoot()
	if err != nil {
		t.Fatal(err)
	}
	root.EstablishingRecords[0].URL = "https://www.dota2.com/not-the-declared-origin"
	if root.Validate() == nil {
		t.Fatal("establishing record origin substitution passed")
	}
}

func TestAuthorityEvidenceRootVerificationRejectsGraphMutations(t *testing.T) {
	for _, mutation := range []string{"none", "private_page", "artifact_hash", "locator", "missing_artifact", "terminal_counter", "evidence_hash"} {
		t.Run(mutation, func(t *testing.T) {
			root, readiness, harness := makeAuthorityEvidenceRoot(t, mutation)
			_, err := verifyAuthorityEvidenceRoot(root, ".", readiness, harness, "987654321")
			if mutation == "none" && err != nil {
				t.Fatalf("valid complete graph rejected: %v", err)
			}
			if mutation != "none" && err == nil {
				t.Fatalf("%s mutation passed", mutation)
			}
		})
	}
}

func makeAuthorityEvidenceRoot(t *testing.T, mutation string) (string, Readiness, Evidence) {
	t.Helper()
	root := filepath.Join("/var/tmp", fmt.Sprintf("dota2-ob-authority-test-%d-%d", os.Getpid(), time.Now().UnixNano()))
	lease, err := acquireFreshRoot(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupErr := acquireExistingRoot(root, ".")
		if cleanupErr == nil {
			_ = cleanup.removeAll()
			_ = cleanup.Close()
		}
	})
	selection := MatchAuthoritySelectionV1{MatchID: "987654321", LeagueID: "14173", LeagueOrEvent: "The Stockholm Major", Series: "upper-final", Game: "2", RadiantTeam: "Radiant Pro", DireTeam: "Dire Pro", ExpectedStartBegin: "2026-08-17T06:00:00Z", ExpectedStartEnd: "2026-08-17T06:20:00Z"}
	pagePayloads := [][]byte{
		[]byte(`{"result":{"games":[{"match_id":"987654321","league_id":"14173","league_name":"The Stockholm Major","series":"upper-final","game":"2","radiant_team":"Radiant Pro","dire_team":"Dire Pro","expected_start_begin":"2026-08-17T06:00:00Z","expected_start_end":"2026-08-17T06:20:00Z"}]}}`),
		[]byte("Valve Dota 2 esports event record: Stockholm Major presented by ESL."),
	}
	rootDefinition, _ := EmbeddedAuthorityRoot()
	evidence := MatchAuthorityEvidenceV1{SchemaVersion: AuthorityEvidenceSchemaVersion, AuthorityRootSHA256: EmbeddedAuthorityRootSHA256, Selection: selection, RetrievalStartedAt: "2026-08-17T05:59:00Z", RetrievalEndedAt: "2026-08-17T05:59:01Z", RetryCount: 0}
	pages := make([]fetchedAuthorityPage, 0, len(pagePayloads))
	for index, payload := range pagePayloads {
		endpoint := rootDefinition.EndpointTemplates[index]
		artifact := AuthorityArtifactV1{EndpointID: endpoint.ID, LogicalPageKey: endpoint.ID + ":0", Cursor: "terminal", PrivatePath: authorityPrivatePath(index), RawSHA256: payloadSHA(payload), SanitizedPath: filepath.ToSlash(filepath.Join("evidence/sanitized/authority", fmt.Sprintf("page-%02d.json", index))), FinalURL: endpoint.Origin + endpoint.Path, Status: 200, MediaType: "application/json", ContentEncoding: "identity", WireBytes: int64(len(payload)), DecodedBytes: int64(len(payload)), TransactionCount: 1, RetryCount: 0, PaginationTerminal: true}
		if index == 1 {
			artifact.MediaType = "text/html"
		}
		sanitized, _ := canonical(struct {
			SchemaVersion, EndpointID, RawSHA256 string
			Bytes                                int64
		}{"authority_sanitized_export.v1", endpoint.ID, artifact.RawSHA256, int64(len(payload))})
		artifact.SanitizedSHA256 = payloadSHA(sanitized)
		if err := writePrivate(filepath.Join(lease.abs, filepath.FromSlash(artifact.PrivatePath)), payload); err != nil {
			t.Fatal(err)
		}
		if err := writePrivate(filepath.Join(lease.abs, filepath.FromSlash(artifact.SanitizedPath)), sanitized); err != nil {
			t.Fatal(err)
		}
		pages = append(pages, fetchedAuthorityPage{artifact: artifact, decoded: payload})
		evidence.Artifacts = append(evidence.Artifacts, artifact)
		evidence.LogicalPages++
		evidence.Transactions++
		evidence.WireBytes += int64(len(payload))
		evidence.DecodedBytes += int64(len(payload))
	}
	facts := []struct{ name, value string }{{"match_id", selection.MatchID}, {"league_id", selection.LeagueID}, {"league_or_event", selection.LeagueOrEvent}, {"series", selection.Series}, {"game", selection.Game}, {"radiant_team", selection.RadiantTeam}, {"dire_team", selection.DireTeam}, {"expected_start_begin", selection.ExpectedStartBegin}, {"expected_start_end", selection.ExpectedStartEnd}}
	for _, fact := range facts {
		binding, bindErr := bindUniqueAuthorityFact(fact.name, fact.value, pages)
		if bindErr != nil {
			t.Fatal(bindErr)
		}
		evidence.FactBindings = append(evidence.FactBindings, binding)
	}
	evidence.ArtifactSetSHA256, _ = authorityArtifactSetSHA(evidence.Artifacts)
	if mutation == "artifact_hash" {
		evidence.Artifacts[0].RawSHA256 = string(make([]byte, 64))
	}
	if mutation == "locator" {
		evidence.FactBindings[0].JSONPointer = "/result/games/0/note"
	}
	if mutation == "missing_artifact" {
		evidence.Artifacts = evidence.Artifacts[:1]
	}
	if mutation == "terminal_counter" {
		evidence.Transactions++
	}
	evidencePayload, _ := canonical(evidence)
	if err := writePrivate(filepath.Join(lease.abs, "evidence/canonical/match-authority-evidence.json"), evidencePayload); err != nil {
		t.Fatal(err)
	}
	readiness := Readiness{CandidateCommit: "candidate", EvidenceIndexSHA256: payloadSHA([]byte("readiness-index"))}
	harness := Evidence{CandidateIdentity: CandidateIdentity{BinarySHA256: payloadSHA([]byte("binary"))}}
	preflight := MatchAuthorityPreflightV1{SchemaVersion: AuthorityPreflightSchemaVersion, CandidateCommit: readiness.CandidateCommit, BinarySHA256: harness.CandidateIdentity.BinarySHA256, AcceptedAmendmentCommit: AcceptedP4Spec, AuthorityRootSHA256: EmbeddedAuthorityRootSHA256, MatchID: selection.MatchID, ArtifactSetSHA256: evidence.ArtifactSetSHA256, AuthorityEvidenceSHA256: payloadSHA(evidencePayload), EvidenceIndexSHA256: readiness.EvidenceIndexSHA256, RetrievalLimitVersion: AuthorityRetrievalLimitVersion, TerminalLogicalPages: evidence.LogicalPages, TerminalTransactions: evidence.Transactions, TerminalWireBytes: evidence.WireBytes, TerminalDecodedBytes: evidence.DecodedBytes, RetryCount: 0}
	if mutation == "evidence_hash" {
		preflight.AuthorityEvidenceSHA256 = payloadSHA([]byte("substitute"))
	}
	if err := writeJSON(filepath.Join(lease.abs, "evidence/canonical/match-authority-preflight.json"), preflight, 0o600); err != nil {
		t.Fatal(err)
	}
	if mutation == "private_page" {
		if err := rootWriteFile(filepath.Join(lease.abs, authorityPrivatePath(0)), []byte(`{"unrelated":"page"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	return root, readiness, harness
}

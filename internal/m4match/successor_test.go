package m4match

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunClassificationClosedCrossProduct(t *testing.T) {
	legal := map[string]bool{
		"p4_acceptance/ti":                    true,
		"p4_acceptance/public_tournament":     true,
		"public_match_rehearsal/public_match": true,
	}
	purposes := []string{"", "p4_acceptance", "public_match_rehearsal", "unknown"}
	classes := []string{"", "ti", "public_tournament", "public_match", "unknown"}
	for _, purpose := range purposes {
		for _, class := range classes {
			_, err := ParseRunClassification(purpose, class)
			if (err == nil) != legal[purpose+"/"+class] {
				t.Fatalf("pair %q/%q legal=%v err=%v", purpose, class, legal[purpose+"/"+class], err)
			}
		}
	}
}

func TestPurposeContractsAreStructurallySeparatedAndNonAcceptance(t *testing.T) {
	p4 := RunClassificationV1{Purpose: PurposeP4Acceptance, Class: MatchClassPublicTournament}
	rehearsal := RunClassificationV1{Purpose: PurposePublicMatchRehearsal, Class: MatchClassPublicMatch}
	if p4.evidenceSchema() == rehearsal.evidenceSchema() || p4.readinessSchema() == rehearsal.readinessSchema() || p4.rootDomain() == rehearsal.rootDomain() {
		t.Fatal("purpose-specific contracts share an identity")
	}
	if rehearsal.consoleState() != "REHEARSAL_READY" || strings.Contains(rehearsal.instruction(), "ARMED") || strings.Contains(rehearsal.instruction(), "DOT-70") {
		t.Fatal("rehearsal emits an acceptance-capable instruction")
	}
	if err := validateNonAcceptanceFields(PurposePublicMatchRehearsal, false, false, false, "none"); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		claims, qualifying, eligible bool
		gate                         string
	}{{true, false, false, "none"}, {false, true, false, "none"}, {false, false, true, "none"}, {false, false, false, "manual_dot70_after_review"}} {
		if validateNonAcceptanceFields(PurposePublicMatchRehearsal, mutation.claims, mutation.qualifying, mutation.eligible, mutation.gate) == nil {
			t.Fatal("unsafe rehearsal fields passed")
		}
	}
}

func TestPurposeVerifiersRejectOppositeWireContracts(t *testing.T) {
	p4Class := RunClassificationV1{Purpose: PurposeP4Acceptance, Class: MatchClassPublicTournament}
	rehearsalClass := RunClassificationV1{Purpose: PurposePublicMatchRehearsal, Class: MatchClassPublicMatch}
	p4EvidencePayload, _ := canonical(Evidence{SchemaVersion: SchemaVersion})
	rehearsalEvidencePayload, _ := canonical(PublicMatchRehearsalEvidenceV1{Evidence: Evidence{SchemaVersion: RehearsalSchemaVersion}, RehearsalContractVersion: rehearsalContractV1, AcceptanceChecks: rehearsalAcceptanceChecks(), SuppressionAudits: rehearsalSuppressionAudits()})
	if _, _, err := decodeEvidenceContract(p4EvidencePayload, rehearsalClass); err == nil {
		t.Fatal("P4 evidence passed rehearsal verifier")
	}
	if _, _, err := decodeEvidenceContract(rehearsalEvidencePayload, p4Class); err == nil {
		t.Fatal("rehearsal evidence passed P4 verifier")
	}
	p4ReadinessPayload, _ := canonical(Readiness{SchemaVersion: ReadinessSchemaVersion})
	rehearsalReadinessPayload, _ := canonical(PublicMatchRehearsalReadinessV1{Readiness: Readiness{SchemaVersion: RehearsalReadinessSchemaVersion}, RehearsalContractVersion: rehearsalContractV1})
	if _, _, err := decodeReadinessContract(p4ReadinessPayload, rehearsalClass); err == nil {
		t.Fatal("P4 readiness passed rehearsal verifier")
	}
	if _, _, err := decodeReadinessContract(rehearsalReadinessPayload, p4Class); err == nil {
		t.Fatal("rehearsal readiness passed P4 verifier")
	}
}

func TestEmbeddedAuthorityRootCanonicalDigestAndClosedTrust(t *testing.T) {
	root, err := EmbeddedAuthorityRoot()
	if err != nil {
		t.Fatal(err)
	}
	if root.EventID == "" || len(root.TrustedOrigins) != 2 || payloadSHA([]byte(embeddedAuthorityRootJSON)) != EmbeddedAuthorityRootSHA256 {
		t.Fatal("embedded authority identity mismatch")
	}
	mutated := root
	mutated.TrustedOrigins = append(mutated.TrustedOrigins, "http://example.invalid")
	if mutated.Validate() == nil {
		t.Fatal("unsafe authority origin passed")
	}
	mutated = root
	mutated.EndpointTemplates[0].Origin = "https://example.invalid"
	if mutated.Validate() == nil {
		t.Fatal("caller-like authority endpoint passed")
	}
}

func TestSingleGzipMemberRejectsTrailingAndTruncation(t *testing.T) {
	var payload bytes.Buffer
	w := gzip.NewWriter(&payload)
	_, _ = w.Write([]byte("authority"))
	_ = w.Close()
	if !validSingleGzipMember(payload.Bytes()) {
		t.Fatal("valid gzip member rejected")
	}
	if validSingleGzipMember(append(append([]byte(nil), payload.Bytes()...), payload.Bytes()...)) {
		t.Fatal("trailing gzip member passed")
	}
	if validSingleGzipMember(payload.Bytes()[:payload.Len()-1]) {
		t.Fatal("truncated gzip passed")
	}
}

func TestAuthorityFetchUsesSecretHeaderWithoutRetentionAndEnforcesBounds(t *testing.T) {
	const secret = "private-web-api-key"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-WebAPI-Key") != secret {
			t.Error("secret header missing")
		}
		writer.Header().Set("Content-Encoding", "gzip")
		gzipWriter := gzip.NewWriter(writer)
		_, _ = gzipWriter.Write([]byte(`{"result":{"games":[]}}`))
		_ = gzipWriter.Close()
	}))
	defer server.Close()
	root := MatchAuthorityRootV1{TrustedOrigins: []string{server.URL}}
	endpoint := AuthorityEndpointV1{ID: "valve_live_league_games", Origin: server.URL, Path: "/"}
	page, err := (authorityFetcher{client: server.Client(), now: time.Now, webAPIKey: secret}).fetch(context.Background(), root, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(page.artifact)
	if bytes.Contains(payload, []byte(secret)) || strings.Contains(page.artifact.FinalURL, "?") || page.artifact.RetryCount != 0 {
		t.Fatalf("secret or retry leaked: %s", payload)
	}

	over := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.CopyN(writer, zeroReader{}, maxAuthorityResponseBytes+1)
	}))
	defer over.Close()
	_, err = (authorityFetcher{client: over.Client(), now: time.Now}).fetch(context.Background(), MatchAuthorityRootV1{TrustedOrigins: []string{over.URL}}, AuthorityEndpointV1{ID: "pgl_event_authority", Origin: over.URL, Path: "/"})
	if err == nil {
		t.Fatal("over-bound response passed")
	}
}

type zeroReader struct{}

func (zeroReader) Read(payload []byte) (int, error) {
	for index := range payload {
		payload[index] = 0
	}
	return len(payload), nil
}

func coverageFrame(t *testing.T, sequence uint64, raw string) CoverageFrameV1 {
	t.Helper()
	payload := json.RawMessage(raw)
	return CoverageFrameV1{Sequence: sequence, RawRecordSHA256: payloadSHA(payload), RawRecord: payload}
}

func TestFieldCoverageValueFreeUnionClassificationAndDynamicCollisions(t *testing.T) {
	baseline := []CoverageFrameV1{
		coverageFrame(t, 1, `{"map":{"clock":null,"only_baseline":true},"players":{"101":{"hp":1,"steamid":"sensitive-id"},"102":{"hp":2}},"items":[{"id":1},{"id":2}]}`),
		coverageFrame(t, 2, `{"map":{"clock":1},"players":{"101":{"hp":3}}}`),
	}
	rehearsal := []CoverageFrameV1{
		coverageFrame(t, 10, `{"map":{"clock":"secret-sensitive-value","extra":true},"players":{"201":{"hp":9},"202":{"hp":8}},"items":[]}`),
		coverageFrame(t, 11, `{"map":{"clock":2},"players":{"201":{"hp":7}}}`),
	}
	hash := strings.Repeat("a", 64)
	delta, err := GenerateFieldCoverageDelta(baseline, rehearsal, CapturedScheduleSHA256, strings.Repeat("c", 64), hash)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(delta)
	if bytes.Contains(payload, []byte("secret-sensitive-value")) || bytes.Contains(payload, []byte("sensitive-id")) || bytes.Contains(payload, []byte("steamid")) || bytes.Contains(payload, []byte(`"101"`)) || bytes.Contains(payload, []byte(`"201"`)) {
		t.Fatalf("coverage leaked a scalar or concrete dynamic key: %s", payload)
	}
	byPath := map[string]FieldCoveragePathDeltaV1{}
	for _, item := range delta.Paths {
		byPath[item.Path] = item
	}
	if byPath["/k:map/k:only_baseline"].Classification != "missing_in_rehearsal" || byPath["/k:map/k:extra"].Classification != "additional_in_rehearsal" || byPath["/k:map/k:clock"].Classification != "different_type_or_nullability" {
		t.Fatal("classification precedence mismatch")
	}
	players := byPath["/k:players/d:decimal"]
	if !players.Baseline.HasDynamicCollision || !players.Rehearsal.HasDynamicCollision || players.Baseline.DynamicCollisionCount != 1 || players.Rehearsal.DynamicCollisionCount != 1 {
		t.Fatalf("dynamic collisions=%+v", players)
	}
	if byPath["/k:items/a:[]/k:id"].Baseline.SeenCount != 2 {
		t.Fatal("array occurrences were not aggregated")
	}
}

func TestFieldCoverageRejectsFrameAndUnionAdversaries(t *testing.T) {
	valid := coverageFrame(t, 1, `{"a":null}`)
	hash := strings.Repeat("a", 64)
	for name, frames := range map[string][]CoverageFrameV1{
		"duplicate":      {valid, valid},
		"out of order":   {coverageFrame(t, 2, `{"a":1}`), valid},
		"hash mismatch":  {{Sequence: 1, RawRecordSHA256: strings.Repeat("0", 64), RawRecord: json.RawMessage(`{"a":1}`)}},
		"unclassifiable": {coverageFrame(t, 1, `{"bad key":1}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := GenerateFieldCoverageDelta(frames, []CoverageFrameV1{valid}, CapturedScheduleSHA256, hash, hash); err == nil {
				t.Fatal("adversary passed")
			}
		})
	}
	delta, err := GenerateFieldCoverageDelta([]CoverageFrameV1{valid}, []CoverageFrameV1{valid}, CapturedScheduleSHA256, hash, hash)
	if err != nil {
		t.Fatal(err)
	}
	delta.Paths = append(delta.Paths, delta.Paths[0])
	if ValidateFieldCoverageDelta(delta) == nil {
		t.Fatal("duplicate union path passed")
	}
	delta, _ = GenerateFieldCoverageDelta([]CoverageFrameV1{valid}, []CoverageFrameV1{valid}, CapturedScheduleSHA256, hash, hash)
	delta.CapturedScheduleSHA256 = hash
	if ValidateFieldCoverageDelta(delta) == nil {
		t.Fatal("baseline hash mismatch passed")
	}
}

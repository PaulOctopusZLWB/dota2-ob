package m4match

import (
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/presentation"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

const acceptedLiveOnlyScopeCommit = "264fed3cdef4ef845b815581b4011a6d293ecbaa"

var acceptedV3Sources = []string{
	"83e7016996cfcee081fe85bc7c2f6628b7257b100b1aa14079778d3cd55456a0",
	"2604f5422188b697591666201c70c8a41729ea6418755d2c2a2847aa451adae9",
	"34e4611547eb049b986cfe5a9709645df52cb4fb89c0a17d7973c13cabe0696f",
	"6b4797f0209ed843efef4d9bc51cbfb88a0c9b2fd5ddd943a679fdf3e21447b7",
	"6c9c390d3e72596a95d7c0ae7189ed2bf77c1fbc93ddc5342d6bcfaa2f055410",
	"607c1fe18153aedcd048ab267720dbbc47b33865621f285f2c6820da2e70f556",
	"61ebd6da3217ed953c854fbae28197690845b088efa16743fd74b724ffe9ccfa",
	"7417c1580323b2bb678d9415db4b5b6e62c1adfccb7bc8de62342956f45c4b30",
	"293adbb955df0c58d39db98d8efdd21f84d4dfb631355e3379926683cb695531",
	"8cd02d084a5ae7077364e5c93872b2aa00a0f13ae0fbaa9e20d1abdf5a3263c7",
	"022ac146cdea343cd3c438eab8be585862874914d33fb16e765edbbdd2a8f435",
	"2c2e4d0ece8f89f3b2488d2db3109e4e1c2921ad2d165baf733855ecc9c6f08b",
	"fd1d0f801c3cc43f7dc367ac95083d335082b0bccdd42a35eedf7711c10029af",
	"5678bc191e6e454694662488b4aa3af9950c1c14299aa70bc4babd3c84a6d771",
}

func sourceArtifact(version string, sources ...string) contracts.PolicyArtifactIdentityV2 {
	hash, _ := contracts.CanonicalSHA256(struct {
		Version string   `json:"version"`
		Source  []string `json:"source_sha256"`
	}{Version: version, Source: sources})
	return contracts.PolicyArtifactIdentityV2{Version: version, ContentSHA256: hash}
}

func liveArtifacts(sessionID string) (contracts.HistoryAvailabilityBindingV1, contracts.PolicyLineageManifestV3, contracts.LiveOnlyReleaseBindingV1, error) {
	history := contracts.HistoryAvailabilityBindingV1{
		SchemaVersion: contracts.HistoryAvailabilityBindingSchemaV1, Mode: contracts.HistoryModeNoGo, TerminalOutcome: contracts.HistoricalNoGoOutcome,
		CodeFoundationCommit: contracts.AcceptedHistoryCodeCommit, EvidenceCommit: contracts.AcceptedHistoryEvidenceCommit,
		EvidenceIndexSHA256: contracts.AcceptedEvidenceIndexSHA256, ArtifactTreeSHA256: contracts.AcceptedArtifactTreeSHA256,
		ReplayGateAuditSHA256: contracts.AcceptedReplayGateAuditSHA256, SourceProvenanceSHA256: contracts.AcceptedSourceProvenanceSHA256,
		DisabledFamilies: contracts.HistoricalDisabledFamiliesV1(), TournamentScopeID: contracts.AcceptedTournamentScopeID,
		TournamentScopeSHA256: contracts.AcceptedTournamentScopeSHA256, Cutoff: "2026-08-12T00:00:00Z", Trailing90Start: "2026-05-14T00:00:00Z",
		Trailing180Start: "2026-02-13T00:00:00Z", PatchID: "60", DotaPatch: "7.41",
	}
	bindingID, err := history.ContentID()
	if err != nil {
		return history, contracts.PolicyLineageManifestV3{}, contracts.LiveOnlyReleaseBindingV1{}, err
	}
	rawSchema := contracts.PolicyArtifactIdentityV2{Version: "raw_record.v3", ContentSHA256: strings.TrimPrefix(session.RawRecordSchemaV3Identity, "sha256:")}
	rawFraming := contracts.PolicyArtifactIdentityV2{Version: "raw_record_framing.v3", ContentSHA256: strings.TrimPrefix(session.RawRecordFramingV3Identity, "sha256:")}
	rawPayload := contracts.PolicyArtifactIdentityV2{Version: "dota2_gsi.v3", ContentSHA256: strings.TrimPrefix(session.RawPayloadSchemaV3Identity, "sha256:")}
	liveSchema := sourceArtifact(contracts.LiveObservationSchemaV1, "cb8513c20b816b681e083af043fb4ab13ea765b40b9dc055439e197c6644a8f9")
	projection := sourceArtifact("gsi_projection.v3+live_observation.v1", strings.TrimPrefix(session.GSIProjectionMappingV3Identity, "sha256:"), "ea481ddba7f714d2f25248d696d0b4d65a8a23b3b901754f96180053fcb1b8a8")
	catalog := sourceArtifact(presentation.CatalogVersion(), "a4b5092c15110544092c532af928b59a08add2f80dfcdfb6b92f275af4fce11d")
	terminology := sourceArtifact(presentation.TerminologyVersion(), "a4b5092c15110544092c532af928b59a08add2f80dfcdfb6b92f275af4fce11d")
	localization := sourceArtifact("localization_parameter_mapping.v1", "a4b5092c15110544092c532af928b59a08add2f80dfcdfb6b92f275af4fce11d")
	engineSources := append(append([]string(nil), acceptedV3Sources...), insight.RulesArtifact().ContentSHA256)
	lineage := contracts.PolicyLineageManifestV3{
		SchemaVersion: contracts.PolicyLineageManifestSchemaV3, SessionID: sessionID, RawRecordSchema: rawSchema, RawRecordFraming: rawFraming,
		RawPayloadSchema: rawPayload, LiveObservationSchema: liveSchema, ProjectionMapping: projection,
		TournamentScopeID: strings.Repeat("a", 64), TournamentScopeSHA256: strings.Repeat("a", 64),
		HistoryAvailabilityBindingID: bindingID, HistoryAvailabilityBindingSHA256: bindingID,
		Rules: insight.RulesArtifact(), Config: insight.ConfigArtifact(insight.DefaultConfig()), Catalog: catalog, Terminology: terminology,
		LocalizationParameterMapping: localization, EngineBuild: sourceArtifact("dota2-ob.product.v3", engineSources...),
	}
	lineageID, err := lineage.ContentID()
	if err != nil {
		return history, lineage, contracts.LiveOnlyReleaseBindingV1{}, err
	}
	release := contracts.LiveOnlyReleaseBindingV1{
		SchemaVersion: contracts.LiveOnlyReleaseBindingSchemaV1, SourceCommit: acceptedLiveOnlyScopeCommit, LineageSchema: contracts.PolicyLineageManifestSchemaV3,
		LineageManifestID: lineageID, LineageManifestSHA256: lineageID, HistoryAvailabilityBindingID: bindingID, HistoryAvailabilityBindingSHA256: bindingID,
		CodeFoundationCommit: history.CodeFoundationCommit, EvidenceCommit: history.EvidenceCommit, EvidenceIndexSHA256: history.EvidenceIndexSHA256,
		ArtifactTreeSHA256: history.ArtifactTreeSHA256, ReplayGateAuditSHA256: history.ReplayGateAuditSHA256, SourceProvenanceSHA256: history.SourceProvenanceSHA256,
		TournamentScopeID: history.TournamentScopeID, TournamentScopeSHA256: history.TournamentScopeSHA256, Cutoff: history.Cutoff,
		Trailing90Start: history.Trailing90Start, Trailing180Start: history.Trailing180Start, PatchID: history.PatchID, DotaPatch: history.DotaPatch,
		DisabledFamilies: contracts.HistoricalDisabledFamiliesV1(), Rules: lineage.Rules, Config: lineage.Config, Catalog: lineage.Catalog,
		Terminology: lineage.Terminology, LocalizationParameterMapping: lineage.LocalizationParameterMapping,
	}
	if err := release.ValidateAgainst(history, lineage); err != nil {
		return history, lineage, release, err
	}
	return history, lineage, release, nil
}

func writeLiveArtifacts(root, sessionID string) ([]Artifact, error) {
	history, lineage, release, err := liveArtifacts(sessionID)
	if err != nil {
		return nil, err
	}
	values := map[string]any{"history_availability_binding_v1.json": history, "policy_lineage_manifest_v3.json": lineage, "live_only_release_binding_v1.json": release}
	var artifacts []Artifact
	for name, value := range values {
		payload, err := contracts.MarshalCanonical(value)
		if err != nil {
			return nil, err
		}
		relative := "config/policy/" + name
		path := root + "/" + relative
		if err := writePrivate(path, payload); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, Artifact{Path: relative, SHA256: payloadSHA(payload), Bytes: int64(len(payload))})
	}
	return artifacts, nil
}

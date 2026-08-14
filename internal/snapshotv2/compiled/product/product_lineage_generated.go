package product

import (
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/snapshotv2/compiled/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/snapshotv2/compiled/presentation"
)

type productLineageArtifacts struct {
	rawRecordSchema, rawRecordFraming, rawPayloadSchema contracts.PolicyArtifactIdentityV2
	liveObservationSchema, projectionMapping            contracts.PolicyArtifactIdentityV2
	catalog, terminology, localizationMapping           contracts.PolicyArtifactIdentityV2
	engineBuild                                         contracts.PolicyArtifactIdentityV2
}

func expectedProductLineageArtifacts() productLineageArtifacts {
	return productLineageArtifacts{
		rawRecordSchema:       acceptedCaptureArtifact("raw_record.v3", session.RawRecordSchemaV3Identity),
		rawRecordFraming:      acceptedCaptureArtifact("raw_record_framing.v3", session.RawRecordFramingV3Identity),
		rawPayloadSchema:      acceptedCaptureArtifact("dota2_gsi.v3", session.RawPayloadSchemaV3Identity),
		liveObservationSchema: sourceArtifact(contracts.LiveObservationSchemaV1, contractsSourceSHA256),
		projectionMapping: sourceArtifact("gsi_projection.v3+live_observation.v1",
			strings.TrimPrefix(session.GSIProjectionMappingV3Identity, "sha256:"), liveMappingSourceSHA256),
		catalog:             sourceArtifact(presentation.CatalogVersion(), presentationCatalogSHA256),
		terminology:         sourceArtifact(presentation.TerminologyVersion(), presentationCatalogSHA256),
		localizationMapping: sourceArtifact("localization_parameter_mapping.v1", presentationCatalogSHA256),
		engineBuild: sourceArtifact("dota2-ob.product.v1", productMainSourceSHA256, productPortsSourceSHA256,
			productRecoverySourceSHA256, productRuntimeSourceSHA256, productLineageSourceSHA256,
			sessionHighWaterSourceSHA256, sessionFollowerSourceSHA256,
			insightEngineSourceSHA256, policyEngineSourceSHA256, policyApplicationSourceSHA256, insight.RulesArtifact().ContentSHA256),
	}
}

func acceptedCaptureArtifact(version, identity string) contracts.PolicyArtifactIdentityV2 {
	return contracts.PolicyArtifactIdentityV2{Version: version, ContentSHA256: strings.TrimPrefix(identity, "sha256:")}
}

func assembleProductLineage(input contracts.PolicyLineageManifestV2, sessionID string) contracts.PolicyLineageManifestV2 {
	owned := expectedProductLineageArtifacts()
	input.SessionID = sessionID
	input.RawRecordSchema = owned.rawRecordSchema
	input.RawRecordFraming = owned.rawRecordFraming
	input.RawPayloadSchema = owned.rawPayloadSchema
	input.LiveObservationSchema = owned.liveObservationSchema
	input.ProjectionMapping = owned.projectionMapping
	input.Rules = insight.RulesArtifact()
	input.Config = insight.ConfigArtifact(insight.DefaultConfig())
	input.Catalog = owned.catalog
	input.Terminology = owned.terminology
	input.LocalizationParameterMapping = owned.localizationMapping
	input.EngineBuild = owned.engineBuild
	return input
}

func sourceArtifact(version string, sourceSHA256 ...string) contracts.PolicyArtifactIdentityV2 {
	content, _ := contracts.CanonicalSHA256(struct {
		Version      string   `json:"version"`
		SourceSHA256 []string `json:"source_sha256"`
	}{Version: version, SourceSHA256: append([]string(nil), sourceSHA256...)})
	return contracts.PolicyArtifactIdentityV2{Version: version, ContentSHA256: content}
}

func matchesProductLineage(lineage contracts.PolicyLineageManifestV2, sessionID string) bool {
	expected := expectedProductLineageArtifacts()
	return lineage.Validate() == nil && lineage.SessionID == sessionID &&
		lineage.RawRecordSchema == expected.rawRecordSchema && lineage.RawRecordFraming == expected.rawRecordFraming &&
		lineage.RawPayloadSchema == expected.rawPayloadSchema && lineage.LiveObservationSchema == expected.liveObservationSchema &&
		lineage.ProjectionMapping == expected.projectionMapping && lineage.Rules == insight.RulesArtifact() &&
		lineage.Config == insight.ConfigArtifact(insight.DefaultConfig()) && lineage.Catalog == expected.catalog &&
		lineage.Terminology == expected.terminology && lineage.LocalizationParameterMapping == expected.localizationMapping &&
		lineage.EngineBuild == expected.engineBuild
}

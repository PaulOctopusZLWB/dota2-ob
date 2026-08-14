package main

import (
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/insight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/presentation"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

type productLineageArtifacts struct {
	rawRecordSchema, rawRecordFraming, rawPayloadSchema contracts.PolicyArtifactIdentityV2
	liveObservationSchema, projectionMapping            contracts.PolicyArtifactIdentityV2
	catalog, terminology, localizationMapping           contracts.PolicyArtifactIdentityV2
	engineBuild                                         contracts.PolicyArtifactIdentityV2
}

func expectedProductLineageArtifacts() productLineageArtifacts {
	// Snapshot V2 is an accepted immutable interface. Its artifact identities
	// remain pinned to the accepted c0b328a source set even when V3 evolves.
	const (
		v2Catalog = "643bbab16fe6576f5be16ee0e71127a58c799730e6754b5ef99178fd91693183"
		v2Main    = "658cc654f9284889a07dc9c49a070529283f60b013b526544fc5a3fedb503c5f"
		v2Ports   = "238b095c9a9196feb438d3180caccf302f3eb646ecf7a21e9ee3c048ea3d3d66"
		v2Lineage = "426f51938697789b97f7599339ba73585d3e64684a444320493b7d4a9eb5e572"
		v2Insight = "03fe0d238c1bb1eb166414e1baa060362356fcdfe964cc968808107e63e7f55f"
		v2Policy  = "805386b3fd50c58aa1435f791932336c6aeb40ff6d28feb1cdceeaa8d25cf6cb"
	)
	return productLineageArtifacts{
		rawRecordSchema:       acceptedCaptureArtifact("raw_record.v3", session.RawRecordSchemaV3Identity),
		rawRecordFraming:      acceptedCaptureArtifact("raw_record_framing.v3", session.RawRecordFramingV3Identity),
		rawPayloadSchema:      acceptedCaptureArtifact("dota2_gsi.v3", session.RawPayloadSchemaV3Identity),
		liveObservationSchema: sourceArtifact(contracts.LiveObservationSchemaV1, contractsSourceSHA256),
		projectionMapping: sourceArtifact("gsi_projection.v3+live_observation.v1",
			strings.TrimPrefix(session.GSIProjectionMappingV3Identity, "sha256:"), liveMappingSourceSHA256),
		catalog:             sourceArtifact(presentation.CatalogVersion(), v2Catalog),
		terminology:         sourceArtifact(presentation.TerminologyVersion(), v2Catalog),
		localizationMapping: sourceArtifact("localization_parameter_mapping.v1", v2Catalog),
		engineBuild: sourceArtifact("dota2-ob.product.v1", v2Main, v2Ports,
			productRecoverySourceSHA256, productRuntimeSourceSHA256, v2Lineage,
			sessionHighWaterSourceSHA256, sessionFollowerSourceSHA256,
			v2Insight, v2Policy, policyApplicationSourceSHA256, insight.RulesArtifact().ContentSHA256),
	}
}

func expectedProductLineageArtifactsV3() productLineageArtifacts {
	return productLineageArtifacts{
		rawRecordSchema:       acceptedCaptureArtifact("raw_record.v3", session.RawRecordSchemaV3Identity),
		rawRecordFraming:      acceptedCaptureArtifact("raw_record_framing.v3", session.RawRecordFramingV3Identity),
		rawPayloadSchema:      acceptedCaptureArtifact("dota2_gsi.v3", session.RawPayloadSchemaV3Identity),
		liveObservationSchema: sourceArtifact(contracts.LiveObservationSchemaV1, contractsSourceSHA256),
		projectionMapping:     sourceArtifact("gsi_projection.v3+live_observation.v1", strings.TrimPrefix(session.GSIProjectionMappingV3Identity, "sha256:"), liveMappingSourceSHA256),
		catalog:               sourceArtifact(presentation.CatalogVersion(), presentationCatalogSHA256),
		terminology:           sourceArtifact(presentation.TerminologyVersion(), presentationCatalogSHA256),
		localizationMapping:   sourceArtifact("localization_parameter_mapping.v1", presentationCatalogSHA256),
		engineBuild: sourceArtifact("dota2-ob.product.v3", productMainSourceSHA256, productPortsSourceSHA256,
			productRecoverySourceSHA256, productRuntimeSourceSHA256, productLineageSourceSHA256,
			productLiveOnlySourceSHA256, productRecoveryV3SourceSHA256, productRuntimeV3SourceSHA256,
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

func matchesProductLineageV3(lineage contracts.PolicyLineageManifestV3, binding contracts.HistoryAvailabilityBindingV1, sessionID string) bool {
	expected := expectedProductLineageArtifactsV3()
	bindingID, err := binding.ContentID()
	return err == nil && binding.Mode == contracts.HistoryModeNoGo && lineage.Validate() == nil && lineage.SessionID == sessionID &&
		lineage.HistoryAvailabilityBindingID == bindingID && lineage.HistoryAvailabilityBindingSHA256 == bindingID &&
		lineage.RawRecordSchema == expected.rawRecordSchema && lineage.RawRecordFraming == expected.rawRecordFraming &&
		lineage.RawPayloadSchema == expected.rawPayloadSchema && lineage.LiveObservationSchema == expected.liveObservationSchema &&
		lineage.ProjectionMapping == expected.projectionMapping && lineage.Rules == insight.RulesArtifact() &&
		lineage.Config == insight.ConfigArtifact(insight.DefaultConfig()) && lineage.Catalog == expected.catalog &&
		lineage.Terminology == expected.terminology && lineage.LocalizationParameterMapping == expected.localizationMapping &&
		lineage.EngineBuild == expected.engineBuild
}

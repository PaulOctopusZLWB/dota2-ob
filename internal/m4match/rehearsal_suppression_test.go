package m4match

import (
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

func TestSuppressionReconciliationRejectsEveryEvidenceAttack(t *testing.T) {
	families := contracts.HistoricalDisabledFamiliesV1()
	registry, _ := contracts.MarshalCanonical(families)
	record := RehearsalRawIdentityV1{Sequence: 1, RawRecordSHA256: repeatHex("a"), RawPayloadSHA256: repeatHex("b")}
	manifest := RehearsalRawManifestV1{SchemaVersion: "public_match_rehearsal_raw_manifest.v3", SessionID: "session", Accepted: 1, Records: []RehearsalRawIdentityV1{record}}
	valid := RehearsalSuppressionEvidenceV1{SchemaVersion: "public_match_rehearsal_suppression.v3", RegistrySHA256: payloadSHA(registry), FrameCount: 1}
	for _, family := range families {
		site := family + ".eligibility.v1"
		valid.Executions = append(valid.Executions, SuppressionExecutionV1{SourceSequence: 1, RawRecordSHA256: record.RawRecordSHA256, RawPayloadSHA256: record.RawPayloadSHA256, Family: family, EligibilitySite: site, Executed: true})
		valid.Audits = append(valid.Audits, SuppressionAuditV1{SourceSequence: 1, RawRecordSHA256: record.RawRecordSHA256, RawPayloadSHA256: record.RawPayloadSHA256, Family: family, EligibilitySite: site, Reason: "unavailable_for_public_match", Result: "suppressed"})
	}
	if err := validateRehearsalSuppression(valid, manifest); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*RehearsalSuppressionEvidenceV1){
		"missing":    func(v *RehearsalSuppressionEvidenceV1) { v.Audits = v.Audits[:len(v.Audits)-1] },
		"duplicate":  func(v *RehearsalSuppressionEvidenceV1) { v.Executions[1] = v.Executions[0] },
		"unexecuted": func(v *RehearsalSuppressionEvidenceV1) { v.Executions[0].Executed = false },
		"fallback":   func(v *RehearsalSuppressionEvidenceV1) { v.Executions[0].FallbackUsed = true },
		"reordered": func(v *RehearsalSuppressionEvidenceV1) {
			v.Executions[0], v.Executions[1] = v.Executions[1], v.Executions[0]
		},
		"spliced":        func(v *RehearsalSuppressionEvidenceV1) { v.Audits[0].RawPayloadSHA256 = repeatHex("c") },
		"candidate-leak": func(v *RehearsalSuppressionEvidenceV1) { v.Audits[0].CandidateClaim = true },
		"decision-leak":  func(v *RehearsalSuppressionEvidenceV1) { v.Audits[0].DecisionClaim = true },
		"overlay-leak":   func(v *RehearsalSuppressionEvidenceV1) { v.Audits[0].OverlayClaim = true },
		"display-leak":   func(v *RehearsalSuppressionEvidenceV1) { v.Audits[0].DisplayProduced = true },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			copyValue := valid
			copyValue.Executions = append([]SuppressionExecutionV1(nil), valid.Executions...)
			copyValue.Audits = append([]SuppressionAuditV1(nil), valid.Audits...)
			mutate(&copyValue)
			if err := validateRehearsalSuppression(copyValue, manifest); err == nil {
				t.Fatal("attack admitted")
			}
		})
	}
}

func repeatHex(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result[:64]
}

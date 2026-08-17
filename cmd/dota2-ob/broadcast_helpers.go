package main

import (
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

func hasCommandResult(results []contracts.PolicyCommandResultRefV2, commandID string) bool {
	for _, result := range results {
		if result.CommandID == commandID {
			return true
		}
	}
	return false
}

func cloneOverlay(state contracts.OverlayStateV1) (contracts.OverlayStateV1, error) {
	payload, err := contracts.MarshalCanonical(state)
	if err != nil {
		return contracts.OverlayStateV1{}, err
	}
	var clone contracts.OverlayStateV1
	if err := contracts.DecodeStrict(payload, &clone); err != nil {
		return contracts.OverlayStateV1{}, err
	}
	return clone, nil
}

func policyPinned(pins []contracts.PolicyPinV2, candidateID string) bool {
	for _, pin := range pins {
		if pin.CandidateID == candidateID {
			return true
		}
	}
	return false
}

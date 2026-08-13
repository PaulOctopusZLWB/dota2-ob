package main

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/delivery"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/liveprojection"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/operator"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

const policyProjectionSubsystem = "broadcast_policy"

type broadcastCommandPort struct{ runtime *broadcastRuntime }
type broadcastOperatorPort struct{ runtime *broadcastRuntime }
type broadcastOverlayPort struct{ runtime *broadcastRuntime }

func (p broadcastCommandPort) Execute(ctx context.Context, command contracts.OperatorCommandV1) (contracts.OperatorCommandResultV1, error) {
	return p.runtime.execute(ctx, command)
}

func (p broadcastOperatorPort) Current(ctx context.Context) (delivery.OperatorState, error) {
	return p.runtime.operatorState(ctx)
}

func (p broadcastOverlayPort) Current(ctx context.Context) (contracts.OverlayStateV1, error) {
	return p.runtime.overlayState(ctx)
}

func loadPolicyLineage(path, sessionID string) (contracts.PolicyLineageManifestV2, error) {
	if strings.TrimSpace(path) == "" {
		return contracts.PolicyLineageManifestV2{}, errors.New("policy lineage file is required")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > contracts.MaxPolicyLineageManifestBytes {
		return contracts.PolicyLineageManifestV2{}, errors.New("policy lineage file is unavailable or oversized")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return contracts.PolicyLineageManifestV2{}, errors.New("policy lineage file is unreadable")
	}
	var lineage contracts.PolicyLineageManifestV2
	if err := contracts.DecodeStrict(payload, &lineage); err != nil || lineage.Validate() != nil || lineage.SessionID != sessionID {
		return contracts.PolicyLineageManifestV2{}, errors.New("policy lineage file is invalid for this session")
	}
	return lineage, nil
}

type trackedCaptureProjection struct {
	projection      capture.Projection
	tracker         *operator.Tracker
	subsystem, code string
}

func newTrackedCaptureProjection(projection capture.Projection, tracker *operator.Tracker, subsystem, code string) liveprojection.Projection {
	return trackedCaptureProjection{projection: projection, tracker: tracker, subsystem: subsystem, code: code}
}

func (p trackedCaptureProjection) Apply(ctx context.Context, record *session.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.projection.Apply(record); err != nil {
		p.tracker.Failure(p.subsystem, p.code, "projection failed")
		return errors.New(p.code)
	}
	if p.subsystem == operator.SubsystemAnalytics {
		p.tracker.AnalyticsSuccess(record.ReceivedAt)
	} else {
		p.tracker.Success(p.subsystem)
	}
	return nil
}

type policyObservationProjection struct {
	runtime *broadcastRuntime
	tracker *operator.Tracker
}

func newPolicyObservationProjection(runtime *broadcastRuntime, tracker *operator.Tracker) liveprojection.Projection {
	return policyObservationProjection{runtime: runtime, tracker: tracker}
}

func (p policyObservationProjection) Apply(ctx context.Context, record *session.Record) error {
	observation, err := capture.MapLiveObservationV1(record)
	if err == nil {
		err = p.runtime.applyObservation(ctx, observation)
	}
	if err != nil {
		p.tracker.Failure(policyProjectionSubsystem, "broadcast_policy_failed", "broadcast policy projection failed")
		return errors.New("broadcast_policy_failed")
	}
	p.tracker.Success(policyProjectionSubsystem)
	return nil
}

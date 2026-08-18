package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/delivery"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/liveprojection"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/operator"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/presentation"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

const policyProjectionSubsystem = "broadcast_policy"

type broadcastPorts interface {
	execute(context.Context, contracts.OperatorCommandV1) (contracts.OperatorCommandResultV1, error)
	operatorState(context.Context) (delivery.OperatorState, error)
	overlayState(context.Context) (contracts.OverlayStateV1, error)
}

// productBroadcastRuntime is the intentionally narrow product composition
// seam implemented independently by the snapshot-backed V2 and live-only V3
// runtimes. It is not a version-neutral policy envelope: typed V2/V3 contracts
// remain confined to their respective implementations.
type productBroadcastRuntime interface {
	broadcastPorts
	io.Closer
	applyObservation(context.Context, contracts.LiveObservationV1) error
	applyRecord(context.Context, contracts.LiveObservationV1, string) error
	session.StartupPublicationBarrier
	session.RejectionHealthSink
}

type broadcastCommandPort struct{ runtime broadcastPorts }
type broadcastOperatorPort struct{ runtime broadcastPorts }
type broadcastOverlayPort struct{ runtime broadcastPorts }

type failClosedBroadcastPorts struct {
	operator delivery.OperatorState
	overlay  contracts.OverlayStateV1
}

func newFailClosedBroadcastPorts(sessionID string, nowMS int64) (*failClosedBroadcastPorts, error) {
	overlay, err := presentation.Hidden(sessionID, nowMS, nowMS+overlayFreshness.Milliseconds(), "policy_unconfigured")
	if err != nil {
		return nil, err
	}
	return &failClosedBroadcastPorts{
		operator: delivery.OperatorState{SchemaVersion: "operator_state.v1", SessionID: sessionID, Previews: []delivery.OperatorPreview{}},
		overlay:  overlay,
	}, nil
}

func (p *failClosedBroadcastPorts) execute(context.Context, contracts.OperatorCommandV1) (contracts.OperatorCommandResultV1, error) {
	return contracts.OperatorCommandResultV1{}, errors.New("broadcast policy is not configured")
}

func (p *failClosedBroadcastPorts) operatorState(context.Context) (delivery.OperatorState, error) {
	return p.operator, nil
}

func (p *failClosedBroadcastPorts) overlayState(context.Context) (contracts.OverlayStateV1, error) {
	return cloneOverlay(p.overlay)
}

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
	if err := contracts.DecodeStrict(payload, &lineage); err != nil || lineage.SessionID != sessionID {
		return contracts.PolicyLineageManifestV2{}, errors.New("policy lineage file is invalid for this session")
	}
	lineage = assembleProductLineage(lineage, sessionID)
	if lineage.Validate() != nil {
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
	runtime        productBroadcastRuntime
	tracker        *operator.Tracker
	mapObservation func(*session.Record) (contracts.LiveObservationV1, error)
}

func newPolicyObservationProjection(runtime productBroadcastRuntime, tracker *operator.Tracker, mapObservation func(*session.Record) (contracts.LiveObservationV1, error)) liveprojection.Projection {
	return policyObservationProjection{runtime: runtime, tracker: tracker, mapObservation: mapObservation}
}

func (p policyObservationProjection) Apply(ctx context.Context, record *session.Record) error {
	observation, err := p.mapObservation(record)
	if err == nil {
		rawRecordSHA256, hashErr := session.RecordV3SHA256(record)
		if hashErr != nil {
			err = hashErr
		}
		request, marshalErr := contracts.MarshalCanonical(observation)
		if err != nil {
			// retain the source identity error
		} else if marshalErr != nil {
			err = marshalErr
		} else if len(request) > contracts.MaxLiveObservationBytes {
			err = p.runtime.applyRecord(ctx, observation, rawRecordSHA256)
		} else {
			var decoded contracts.LiveObservationV1
			if decodeErr := contracts.DecodeStrict(request, &decoded); decodeErr != nil {
				err = decodeErr
			} else {
				err = p.runtime.applyRecord(ctx, decoded, rawRecordSHA256)
			}
		}
	}
	if err != nil {
		p.tracker.Failure(policyProjectionSubsystem, "broadcast_policy_failed", "broadcast policy projection failed")
		return errors.New("broadcast_policy_failed")
	}
	p.tracker.Success(policyProjectionSubsystem)
	return nil
}

type productWaiter struct {
	capture interface{ Wait() }
	policy  *policyProjectionRunner
}

func (w productWaiter) Wait() {
	if w.capture != nil {
		w.capture.Wait()
	}
	if w.policy != nil {
		w.policy.Wait()
	}
}

// policyProjectionRunner gives the broadcast policy plane its own cursor and
// independent high-water subscription. A missing or retrying policy projection
// therefore cannot stall latest/profile/analytics capture projections.
type policyProjectionRunner struct {
	follower    *session.LiveFollower
	highWater   *session.HighWater
	cancel      context.CancelFunc
	done        <-chan error
	unsubscribe func()
	once        sync.Once
}

func newPolicyProjectionRunner(store *session.Store, runtime productBroadcastRuntime, tracker *operator.Tracker, mapObservation func(*session.Record) (contracts.LiveObservationV1, error)) *policyProjectionRunner {
	updates, unsubscribe := store.HighWater().Subscribe()
	follower := session.NewLiveFollower(
		store.SessionID(),
		store.RawPath(),
		filepath.Join(store.SessionDir(), "broadcast_policy_projection_cursor.json"),
		[]session.LiveProjection{newPolicyObservationProjection(runtime, tracker, mapObservation)},
		session.WithFollowerHighWater(store.HighWater()),
		session.WithStartupBarrier(runtime),
		session.WithRejectionHealthSink(runtime),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- follower.Run(ctx, updates) }()
	return &policyProjectionRunner{
		follower: follower, highWater: store.HighWater(), cancel: cancel,
		done: done, unsubscribe: unsubscribe,
	}
}

func (r *policyProjectionRunner) Wait() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.unsubscribe()
		select {
		case <-r.done:
		case <-time.After(100 * time.Millisecond):
			r.cancel()
			<-r.done
		}
		r.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = r.follower.CatchUp(ctx, r.highWater.Current().Sequence)
		cancel()
	})
}

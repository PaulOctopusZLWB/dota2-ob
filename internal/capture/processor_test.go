package capture_test

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/operator"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

type memoryAppender struct {
	mu   sync.Mutex
	seq  uint64
	fail error
}

func (a *memoryAppender) Append([]byte) (*session.Record, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail != nil {
		err := a.fail
		a.fail = nil
		return nil, err
	}
	a.seq++
	return &session.Record{SchemaVersion: 2, SessionID: "s", Sequence: a.seq, ReceivedAt: time.Unix(int64(a.seq), 0).UTC(), Source: "gsi", Payload: map[string]any{}}, nil
}

type projection struct {
	name   string
	calls  *[]string
	failAt map[uint64]bool
}

func (p projection) Apply(record *session.Record) error {
	*p.calls = append(*p.calls, p.name+":"+string(rune('0'+record.Sequence)))
	if p.failAt[record.Sequence] {
		return errors.New("sensitive internal failure")
	}
	return nil
}

type blockingProjection struct {
	calls            *[]string
	entered, release chan struct{}
}

func (p blockingProjection) Apply(record *session.Record) error {
	*p.calls = append(*p.calls, "latest:"+string(rune('0'+record.Sequence)))
	if record.Sequence == 1 {
		close(p.entered)
		<-p.release
	}
	return nil
}

func TestProcessorOrdersGroupsAndContinuesAfterFailure(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	tracker := operator.NewTracker("s", now, time.Minute, func() time.Time { return now })
	appender := &memoryAppender{}
	var calls []string
	processor := capture.NewProcessor(appender, tracker,
		capture.WithLatest(projection{"latest", &calls, map[uint64]bool{1: true}}),
		capture.WithProfile(projection{"profile", &calls, nil}),
		capture.WithAnalytics(projection{"analytics", &calls, nil}),
	)
	if _, err := processor.Process([]byte(`{}`)); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if _, err := processor.Process([]byte(`{}`)); err != nil {
		t.Fatalf("Process second: %v", err)
	}
	want := []string{"latest:1", "profile:1", "analytics:1", "latest:2", "profile:2", "analytics:2"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	snap := tracker.Snapshot()
	if snap.AcceptedCount != 2 || snap.PostProcessingFailureCount != 1 || snap.State != operator.StateReceiving {
		t.Fatalf("snapshot = %#v", snap)
	}
}

func TestProcessorRawFailureDoesNotProjectAndRecoversOnCommit(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	tracker := operator.NewTracker("s", now, time.Minute, func() time.Time { return now })
	appender := &memoryAppender{fail: session.ErrAppendFailed}
	var calls []string
	processor := capture.NewProcessor(appender, tracker, capture.WithLatest(projection{"latest", &calls, nil}))
	if _, err := processor.Process([]byte(`{}`)); !errors.Is(err, session.ErrAppendFailed) {
		t.Fatalf("error = %v", err)
	}
	if len(calls) != 0 || tracker.Snapshot().State != operator.StateDegraded {
		t.Fatalf("failure projected or was not degraded")
	}
	if _, err := processor.Process([]byte(`{}`)); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if tracker.Snapshot().State != operator.StateReceiving {
		t.Fatalf("raw failure did not recover: %#v", tracker.Snapshot())
	}
}

func TestProcessorFailureLogUsesStableCodeWithoutInternalError(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	tracker := operator.NewTracker("s", now, time.Minute, func() time.Time { return now })
	appender := &memoryAppender{}
	var calls, logs []string
	processor := capture.NewProcessor(appender, tracker,
		capture.WithLatest(projection{"latest", &calls, map[uint64]bool{1: true}}),
		capture.WithFailureLogger(func(code, subsystem string) { logs = append(logs, code+":"+subsystem) }),
	)
	_, _ = processor.Process([]byte(`{}`))
	if !reflect.DeepEqual(logs, []string{"latest_failed:latest"}) {
		t.Fatalf("logs=%v", logs)
	}
}

func TestProcessorConcurrentRequestsKeepWholeProjectionOrder(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	tracker := operator.NewTracker("s", now, time.Minute, func() time.Time { return now })
	appender := &memoryAppender{}
	var calls []string
	entered, release := make(chan struct{}), make(chan struct{})
	processor := capture.NewProcessor(appender, tracker,
		capture.WithLatest(blockingProjection{&calls, entered, release}),
		capture.WithProfile(projection{"profile", &calls, nil}),
		capture.WithAnalytics(projection{"analytics", &calls, nil}),
	)
	results := make(chan error, 2)
	go func() { _, err := processor.Process([]byte(`{"request":1}`)); results <- err }()
	<-entered
	secondStarted := make(chan struct{})
	go func() { close(secondStarted); _, err := processor.Process([]byte(`{"request":2}`)); results <- err }()
	<-secondStarted
	close(release)
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	want := []string{"latest:1", "profile:1", "analytics:1", "latest:2", "profile:2", "analytics:2"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

func TestProcessorOverlappingFailuresRecoverOnlyTheirOwnSubsystem(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	tracker := operator.NewTracker("s", now, time.Minute, func() time.Time { return now })
	appender := &memoryAppender{}
	var calls []string
	processor := capture.NewProcessor(appender, tracker,
		capture.WithLatest(projection{"latest", &calls, map[uint64]bool{1: true}}),
		capture.WithProfile(projection{"profile", &calls, map[uint64]bool{1: true}}),
		capture.WithAnalytics(projection{"analytics", &calls, map[uint64]bool{2: true}}),
		capture.WithFailureLogger(func(string, string) {}),
	)
	_, _ = processor.Process([]byte(`{}`))
	if snap := tracker.Snapshot(); snap.PostProcessingFailureCount != 2 || snap.State != operator.StateDegraded {
		t.Fatalf("after seq1: %#v", snap)
	}
	_, _ = processor.Process([]byte(`{}`))
	snap := tracker.Snapshot()
	if len(snap.ActiveFailures) != 1 || snap.ActiveFailures[operator.SubsystemAnalytics].Code != "analytics_failed" || snap.PostProcessingFailureCount != 3 {
		t.Fatalf("after seq2: %#v", snap)
	}
	_, _ = processor.Process([]byte(`{}`))
	if snap := tracker.Snapshot(); snap.State != operator.StateReceiving || snap.PostProcessingFailureCount != 3 {
		t.Fatalf("after seq3: %#v", snap)
	}
}

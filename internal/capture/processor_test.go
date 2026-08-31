package capture_test

import (
	"errors"
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

func TestProcessorAcknowledgesCommittedAppend(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	tracker := operator.NewTracker("s", now, time.Minute, func() time.Time { return now })
	appender := &memoryAppender{}
	processor := capture.NewProcessor(appender, tracker)
	record, err := processor.Process([]byte(`{}`))
	if err != nil || record.Sequence != 1 || tracker.Snapshot().AcceptedCount != 1 {
		t.Fatalf("record=%#v err=%v status=%#v", record, err, tracker.Snapshot())
	}
}

func TestProcessorRawFailureDoesNotProjectAndRecoversOnCommit(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	tracker := operator.NewTracker("s", now, time.Minute, func() time.Time { return now })
	appender := &memoryAppender{fail: session.ErrAppendFailed}
	processor := capture.NewProcessor(appender, tracker)
	if _, err := processor.Process([]byte(`{}`)); !errors.Is(err, session.ErrAppendFailed) {
		t.Fatalf("error = %v", err)
	}
	if tracker.Snapshot().State != operator.StateDegraded {
		t.Fatalf("failure was not degraded")
	}
	if _, err := processor.Process([]byte(`{}`)); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if tracker.Snapshot().State != operator.StateReceiving {
		t.Fatalf("raw failure did not recover: %#v", tracker.Snapshot())
	}
}

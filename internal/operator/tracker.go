package operator

import (
	"sync"
	"time"
)

const (
	StateWaiting   = "waiting"
	StateReceiving = "receiving"
	StateStale     = "stale"
	StateDegraded  = "degraded"

	SubsystemRaw       = "raw"
	SubsystemLatest    = "latest"
	SubsystemProfile   = "profile"
	SubsystemAnalytics = "analytics"
	MaxErrors          = 16
	maxMessageBytes    = 160
)

type Clock func() time.Time

type ErrorRecord struct {
	Code      string    `json:"code"`
	Subsystem string    `json:"subsystem"`
	Message   string    `json:"message"`
	Time      time.Time `json:"time"`
}

type Snapshot struct {
	State                      string                 `json:"state"`
	ProcessStartTime           time.Time              `json:"process_start_time"`
	SessionID                  string                 `json:"session_id"`
	StaleThresholdSeconds      int64                  `json:"stale_threshold_seconds"`
	LastRequestTime            *time.Time             `json:"last_request_time,omitempty"`
	FirstAcceptedTime          *time.Time             `json:"first_accepted_time,omitempty"`
	LastAcceptedTime           *time.Time             `json:"last_accepted_time,omitempty"`
	LastAnalyticsSuccessTime   *time.Time             `json:"last_analytics_success_time,omitempty"`
	RequestCount               uint64                 `json:"request_count"`
	AcceptedCount              uint64                 `json:"accepted_count"`
	RejectedCount              uint64                 `json:"rejected_count"`
	RawWriteFailureCount       uint64                 `json:"raw_write_failure_count"`
	PostProcessingFailureCount uint64                 `json:"post_processing_failure_count"`
	ActiveFailures             map[string]ErrorRecord `json:"active_failures"`
	Errors                     []ErrorRecord          `json:"errors"`
}

type Tracker struct {
	mu                                                                          sync.RWMutex
	clock                                                                       Clock
	start                                                                       time.Time
	sessionID                                                                   string
	stale                                                                       time.Duration
	lastRequest, firstAccepted, lastAccepted, lastAnalytics                     *time.Time
	requestCount, acceptedCount, rejectedCount, rawFailures, projectionFailures uint64
	active                                                                      map[string]ErrorRecord
	errors                                                                      []ErrorRecord
}

func NewTracker(sessionID string, start time.Time, stale time.Duration, clock Clock) *Tracker {
	if clock == nil {
		clock = time.Now
	}
	if stale <= 0 {
		stale = 15 * time.Second
	}
	return &Tracker{clock: clock, start: start.UTC(), sessionID: sessionID, stale: stale, active: make(map[string]ErrorRecord)}
}

func (t *Tracker) Request() {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock().UTC()
	t.lastRequest = &now
	t.requestCount++
}
func (t *Tracker) Rejected() { t.mu.Lock(); t.rejectedCount++; t.mu.Unlock() }

func (t *Tracker) Accepted(at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	at = at.UTC()
	t.acceptedCount++
	t.lastAccepted = &at
	if t.firstAccepted == nil {
		first := at
		t.firstAccepted = &first
	}
	if failure, ok := t.active[SubsystemRaw]; ok && failure.Code != "raw_store_sealed" {
		delete(t.active, SubsystemRaw)
	}
}

func (t *Tracker) AnalyticsSuccess(at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	at = at.UTC()
	t.lastAnalytics = &at
	delete(t.active, SubsystemAnalytics)
}

func (t *Tracker) RawFailure(code, message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rawFailures++
	t.recordLocked(SubsystemRaw, code, message)
}

func (t *Tracker) Failure(subsystem, code, message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if subsystem != SubsystemRaw {
		t.projectionFailures++
	}
	t.recordLocked(subsystem, code, message)
}

func (t *Tracker) Success(subsystem string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if subsystem == SubsystemRaw {
		if failure, ok := t.active[subsystem]; ok && failure.Code == "raw_store_sealed" {
			return
		}
	}
	delete(t.active, subsystem)
}

func (t *Tracker) recordLocked(subsystem, code, message string) {
	if len(message) > maxMessageBytes {
		message = message[:maxMessageBytes]
	}
	record := ErrorRecord{Code: code, Subsystem: subsystem, Message: message, Time: t.clock().UTC()}
	t.active[subsystem] = record
	t.errors = append(t.errors, record)
	if len(t.errors) > MaxErrors {
		t.errors = append([]ErrorRecord(nil), t.errors[len(t.errors)-MaxErrors:]...)
	}
}

func (t *Tracker) Snapshot() Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	now := t.clock().UTC()
	state := StateWaiting
	if len(t.active) > 0 {
		state = StateDegraded
	} else if t.acceptedCount > 0 && t.lastAccepted != nil {
		if now.Sub(*t.lastAccepted) >= t.stale {
			state = StateStale
		} else {
			state = StateReceiving
		}
	}
	active := make(map[string]ErrorRecord, len(t.active))
	for k, v := range t.active {
		active[k] = v
	}
	return Snapshot{
		State: state, ProcessStartTime: t.start, SessionID: t.sessionID, StaleThresholdSeconds: int64(t.stale / time.Second),
		LastRequestTime: cloneTime(t.lastRequest), FirstAcceptedTime: cloneTime(t.firstAccepted), LastAcceptedTime: cloneTime(t.lastAccepted), LastAnalyticsSuccessTime: cloneTime(t.lastAnalytics),
		RequestCount: t.requestCount, AcceptedCount: t.acceptedCount, RejectedCount: t.rejectedCount, RawWriteFailureCount: t.rawFailures, PostProcessingFailureCount: t.projectionFailures,
		ActiveFailures: active, Errors: append([]ErrorRecord(nil), t.errors...),
	}
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

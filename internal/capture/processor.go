package capture

import (
	"errors"
	"log"
	"sync"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/operator"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

type Appender interface {
	Append([]byte) (*session.Record, error)
}
type Projection interface{ Apply(*session.Record) error }
type Option func(*Processor)

type Processor struct {
	mu                         sync.Mutex
	appender                   Appender
	tracker                    *operator.Tracker
	latest, profile, analytics Projection
	logFailure                 func(code, subsystem string)
}

func NewProcessor(appender Appender, tracker *operator.Tracker, opts ...Option) *Processor {
	p := &Processor{appender: appender, tracker: tracker, logFailure: func(code, subsystem string) {
		log.Printf("capture_failure code=%s subsystem=%s", code, subsystem)
	}}
	for _, opt := range opts {
		opt(p)
	}
	return p
}
func WithLatest(v Projection) Option    { return func(p *Processor) { p.latest = v } }
func WithProfile(v Projection) Option   { return func(p *Processor) { p.profile = v } }
func WithAnalytics(v Projection) Option { return func(p *Processor) { p.analytics = v } }
func WithFailureLogger(logger func(code, subsystem string)) Option {
	return func(p *Processor) {
		if logger != nil {
			p.logFailure = logger
		}
	}
}

func (p *Processor) Process(raw []byte) (*session.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	record, err := p.appender.Append(raw)
	if err != nil {
		if !errors.Is(err, session.ErrInvalidJSON) && p.tracker != nil {
			code := "raw_append_failed"
			if errors.Is(err, session.ErrStoreSealed) {
				code = "raw_store_sealed"
			}
			p.tracker.RawFailure(code, safeMessage(code))
			p.logFailure(code, operator.SubsystemRaw)
		}
		return nil, err
	}
	if p.tracker != nil {
		p.tracker.Accepted(record.ReceivedAt)
	}
	p.apply(operator.SubsystemLatest, "latest_failed", p.latest, record)
	p.apply(operator.SubsystemProfile, "profile_failed", p.profile, record)
	p.apply(operator.SubsystemAnalytics, "analytics_failed", p.analytics, record)
	return record, nil
}

func (p *Processor) apply(subsystem, code string, projection Projection, record *session.Record) {
	if projection == nil {
		return
	}
	if err := projection.Apply(record); err != nil {
		if p.tracker != nil {
			p.tracker.Failure(subsystem, code, safeMessage(code))
		}
		p.logFailure(code, subsystem)
		return
	}
	if p.tracker != nil {
		if subsystem == operator.SubsystemAnalytics {
			p.tracker.AnalyticsSuccess(record.ReceivedAt)
		} else {
			p.tracker.Success(subsystem)
		}
	}
}

func safeMessage(code string) string {
	switch code {
	case "raw_append_failed":
		return "raw append failed; retry is available"
	case "raw_store_sealed":
		return "raw store is sealed; restart is required"
	case "latest_failed":
		return "latest projection failed"
	case "profile_failed":
		return "profile projection failed"
	case "analytics_failed":
		return "analytics projection failed"
	default:
		return "subsystem operation failed"
	}
}

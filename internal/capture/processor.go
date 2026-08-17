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
	mu         sync.Mutex
	appender   Appender
	tracker    *operator.Tracker
	logFailure func(code, subsystem string)
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
	return record, nil
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

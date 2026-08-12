package session

import "sync"

// HighWaterMark identifies the newest raw record whose complete newline-
// terminated append returned successfully.
type HighWaterMark struct {
	SessionID string
	Sequence  uint64
}

// HighWater is a capacity-one, coalescing wakeup. The session log, not this
// notification, is authoritative.
type HighWater struct {
	mu      sync.Mutex
	current HighWaterMark
	updates chan HighWaterMark
}

func NewHighWater(sessionID string, sequence uint64) *HighWater {
	return &HighWater{current: HighWaterMark{SessionID: sessionID, Sequence: sequence}, updates: make(chan HighWaterMark, 1)}
}

func (h *HighWater) Publish(sequence uint64) {
	h.mu.Lock()
	if sequence <= h.current.Sequence {
		h.mu.Unlock()
		return
	}
	h.current.Sequence = sequence
	mark := h.current
	select {
	case <-h.updates:
	default:
	}
	select {
	case h.updates <- mark:
	default:
	}
	h.mu.Unlock()
}

func (h *HighWater) Wake() {
	h.mu.Lock()
	mark := h.current
	select {
	case <-h.updates:
	default:
	}
	select {
	case h.updates <- mark:
	default:
	}
	h.mu.Unlock()
}

func (h *HighWater) Current() HighWaterMark {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.current
}

func (h *HighWater) C() <-chan HighWaterMark { return h.updates }

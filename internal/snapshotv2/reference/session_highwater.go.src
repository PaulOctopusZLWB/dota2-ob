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
	mu          sync.Mutex
	current     HighWaterMark
	updates     chan HighWaterMark
	subscribers map[uint64]chan HighWaterMark
	nextID      uint64
}

func NewHighWater(sessionID string, sequence uint64) *HighWater {
	return &HighWater{
		current:     HighWaterMark{SessionID: sessionID, Sequence: sequence},
		updates:     make(chan HighWaterMark, 1),
		subscribers: make(map[uint64]chan HighWaterMark),
	}
}

func (h *HighWater) Publish(sequence uint64) {
	h.mu.Lock()
	if sequence <= h.current.Sequence {
		h.mu.Unlock()
		return
	}
	h.current.Sequence = sequence
	mark := h.current
	coalesce(h.updates, mark)
	for _, updates := range h.subscribers {
		coalesce(updates, mark)
	}
	h.mu.Unlock()
}

func (h *HighWater) Wake() {
	h.mu.Lock()
	mark := h.current
	coalesce(h.updates, mark)
	for _, updates := range h.subscribers {
		coalesce(updates, mark)
	}
	h.mu.Unlock()
}

// Subscribe returns an independent capacity-one notification stream. The raw
// session log remains authoritative; subscribers receive the current mark
// immediately and later notifications coalesce to the newest committed
// sequence. The returned cancellation function is safe to call more than once.
func (h *HighWater) Subscribe() (<-chan HighWaterMark, func()) {
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	updates := make(chan HighWaterMark, 1)
	updates <- h.current
	h.subscribers[id] = updates
	h.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			h.mu.Lock()
			if registered, ok := h.subscribers[id]; ok {
				delete(h.subscribers, id)
				close(registered)
			}
			h.mu.Unlock()
		})
	}
	return updates, unsubscribe
}

func (h *HighWater) Current() HighWaterMark {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.current
}

func (h *HighWater) C() <-chan HighWaterMark { return h.updates }

func coalesce(updates chan HighWaterMark, mark HighWaterMark) {
	select {
	case <-updates:
	default:
	}
	select {
	case updates <- mark:
	default:
	}
}

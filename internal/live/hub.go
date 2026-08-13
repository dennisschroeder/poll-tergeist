// Package live is the in-process invalidation hub for SSE subscribers. It
// carries no tally data itself — Postgres is the only source of truth for
// what changed. It is per-process by design — see docs/adr for the
// multi-instance trade-off.
package live

import "sync"

type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan struct{}]struct{} // pollID -> subscribers
}

func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[chan struct{}]struct{})}
}

// Subscribe registers a new invalidation channel for pollID. A receive from
// the channel means "this poll may now be stale — re-read the tally"; it
// carries no payload. Call the returned func to unsubscribe and let the
// channel be garbage collected.
func (h *Hub) Subscribe(pollID string) (ch chan struct{}, unsubscribe func()) {
	ch = make(chan struct{}, 1)

	h.mu.Lock()
	if h.subs[pollID] == nil {
		h.subs[pollID] = make(map[chan struct{}]struct{})
	}
	h.subs[pollID][ch] = struct{}{}
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		delete(h.subs[pollID], ch)
		if len(h.subs[pollID]) == 0 {
			delete(h.subs, pollID)
		}
		h.mu.Unlock()
		close(ch)
	}
}

// Invalidate marks pollID as changed for every current subscriber. It never
// blocks: a subscriber already marked dirty (an unconsumed pending signal)
// is left as-is — one more invalidation adds no information, since the
// subscriber will re-read the current tally from Postgres regardless of how
// many votes landed since its last read. This is intentional coalescing,
// not accidental message loss; see docs/adr/0003-tally-fan-out-and-queue-design.md.
func (h *Hub) Invalidate(pollID string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs[pollID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

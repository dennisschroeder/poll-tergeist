// Package live is the in-process SSE fan-out hub. It is per-process by
// design — see docs/adr for the multi-instance trade-off.
package live

import "sync"

// bufSize bounds how many un-consumed frames a slow subscriber can queue
// before Publish starts dropping frames for it instead of blocking the
// voter's request.
const bufSize = 4

type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan []byte]struct{} // pollID -> subscribers
}

func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[chan []byte]struct{})}
}

// Subscribe registers a new channel for pollID. Call the returned func to
// unsubscribe and let the channel be garbage collected.
func (h *Hub) Subscribe(pollID string) (ch chan []byte, unsubscribe func()) {
	ch = make(chan []byte, bufSize)

	h.mu.Lock()
	if h.subs[pollID] == nil {
		h.subs[pollID] = make(map[chan []byte]struct{})
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

// Publish fans msg out to every subscriber of pollID. A subscriber whose
// buffer is full is skipped rather than blocked — a slow reader drops
// frames instead of stalling the vote path that triggered the publish.
func (h *Hub) Publish(pollID string, msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs[pollID] {
		select {
		case ch <- msg:
		default:
		}
	}
}

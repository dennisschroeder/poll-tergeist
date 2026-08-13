package http

import (
	"encoding/json"
	"net/http"
)

// stream serves Server-Sent Events for one poll: an initial tally frame,
// then one frame per subsequent invalidation. It subscribes to the hub
// before reading any state, so a vote committed while the connection is
// being established is never permanently missed — see
// docs/adr/0003-tally-fan-out-and-queue-design.md for the ordering
// rationale. The hub only ever signals "this poll may be stale"; every
// frame sent here, initial or on invalidation, is read fresh from
// Postgres.
func (a *api) stream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	changes, unsubscribe := a.hub.Subscribe(id)
	defer unsubscribe()

	_, tally, err := a.loadPollAndTally(r, id)
	if err != nil {
		writePollLoadError(w, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	if !writeSSEFrame(w, tallyJSON(tally)) {
		return
	}
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			tally, err := a.store.GetTally(ctx, id)
			if err != nil {
				return
			}
			if !writeSSEFrame(w, tallyJSON(tally)) {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSEFrame(w http.ResponseWriter, v any) bool {
	payload, err := json.Marshal(v)
	if err != nil {
		return false
	}
	_, err = w.Write(append(append([]byte("data: "), payload...), '\n', '\n'))
	return err == nil
}

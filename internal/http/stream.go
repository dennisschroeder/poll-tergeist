package http

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// stream serves Server-Sent Events for one poll: an initial tally frame,
// then one frame per subsequent vote. Publish is non-blocking on the hub
// side, so a slow client here never stalls a voter's request elsewhere.
func (a *api) stream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

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

	counts := make(map[string]int64, len(tally.Counts))
	for optID, c := range tally.Counts {
		counts[fmt.Sprintf("%d", optID)] = int64(c)
	}
	if !writeSSEFrame(w, counts) {
		return
	}
	flusher.Flush()

	ch, unsubscribe := a.hub.Subscribe(id)
	defer unsubscribe()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(append(append([]byte("data: "), msg...), '\n', '\n')); err != nil {
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

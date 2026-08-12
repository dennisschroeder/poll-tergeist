package http

import (
	"io/fs"
	"net/http"

	"github.com/dennisschroeder/poll-tergeist/internal/live"
	"github.com/dennisschroeder/poll-tergeist/internal/store"
)

// NewRouter wires the API, the SSE stream, and the three static views.
// webFS is rooted at the web/ directory (see web.FS).
func NewRouter(s *store.Store, hub *live.Hub, webFS fs.FS) http.Handler {
	a := &api{store: s, hub: hub}
	v := &views{fs: webFS}

	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/polls", a.createPoll)
	mux.HandleFunc("GET /api/polls/{id}", a.getPoll)
	mux.HandleFunc("POST /api/polls/{id}/votes", a.vote)
	mux.HandleFunc("GET /api/polls/{id}/stream", a.stream)

	mux.HandleFunc("GET /{$}", v.page("create/index.html"))
	mux.HandleFunc("GET /p/{id}/results", v.page("results/index.html"))
	mux.HandleFunc("GET /p/{id}", v.page("vote/index.html"))
	mux.HandleFunc("GET /app.js", v.asset("app.js", "text/javascript; charset=utf-8"))

	return voterCookie(mux)
}

package http

import (
	"io/fs"
	"net/http"
)

type views struct {
	fs fs.FS
}

// page serves one embedded HTML file verbatim for every request that
// matches its route — the poll ID in the URL is read client-side, since
// there's no server-side templating for these static pages.
func (v *views) page(path string) http.HandlerFunc {
	return v.asset(path, "text/html; charset=utf-8")
}

func (v *views) asset(path, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(v.fs, path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(b)
	}
}

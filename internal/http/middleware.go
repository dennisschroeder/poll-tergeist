package http

import (
	"context"
	"net/http"

	"github.com/dennisschroeder/poll-tergeist/internal/poll"
)

type contextKey int

const voterTokenKey contextKey = iota

const voterCookieName = "voter_token"

// voterCookie ensures every request carries an opaque voter token, issuing
// one via an HttpOnly cookie on first visit. The token is not identity —
// it only scopes "one vote per poll" (see docs/adr).
func voterCookie(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if c, err := r.Cookie(voterCookieName); err == nil && c.Value != "" {
			token = c.Value
		} else {
			newToken, err := poll.NewVoterToken()
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			token = newToken
			http.SetCookie(w, &http.Cookie{
				Name:     voterCookieName,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   int((365 * 24 * 3600)),
			})
		}
		ctx := context.WithValue(r.Context(), voterTokenKey, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func voterTokenFrom(r *http.Request) string {
	token, _ := r.Context().Value(voterTokenKey).(string)
	return token
}

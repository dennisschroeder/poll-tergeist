package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	apphttp "github.com/dennisschroeder/poll-tergeist/internal/http"
	"github.com/dennisschroeder/poll-tergeist/internal/live"
	"github.com/dennisschroeder/poll-tergeist/internal/store"
	"github.com/dennisschroeder/poll-tergeist/web"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping HTTP handler tests")
	}

	s, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(s.Close)

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect for truncate: %v", err)
	}
	if _, err := pool.Exec(context.Background(), "TRUNCATE polls CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	pool.Close()

	handler := apphttp.NewRouter(s, live.NewHub(), web.FS)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func postJSON(t *testing.T, client *http.Client, url string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func TestCreatePoll_ValidationBounds(t *testing.T) {
	srv := newTestServer(t)
	client := srv.Client()

	tests := []struct {
		name       string
		question   string
		options    []string
		wantStatus int
	}{
		{"too few options", "Q?", []string{"only one"}, http.StatusBadRequest},
		{"too many options", "Q?", []string{"a", "b", "c", "d", "e", "f"}, http.StatusBadRequest},
		{"blank question", "   ", []string{"a", "b"}, http.StatusBadRequest},
		{"oversized question", string(bytes.Repeat([]byte("x"), 281)), []string{"a", "b"}, http.StatusBadRequest},
		{"blank option", "Q?", []string{"a", "   "}, http.StatusBadRequest},
		{"valid poll", "Tabs or spaces?", []string{"Tabs", "Spaces"}, http.StatusCreated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := postJSON(t, client, srv.URL+"/api/polls", map[string]any{
				"question": tt.question,
				"options":  tt.options,
			})
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

func TestGetPoll_UnknownID(t *testing.T) {
	srv := newTestServer(t)
	resp, err := srv.Client().Get(srv.URL + "/api/polls/doesnotexist")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestVote_UnknownPoll(t *testing.T) {
	srv := newTestServer(t)
	resp := postJSON(t, srv.Client(), srv.URL+"/api/polls/doesnotexist/votes", map[string]any{"option_id": 1})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestVote_OptionFromAnotherPoll(t *testing.T) {
	srv := newTestServer(t)
	client := srv.Client()

	var pollA, pollB struct {
		ID      string `json:"id"`
		Options []struct {
			ID int64 `json:"id"`
		} `json:"options"`
	}

	resp := postJSON(t, client, srv.URL+"/api/polls", map[string]any{
		"question": "Poll A", "options": []string{"A1", "A2"},
	})
	decodeJSON(t, resp, &pollA)

	resp = postJSON(t, client, srv.URL+"/api/polls", map[string]any{
		"question": "Poll B", "options": []string{"B1", "B2"},
	})
	decodeJSON(t, resp, &pollB)

	resp = postJSON(t, client, srv.URL+"/api/polls/"+pollA.ID+"/votes", map[string]any{
		"option_id": pollB.Options[0].ID,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

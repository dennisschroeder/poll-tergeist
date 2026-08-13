package http_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// readSSEFrame reads and decodes the next "data: {...}" frame from an SSE
// stream, skipping blank separator lines.
func readSSEFrame(t *testing.T, r *bufio.Reader) map[string]int64 {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE frame: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var tally map[string]int64
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &tally); err != nil {
			t.Fatalf("unmarshal SSE frame %q: %v", line, err)
		}
		return tally
	}
}

func sumTally(tally map[string]int64) int64 {
	var total int64
	for _, c := range tally {
		total += c
	}
	return total
}

type pollWithOptions struct {
	ID      string `json:"id"`
	Options []struct {
		ID int64 `json:"id"`
	} `json:"options"`
}

// TestStream_VoteRacingSubscriptionIsNotPermanentlyMissed proves the
// connection-order fix in internal/http/stream.go: a vote cast the instant
// the SSE connection opens (racing the server's subscribe-then-read
// sequence) still reaches the client, with no second vote required to
// trigger recovery. Before the fix (read DB, send, subscribe), a vote
// landing in that window could be lost forever.
func TestStream_VoteRacingSubscriptionIsNotPermanentlyMissed(t *testing.T) {
	srv := newTestServer(t)
	client := srv.Client()

	var p pollWithOptions
	resp := postJSON(t, client, srv.URL+"/api/polls", map[string]any{
		"question": "Q?", "options": []string{"A", "B"},
	})
	decodeJSON(t, resp, &p)
	optionID := p.Options[0].ID

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/polls/"+p.ID+"/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	sseResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer sseResp.Body.Close()

	// Cast the vote as soon as the connection is open, racing the server's
	// subscribe/read-tally sequence the way a real voter could.
	voteErrCh := make(chan error, 1)
	go func() {
		b, _ := json.Marshal(map[string]any{"option_id": optionID})
		r, err := client.Post(srv.URL+"/api/polls/"+p.ID+"/votes", "application/json", bytes.NewReader(b))
		if err == nil {
			r.Body.Close()
		}
		voteErrCh <- err
	}()

	reader := bufio.NewReader(sseResp.Body)
	for {
		tally := readSSEFrame(t, reader)
		if sumTally(tally) == 1 {
			break
		}
	}

	if err := <-voteErrCh; err != nil {
		t.Fatalf("vote request failed: %v", err)
	}
}

// TestStream_ConvergesAfterVoteBurstWithoutFurtherVotes covers the old
// queue-semantics failure mode directly: many votes land while the
// subscriber isn't draining the stream, then voting stops entirely. The
// client must still converge on the final tally by itself — recovery must
// not depend on one more vote occurring.
func TestStream_ConvergesAfterVoteBurstWithoutFurtherVotes(t *testing.T) {
	srv := newTestServer(t)
	client := srv.Client()

	var p pollWithOptions
	resp := postJSON(t, client, srv.URL+"/api/polls", map[string]any{
		"question": "Q?", "options": []string{"A", "B"},
	})
	decodeJSON(t, resp, &p)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/polls/"+p.ID+"/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	sseResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer sseResp.Body.Close()
	reader := bufio.NewReader(sseResp.Body)

	initial := readSSEFrame(t, reader)
	if sumTally(initial) != 0 {
		t.Fatalf("initial tally = %v, want all-zero", initial)
	}

	// Fire many votes from distinct voters without reading the stream in
	// between — simulates a slow consumer falling behind a burst, then
	// voting stopping for good.
	const n = 30
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			b, _ := json.Marshal(map[string]any{"option_id": p.Options[i%2].ID})
			r, err := client.Post(srv.URL+"/api/polls/"+p.ID+"/votes", "application/json", bytes.NewReader(b))
			if err != nil {
				return
			}
			r.Body.Close()
		}(i)
	}
	wg.Wait() // voting has now stopped for good

	// No further vote occurs from here on. The client must still reach the
	// final tally purely by draining whatever frames are pending/arrive.
	for {
		tally := readSSEFrame(t, reader)
		if sumTally(tally) == n {
			return
		}
		if sumTally(tally) > n {
			t.Fatalf("tally sum = %d, want at most %d", sumTally(tally), n)
		}
	}
}

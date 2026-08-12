package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/dennisschroeder/poll-tergeist/internal/poll"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	s, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := s.pool.Exec(context.Background(), "TRUNCATE polls CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func mustCreatePoll(t *testing.T, s *Store, question string, labels []string) poll.Poll {
	t.Helper()
	p, err := s.CreatePoll(context.Background(), question, labels)
	if err != nil {
		t.Fatalf("create poll: %v", err)
	}
	return p
}

// TestConcurrentDistinctVoters proves distinct voters never contend: every
// goroutine's vote lands, and the tally sums to exactly n.
func TestConcurrentDistinctVoters(t *testing.T) {
	s := openTestStore(t)
	p := mustCreatePoll(t, s, "Tabs or spaces?", []string{"Tabs", "Spaces"})

	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			token := fmt.Sprintf("voter-%d", i)
			optionID := p.Options[i%2].ID
			if _, err := s.InsertVote(context.Background(), p.ID, optionID, token); err != nil {
				t.Errorf("vote %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	total := sumTally(t, s, p.ID)
	if total != n {
		t.Fatalf("tally sum = %d, want %d", total, n)
	}
}

// TestConcurrentSameVoter proves UNIQUE(poll_id, voter_token) is the entire
// enforcement: under a real race, exactly one of n concurrent votes from the
// same voter wins and the rest come back as ErrAlreadyVoted.
func TestConcurrentSameVoter(t *testing.T) {
	s := openTestStore(t)
	p := mustCreatePoll(t, s, "Cats or dogs?", []string{"Cats", "Dogs"})

	const n = 50
	const token = "shared-voter"
	var wg sync.WaitGroup
	wg.Add(n)
	var mu sync.Mutex
	successes, conflicts := 0, 0
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			optionID := p.Options[i%2].ID
			_, err := s.InsertVote(context.Background(), p.ID, optionID, token)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrAlreadyVoted):
				conflicts++
			default:
				t.Errorf("vote %d: unexpected error: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("successes = %d, want 1", successes)
	}
	if conflicts != n-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, n-1)
	}
	if total := sumTally(t, s, p.ID); total != 1 {
		t.Fatalf("tally sum = %d, want 1", total)
	}
}

func sumTally(t *testing.T, s *Store, pollID string) int {
	t.Helper()
	tally, err := s.GetTally(context.Background(), pollID)
	if err != nil {
		t.Fatalf("get tally: %v", err)
	}
	total := 0
	for _, c := range tally.Counts {
		total += c
	}
	return total
}

// Package poll holds the domain types for poll-tergeist: Poll, Option, Vote,
// and the derived Tally. No I/O — persistence lives in internal/store.
package poll

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	MinOptions = 2
	MaxOptions = 5

	MaxQuestionLen = 280
	MaxLabelLen    = 120

	idAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	idLen      = 10
)

type Poll struct {
	ID        string
	Question  string
	CreatedAt time.Time
	Options   []Option
}

type Option struct {
	ID       int64
	PollID   string
	Position int
	Label    string
}

type Vote struct {
	ID         int64
	PollID     string
	OptionID   int64
	VoterToken string
	CreatedAt  time.Time
}

// Tally is a derived, never-stored count of votes per option.
type Tally struct {
	PollID string
	Counts map[int64]int // option ID -> vote count
}

// NewID returns a 10-char base62 poll ID, e.g. "aZ3kP9mQ2x".
func NewID() (string, error) {
	b := make([]byte, idLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("poll: generate id: %w", err)
	}
	for i, v := range b {
		b[i] = idAlphabet[int(v)%len(idAlphabet)]
	}
	return string(b), nil
}

// NewVoterToken returns a 32-char base62 opaque token for the voter cookie.
func NewVoterToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("poll: generate voter token: %w", err)
	}
	for i, v := range b {
		b[i] = idAlphabet[int(v)%len(idAlphabet)]
	}
	return string(b), nil
}

var (
	ErrBlankQuestion   = errors.New("poll: question must not be blank")
	ErrQuestionTooLong = fmt.Errorf("poll: question must be at most %d characters", MaxQuestionLen)
	ErrOptionCount     = fmt.Errorf("poll: must have between %d and %d options", MinOptions, MaxOptions)
	ErrBlankOption     = errors.New("poll: option label must not be blank")
	ErrOptionTooLong   = fmt.Errorf("poll: option label must be at most %d characters", MaxLabelLen)
)

// ValidateCreate checks a poll's question and option labels ahead of insert.
// Option count is a create-time input constraint, not a table CHECK — a
// single row can't see its siblings. See docs/adr for the trade-off.
func ValidateCreate(question string, optionLabels []string) error {
	q := strings.TrimSpace(question)
	if q == "" {
		return ErrBlankQuestion
	}
	if len(q) > MaxQuestionLen {
		return ErrQuestionTooLong
	}
	if len(optionLabels) < MinOptions || len(optionLabels) > MaxOptions {
		return ErrOptionCount
	}
	for _, l := range optionLabels {
		label := strings.TrimSpace(l)
		if label == "" {
			return ErrBlankOption
		}
		if len(label) > MaxLabelLen {
			return ErrOptionTooLong
		}
	}
	return nil
}

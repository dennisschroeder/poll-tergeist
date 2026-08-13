// Package store is the Postgres repository for poll-tergeist, built on pgx
// with hand-written SQL. Dedupe and vote finality are enforced by the
// UNIQUE(poll_id, voter_token) constraint, not by app-level locking.
package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dennisschroeder/poll-tergeist/internal/poll"
	"github.com/dennisschroeder/poll-tergeist/migrations"
)

var (
	ErrPollNotFound   = errors.New("store: poll not found")
	ErrOptionNotFound = errors.New("store: option not found for this poll")
	ErrAlreadyVoted   = errors.New("store: voter already voted in this poll")
)

const pgUniqueViolation = "23505"

type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres and applies embedded migrations.
func Open(ctx context.Context, connString string) (*Store, error) {
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	s := &Store{pool: pool}
	if err := s.applyMigrations(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) applyMigrations(ctx context.Context) error {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return fmt.Errorf("store: read migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		sql, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return fmt.Errorf("store: read migration %s: %w", name, err)
		}
		if _, err := s.pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("store: apply migration %s: %w", name, err)
		}
	}
	return nil
}

// CreatePoll inserts a poll and its options in one transaction. Option count
// and label bounds are validated by the caller via poll.ValidateCreate.
func (s *Store) CreatePoll(ctx context.Context, question string, optionLabels []string) (poll.Poll, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return poll.Poll{}, fmt.Errorf("store: begin create poll: %w", err)
	}
	defer tx.Rollback(ctx)

	// ON CONFLICT DO NOTHING turns a colliding ID into a no-op (RowsAffected
	// == 0), not a statement error — Postgres aborts a transaction after a
	// real statement error, so retrying an INSERT that errored inside this
	// same tx would fail every subsequent statement. This keeps the whole
	// poll-creation transaction usable across retries.
	const maxIDAttempts = 4
	var id string
	for attempt := 0; ; attempt++ {
		id, err = poll.NewID()
		if err != nil {
			return poll.Poll{}, err
		}
		tag, err := tx.Exec(ctx,
			`INSERT INTO polls (id, question) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
			id, question)
		if err != nil {
			return poll.Poll{}, fmt.Errorf("store: insert poll: %w", err)
		}
		if tag.RowsAffected() == 1 {
			break
		}
		if attempt >= maxIDAttempts-1 {
			return poll.Poll{}, fmt.Errorf("store: insert poll: exhausted %d id collision retries", maxIDAttempts)
		}
	}

	options := make([]poll.Option, len(optionLabels))
	for i, label := range optionLabels {
		var optionID int64
		err := tx.QueryRow(ctx,
			`INSERT INTO options (poll_id, position, label) VALUES ($1, $2, $3) RETURNING id`,
			id, i, label,
		).Scan(&optionID)
		if err != nil {
			return poll.Poll{}, fmt.Errorf("store: insert option: %w", err)
		}
		options[i] = poll.Option{ID: optionID, PollID: id, Position: i, Label: label}
	}

	var p poll.Poll
	err = tx.QueryRow(ctx, `SELECT id, question, created_at FROM polls WHERE id = $1`, id).
		Scan(&p.ID, &p.Question, &p.CreatedAt)
	if err != nil {
		return poll.Poll{}, fmt.Errorf("store: read back poll: %w", err)
	}
	p.Options = options

	if err := tx.Commit(ctx); err != nil {
		return poll.Poll{}, fmt.Errorf("store: commit create poll: %w", err)
	}
	return p, nil
}

// GetPoll fetches a poll with its options, ordered by display position.
func (s *Store) GetPoll(ctx context.Context, id string) (poll.Poll, error) {
	var p poll.Poll
	err := s.pool.QueryRow(ctx, `SELECT id, question, created_at FROM polls WHERE id = $1`, id).
		Scan(&p.ID, &p.Question, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return poll.Poll{}, ErrPollNotFound
	}
	if err != nil {
		return poll.Poll{}, fmt.Errorf("store: get poll: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, poll_id, position, label FROM options WHERE poll_id = $1 ORDER BY position`, id)
	if err != nil {
		return poll.Poll{}, fmt.Errorf("store: get options: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var o poll.Option
		if err := rows.Scan(&o.ID, &o.PollID, &o.Position, &o.Label); err != nil {
			return poll.Poll{}, fmt.Errorf("store: scan option: %w", err)
		}
		p.Options = append(p.Options, o)
	}
	if err := rows.Err(); err != nil {
		return poll.Poll{}, fmt.Errorf("store: iterate options: %w", err)
	}
	return p, nil
}

// GetTally returns the vote count per option, including options with zero
// votes. Counts are always computed from votes — never stored.
func (s *Store) GetTally(ctx context.Context, pollID string) (poll.Tally, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.id, COUNT(v.id)
		FROM options o
		LEFT JOIN votes v ON v.option_id = o.id
		WHERE o.poll_id = $1
		GROUP BY o.id`, pollID)
	if err != nil {
		return poll.Tally{}, fmt.Errorf("store: get tally: %w", err)
	}
	defer rows.Close()

	t := poll.Tally{PollID: pollID, Counts: map[int64]int{}}
	for rows.Next() {
		var optionID int64
		var count int
		if err := rows.Scan(&optionID, &count); err != nil {
			return poll.Tally{}, fmt.Errorf("store: scan tally row: %w", err)
		}
		t.Counts[optionID] = count
	}
	if err := rows.Err(); err != nil {
		return poll.Tally{}, fmt.Errorf("store: iterate tally: %w", err)
	}
	return t, nil
}

// InsertVote records one vote and reports only the persistence outcome —
// it does not read the tally back. The first vote per (poll, voter) wins;
// a repeat attempt returns ErrAlreadyVoted. option_id -> poll_id
// consistency is enforced twice: the WHERE EXISTS guard turns a mismatched
// option into a clean ErrOptionNotFound in this same round trip, and the
// composite FOREIGN KEY (poll_id, option_id) on votes (see migrations)
// makes an inconsistent pair impossible at the schema level regardless of
// which application code performs the insert.
//
// Deliberately not returning a tally: the vote command's HTTP response
// only acknowledges the mutation (201/409), it doesn't carry state — a
// caller reads current state via GetTally on the query/SSE path instead,
// as its own separate step. That keeps "the vote committed" independent
// from "a follow-up read succeeded": a failing follow-up read must never
// make a durably committed vote look unrecorded to the caller, and a
// command response must never fabricate tally data it didn't actually
// read (see docs/adr/0003-tally-fan-out-and-queue-design.md on why a
// committed vote must always produce an invalidation regardless).
func (s *Store) InsertVote(ctx context.Context, pollID string, optionID int64, voterToken string) error {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO votes (poll_id, option_id, voter_token)
		SELECT $1, $2, $3
		WHERE EXISTS (SELECT 1 FROM options WHERE id = $2 AND poll_id = $1)`,
		pollID, optionID, voterToken)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyVoted
		}
		return fmt.Errorf("store: insert vote: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrOptionNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgUniqueViolation
	}
	return false
}

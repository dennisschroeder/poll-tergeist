package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/dennisschroeder/poll-tergeist/internal/live"
	"github.com/dennisschroeder/poll-tergeist/internal/poll"
	"github.com/dennisschroeder/poll-tergeist/internal/store"
)

type api struct {
	store *store.Store
	hub   *live.Hub
}

type optionJSON struct {
	ID       int64  `json:"id"`
	Position int    `json:"position"`
	Label    string `json:"label"`
}

type pollJSON struct {
	ID        string           `json:"id"`
	Question  string           `json:"question"`
	CreatedAt string           `json:"created_at"`
	Options   []optionJSON     `json:"options"`
	Tally     map[string]int64 `json:"tally"`
}

func toPollJSON(p poll.Poll, tally poll.Tally) pollJSON {
	options := make([]optionJSON, len(p.Options))
	for i, o := range p.Options {
		options[i] = optionJSON{ID: o.ID, Position: o.Position, Label: o.Label}
	}
	return pollJSON{
		ID:        p.ID,
		Question:  p.Question,
		CreatedAt: p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Options:   options,
		Tally:     tallyJSON(tally),
	}
}

// tallyJSON flattens a Tally's int64 option IDs to string keys, since Go's
// encoding/json requires string map keys.
func tallyJSON(t poll.Tally) map[string]int64 {
	counts := make(map[string]int64, len(t.Counts))
	for id, c := range t.Counts {
		counts[fmt.Sprintf("%d", id)] = int64(c)
	}
	return counts
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type createPollRequest struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

func (a *api) createPoll(w http.ResponseWriter, r *http.Request) {
	var req createPollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := poll.ValidateCreate(req.Question, req.Options); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	question := strings.TrimSpace(req.Question)
	labels := make([]string, len(req.Options))
	for i, l := range req.Options {
		labels[i] = strings.TrimSpace(l)
	}

	p, err := a.store.CreatePoll(r.Context(), question, labels)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create poll")
		return
	}
	zeroTally := poll.Tally{PollID: p.ID, Counts: map[int64]int{}}
	for _, o := range p.Options {
		zeroTally.Counts[o.ID] = 0
	}
	writeJSON(w, http.StatusCreated, toPollJSON(p, zeroTally))
}

func (a *api) getPoll(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, tally, err := a.loadPollAndTally(r, id)
	if err != nil {
		writePollLoadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPollJSON(p, tally))
}

func (a *api) loadPollAndTally(r *http.Request, id string) (poll.Poll, poll.Tally, error) {
	p, err := a.store.GetPoll(r.Context(), id)
	if err != nil {
		return poll.Poll{}, poll.Tally{}, err
	}
	tally, err := a.store.GetTally(r.Context(), id)
	if err != nil {
		return poll.Poll{}, poll.Tally{}, err
	}
	return p, tally, nil
}

func writePollLoadError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrPollNotFound) {
		writeError(w, http.StatusNotFound, "poll not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to load poll")
}

type voteRequest struct {
	OptionID int64 `json:"option_id"`
}

func (a *api) vote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if _, err := a.store.GetPoll(r.Context(), id); err != nil {
		writePollLoadError(w, err)
		return
	}

	var req voteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	voterToken := voterTokenFrom(r)
	tally, err := a.store.InsertVote(r.Context(), id, req.OptionID, voterToken)
	switch {
	case err == nil:
		a.publishTally(id, tally)
		writeJSON(w, http.StatusCreated, tallyResponse(tally))
	case errors.Is(err, store.ErrAlreadyVoted):
		writeJSON(w, http.StatusConflict, withError(tallyResponse(tally), "already voted"))
	case errors.Is(err, store.ErrOptionNotFound):
		writeError(w, http.StatusBadRequest, "option does not belong to this poll")
	default:
		writeError(w, http.StatusInternalServerError, "failed to record vote")
	}
}

func tallyResponse(t poll.Tally) map[string]any {
	return map[string]any{"tally": tallyJSON(t)}
}

func withError(m map[string]any, msg string) map[string]any {
	m["error"] = msg
	return m
}

func (a *api) publishTally(pollID string, t poll.Tally) {
	payload, err := json.Marshal(tallyJSON(t))
	if err != nil {
		return
	}
	a.hub.Publish(pollID, payload)
}

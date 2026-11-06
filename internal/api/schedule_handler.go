package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Scheduling a post through the API. Until now only the ops bot's /rejalash
// could set publish_at (migration 000007); the bot's scheduler still does the
// publishing, so a post scheduled here goes live the same way and fires the
// same post.published notification.

// How far ahead a schedule may go: further is more likely a typo in the year
// than a plan.
const maxScheduleAhead = 366 * 24 * time.Hour

func (s *Server) handleSchedulePost(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// publish_at: an RFC 3339 time to schedule, null to cancel.
	var body struct {
		PublishAt *time.Time `json:"publish_at"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now()
	if at := body.PublishAt; at != nil {
		// The scheduler publishes anything already due on its next tick, so
		// a time in the past would be "publish now" in disguise.
		if !at.After(now.Add(time.Minute)) {
			writeError(w, http.StatusBadRequest, "publish_at must be at least a minute in the future")
			return
		}
		if at.After(now.Add(maxScheduleAhead)) {
			writeError(w, http.StatusBadRequest, "publish_at is more than a year ahead")
			return
		}
	}

	var draft bool
	err = s.pool.QueryRow(r.Context(), `SELECT draft FROM posts WHERE id = $1`, id).Scan(&draft)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "post not found")
		return
	}
	if err != nil {
		s.serverError(w, r, "failed to load post", err)
		return
	}
	if !draft {
		writeError(w, http.StatusConflict, "only a draft can be scheduled; this post is already published")
		return
	}
	if _, err := s.pool.Exec(r.Context(),
		`UPDATE posts SET publish_at = $2, updated_at = now() WHERE id = $1 AND draft`,
		id, body.PublishAt); err != nil {
		s.serverError(w, r, "failed to schedule post", err)
		return
	}
	post, err := s.q.GetPostByID(r.Context(), id)
	if err != nil {
		s.serverError(w, r, "failed to load post", err)
		return
	}
	writeJSON(w, http.StatusOK, toPostResponse(post))
}

/* ---------------------------- English practice ---------------------------- */

// Writing a practice session through the API: the same row the ops bot's
// /qildim writes (migration 000015), for the MCP server's log_practice.

var growthKinds = map[string]bool{
	"writing": true, "speaking": true, "listening": true, "reading": true,
	"vocab": true, "grammar": true, "mock": true, "other": true,
}

type growthLogInput struct {
	Day     string `json:"day"` // YYYY-MM-DD, Tashkent; default today
	Kind    string `json:"kind"`
	Minutes int    `json:"minutes"`
	Note    string `json:"note"`
}

// How far back a session may be logged: a forgotten day last week is real,
// a date last year is a mistake.
const growthLogMaxAgeDays = 31

func (in *growthLogInput) validate(now time.Time) error {
	in.Kind = strings.TrimSpace(strings.ToLower(in.Kind))
	in.Note = strings.TrimSpace(in.Note)
	if !growthKinds[in.Kind] {
		return errors.New("kind must be one of writing, speaking, listening, reading, vocab, grammar, mock, other")
	}
	if in.Minutes < 1 || in.Minutes > 600 {
		return errors.New("minutes must be between 1 and 600")
	}
	if len([]rune(in.Note)) > 300 {
		return errors.New("note is longer than 300 characters")
	}
	today := now.In(tashkent).Format(dateLayout)
	if in.Day == "" {
		in.Day = today
	}
	day, err := time.ParseInLocation(dateLayout, in.Day, tashkent)
	if err != nil {
		return errors.New("day must be YYYY-MM-DD")
	}
	if in.Day > today {
		return errors.New("day is in the future")
	}
	if day.Before(now.In(tashkent).AddDate(0, 0, -growthLogMaxAgeDays)) {
		return errors.New("day is more than a month ago")
	}
	return nil
}

func (s *Server) handleCreateGrowthLog(w http.ResponseWriter, r *http.Request) {
	var in growthLogInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := in.validate(time.Now()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var (
		id string
		at time.Time
	)
	if err := s.pool.QueryRow(r.Context(), `
		INSERT INTO growth_logs (day, kind, minutes, note)
		VALUES ($1::date, $2, $3, $4)
		RETURNING id::text, created_at`,
		in.Day, in.Kind, in.Minutes, in.Note).Scan(&id, &at); err != nil {
		s.serverError(w, r, "failed to save practice", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "day": in.Day, "kind": in.Kind, "minutes": in.Minutes, "note": in.Note, "created_at": at,
	})
}

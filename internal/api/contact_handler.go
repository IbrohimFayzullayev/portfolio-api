package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ibrohimcoder/portfolio-api/internal/db"
)

// The contact form.
//
// A second public write endpoint, with the same shape as the first one
// (invitations): validate, store, queue a notification, answer. The row is
// written before anything tries to deliver it, so the visitor's message
// survives Telegram being unreachable — it is merely announced late.

const (
	contactMinMessage = 10
	contactMaxMessage = 4000
	contactMaxName    = 120
	contactMaxEmail   = 200

	// The notification carries an excerpt; the whole message is one tap away
	// in the dashboard and would otherwise push Telegram's 4096 limit.
	contactNotifyExcerpt = 800
)

type contactInput struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Message string `json:"message"`
	Locale  string `json:"locale"`
	// Honeypot. The form renders this field where no person will find it;
	// a script that fills in every input it sees fills this one too.
	Website string `json:"website"`
}

func (in *contactInput) normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	in.Message = strings.TrimSpace(in.Message)
	in.Locale = strings.TrimSpace(in.Locale)
}

func (in *contactInput) validate() error {
	if in.Name == "" {
		return fmt.Errorf("name is required")
	}
	if utf8.RuneCountInString(in.Name) > contactMaxName {
		return fmt.Errorf("name is too long")
	}
	// Required, because a message nobody can answer is a dead end for the
	// person who wrote it.
	if len(in.Email) > contactMaxEmail || !emailRe.MatchString(in.Email) {
		return fmt.Errorf("a valid email is required")
	}
	switch n := utf8.RuneCountInString(in.Message); {
	case n < contactMinMessage:
		return fmt.Errorf("message must be at least %d characters", contactMinMessage)
	case n > contactMaxMessage:
		return fmt.Errorf("message must be at most %d characters", contactMaxMessage)
	}
	return nil
}

func (s *Server) handleCreateContact(w http.ResponseWriter, r *http.Request) {
	var in contactInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.normalize()

	// Answer a bot exactly as a person would be answered. A distinct status
	// here would only teach it which field to leave empty next time.
	if in.Website != "" {
		writeJSON(w, http.StatusCreated, map[string]string{"status": "received"})
		return
	}

	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// The per-IP limit is in the route; this one is for everything it cannot
	// see — many addresses, one target. Past it the form is closed for the
	// rest of the hour rather than turning the phone into a spam feed.
	if !s.contactBudget.allow("all") {
		writeError(w, http.StatusTooManyRequests, "too many messages right now, please try again later")
		return
	}

	locale := in.Locale
	if locale != "en" && locale != "uz" {
		locale = ""
	}

	msg, err := s.q.CreateContactMessage(r.Context(), db.CreateContactMessageParams{
		Name:      in.Name,
		Email:     in.Email,
		Message:   in.Message,
		Locale:    locale,
		UserAgent: clampField(r.UserAgent(), 400),
	})
	if err != nil {
		s.serverError(w, r, "failed to save message", err)
		return
	}

	s.notify("contact.created", map[string]any{
		"id":      msg.ID.String(),
		"name":    msg.Name,
		"email":   msg.Email,
		"locale":  msg.Locale,
		"message": truncateChars(msg.Message, contactNotifyExcerpt),
	})

	writeJSON(w, http.StatusCreated, map[string]string{"status": "received"})
}

/* ------------------------------ admin side ------------------------------- */

type contactMessageResponse struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	Email     string     `json:"email"`
	Message   string     `json:"message"`
	Locale    string     `json:"locale"`
	UserAgent string     `json:"user_agent"`
	ReadAt    *time.Time `json:"read_at"`
	CreatedAt time.Time  `json:"created_at"`
}

type contactMessageListResponse struct {
	Items  []contactMessageResponse `json:"items"`
	Total  int64                    `json:"total"`
	Unread int64                    `json:"unread"`
}

func toContactMessageResponse(m db.ContactMessage) contactMessageResponse {
	return contactMessageResponse{
		ID:        m.ID,
		Name:      m.Name,
		Email:     m.Email,
		Message:   m.Message,
		Locale:    m.Locale,
		UserAgent: m.UserAgent,
		ReadAt:    m.ReadAt,
		CreatedAt: m.CreatedAt,
	}
}

func (s *Server) handleListContactMessages(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100, 1, 500)
	offset := queryInt(r, "offset", 0, 0, 1_000_000)

	items, err := s.q.ListContactMessages(r.Context(), db.ListContactMessagesParams{
		Limit:  int32(limit),
		Offset: int32(offset),
	})
	if err != nil {
		s.serverError(w, r, "failed to list messages", err)
		return
	}
	total, err := s.q.CountContactMessages(r.Context())
	if err != nil {
		s.serverError(w, r, "failed to count messages", err)
		return
	}
	unread, err := s.q.CountUnreadContactMessages(r.Context())
	if err != nil {
		s.serverError(w, r, "failed to count unread messages", err)
		return
	}

	out := make([]contactMessageResponse, 0, len(items))
	for _, m := range items {
		out = append(out, toContactMessageResponse(m))
	}
	writeJSON(w, http.StatusOK, contactMessageListResponse{Items: out, Total: total, Unread: unread})
}

func (s *Server) handleMarkContactMessageRead(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	rows, err := s.q.MarkContactMessageRead(r.Context(), id)
	if err != nil {
		s.serverError(w, r, "failed to update message", err)
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "message not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteContactMessage(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	if err := s.q.DeleteContactMessage(r.Context(), id); err != nil {
		s.serverError(w, r, "failed to delete message", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

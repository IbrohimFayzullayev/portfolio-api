package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/ibrohimcoder/portfolio-api/internal/db"
)

func (s *Server) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	var in invitationInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	source := clampField(in.Source, 40)
	if source == "" {
		source = "planner"
	}

	_, err := s.q.CreateInvitation(r.Context(), db.CreateInvitationParams{
		Source:     source,
		SessionID:  clampField(in.SessionID, 80),
		EventDate:  parseContentDate(in.Date),
		EventTime:  clampField(in.Time, 40),
		FoodID:     clampField(in.FoodID, 60),
		FoodLabel:  clampField(in.FoodLabel, 120),
		FoodEmoji:  clampField(in.FoodEmoji, 16),
		PlaceID:    clampField(in.PlaceID, 60),
		PlaceLabel: clampField(in.PlaceLabel, 120),
		PlaceEmoji: clampField(in.PlaceEmoji, 16),
		InviteText: clampField(in.InviteText, 4000),
		UserAgent:  clampField(r.UserAgent(), 400),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save invitation")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"status": "received"})
}

func (s *Server) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100, 1, 500)
	offset := queryInt(r, "offset", 0, 0, 1_000_000)

	items, err := s.q.ListInvitations(r.Context(), db.ListInvitationsParams{
		Limit:  int32(limit),
		Offset: int32(offset),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list invitations")
		return
	}

	total, err := s.q.CountInvitations(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count invitations")
		return
	}

	writeJSON(w, http.StatusOK, invitationListResponse{
		Items: toInvitationResponses(items),
		Total: total,
	})
}

func (s *Server) handleGetInvitation(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	item, err := s.q.GetInvitationByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "invitation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load invitation")
		return
	}
	writeJSON(w, http.StatusOK, toInvitationResponse(item))
}

func (s *Server) handleDeleteInvitation(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	if err := s.q.DeleteInvitation(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete invitation")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func queryInt(r *http.Request, key string, def, min, max int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

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

	guestName := clampField(in.GuestName, 120)
	// Clamped to the column widths, so an over-long value is trimmed here
	// rather than rejected by PostgreSQL with the whole submission.
	venueID := clampField(in.VenueID, 64)
	venueName := clampField(in.VenueName, 80)

	_, err := s.q.CreateInvitation(r.Context(), db.CreateInvitationParams{
		Source:      source,
		SessionID:   clampField(in.SessionID, 80),
		GuestName:   guestName,
		EventDate:   parseContentDate(in.Date),
		EventTime:   clampField(in.Time, 40),
		FoodID:      clampField(in.FoodID, 60),
		FoodLabel:   clampField(in.FoodLabel, 120),
		FoodEmoji:   clampField(in.FoodEmoji, 16),
		PlaceID:     clampField(in.PlaceID, 60),
		PlaceLabel:  clampField(in.PlaceLabel, 120),
		PlaceEmoji:  clampField(in.PlaceEmoji, 16),
		VenueID:     venueID,
		VenueName:   venueName,
		VenueCustom: in.VenueCustom,
		InviteText:  clampField(in.InviteText, 4000),
		UserAgent:   clampField(r.UserAgent(), 400),
	})
	if err != nil {
		s.serverError(w, r, "failed to save invitation", err)
		return
	}

	// Queued, not sent: the row is already safe in the database, so the visitor
	// gets their answer whether or not Telegram is reachable.
	s.notify("invitation.created", map[string]any{
		"source":       source,
		"guest_name":   guestName,
		"date":         in.Date,
		"time":         clampField(in.Time, 40),
		"food_label":   clampField(in.FoodLabel, 120),
		"food_emoji":   clampField(in.FoodEmoji, 16),
		"place_label":  clampField(in.PlaceLabel, 120),
		"place_emoji":  clampField(in.PlaceEmoji, 16),
		"venue_name":   venueName,
		"venue_custom": in.VenueCustom,
	})

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
		s.serverError(w, r, "failed to list invitations", err)
		return
	}

	total, err := s.q.CountInvitations(r.Context())
	if err != nil {
		s.serverError(w, r, "failed to count invitations", err)
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
		s.serverError(w, r, "failed to load invitation", err)
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
		s.serverError(w, r, "failed to delete invitation", err)
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

package api

import (
	"context"
	"net/http"
	"testing"
)

// The public invitation endpoint is the one the standalone planner site posts
// to, and it rejects unknown fields on purpose — which makes the exact JSON
// keys part of the contract with a codebase that ships separately. These tests
// exercise that contract against a real database, because the failure mode is
// silent from here: the site just starts getting 400s.

func samplePlannerSubmission(overrides map[string]any) map[string]any {
	body := map[string]any{
		"source":       "planner",
		"session_id":   "integration-session",
		"guest_name":   "Aziza Karimova",
		"date":         "2026-09-20",
		"time":         "19:00",
		"food_id":      "pizza",
		"food_label":   "Pitsa",
		"food_emoji":   "🍕",
		"place_id":     "park",
		"place_label":  "Bog'",
		"place_emoji":  "🌳",
		"venue_id":     "chorsu",
		"venue_name":   "Chorsu bog'i",
		"venue_custom": false,
		"invite_text":  "Salom!",
	}
	for k, v := range overrides {
		body[k] = v
	}
	return body
}

func TestIntegration_InvitationWithGuestName(t *testing.T) {
	s := setupServer(t)
	ctx := context.Background()

	if _, err := s.pool.Exec(ctx, `DELETE FROM invitations WHERE session_id LIKE 'integration-%'`); err != nil {
		t.Fatalf("clean up: %v", err)
	}

	rec := do(t, s, http.MethodPost, "/api/v1/public/invitations", "",
		samplePlannerSubmission(nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var stored string
	if err := s.pool.QueryRow(ctx,
		`SELECT guest_name FROM invitations WHERE session_id = 'integration-session'`,
	).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != "Aziza Karimova" {
		t.Errorf("guest_name = %q, want %q", stored, "Aziza Karimova")
	}
}

// The field is optional: the site may not ask for a name, and every row
// written before the column existed has none.
func TestIntegration_InvitationWithoutGuestName(t *testing.T) {
	s := setupServer(t)
	ctx := context.Background()

	body := samplePlannerSubmission(map[string]any{"session_id": "integration-no-name"})
	delete(body, "guest_name")

	if _, err := s.pool.Exec(ctx,
		`DELETE FROM invitations WHERE session_id = 'integration-no-name'`); err != nil {
		t.Fatalf("clean up: %v", err)
	}

	rec := do(t, s, http.MethodPost, "/api/v1/public/invitations", "", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var stored string
	if err := s.pool.QueryRow(ctx,
		`SELECT guest_name FROM invitations WHERE session_id = 'integration-no-name'`,
	).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != "" {
		t.Errorf("guest_name = %q, want empty", stored)
	}
}

// A long name is clamped rather than refused: the visitor typed it, and
// losing the whole submission over a field length would be the wrong trade.
func TestIntegration_InvitationGuestNameIsClamped(t *testing.T) {
	s := setupServer(t)
	ctx := context.Background()

	long := ""
	for i := 0; i < 300; i++ {
		long += "a"
	}

	if _, err := s.pool.Exec(ctx,
		`DELETE FROM invitations WHERE session_id = 'integration-long-name'`); err != nil {
		t.Fatalf("clean up: %v", err)
	}

	rec := do(t, s, http.MethodPost, "/api/v1/public/invitations", "",
		samplePlannerSubmission(map[string]any{
			"session_id": "integration-long-name",
			"guest_name": long,
		}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var stored string
	if err := s.pool.QueryRow(ctx,
		`SELECT guest_name FROM invitations WHERE session_id = 'integration-long-name'`,
	).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len([]rune(stored)) != 120 {
		t.Errorf("stored %d characters, want 120", len([]rune(stored)))
	}
}

// An unknown field still fails, and that is deliberate: it is how a typo in
// the sending codebase is caught immediately instead of being dropped.
func TestIntegration_InvitationRejectsUnknownField(t *testing.T) {
	s := setupServer(t)

	rec := do(t, s, http.MethodPost, "/api/v1/public/invitations", "",
		samplePlannerSubmission(map[string]any{"guestName": "camelCase typo"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body = %s)", rec.Code, rec.Body.String())
	}
}

// The venue is the exact place, and venue_custom is what separates a name off
// the list from one the visitor typed — which is the difference between a
// place you can look up and one you may have to ask about.
func TestIntegration_InvitationVenue(t *testing.T) {
	s := setupServer(t)
	ctx := context.Background()

	if _, err := s.pool.Exec(ctx,
		`DELETE FROM invitations WHERE session_id = 'integration-venue'`); err != nil {
		t.Fatalf("clean up: %v", err)
	}

	rec := do(t, s, http.MethodPost, "/api/v1/public/invitations", "",
		samplePlannerSubmission(map[string]any{
			"session_id":   "integration-venue",
			"venue_id":     "custom",
			"venue_name":   "Bobomning bog'i",
			"venue_custom": true,
		}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var (
		venueID, venueName string
		venueCustom        bool
	)
	if err := s.pool.QueryRow(ctx, `
		SELECT venue_id, venue_name, venue_custom
		FROM invitations WHERE session_id = 'integration-venue'`,
	).Scan(&venueID, &venueName, &venueCustom); err != nil {
		t.Fatalf("read back: %v", err)
	}

	if venueID != "custom" || venueName != "Bobomning bog'i" || !venueCustom {
		t.Errorf("stored %q / %q / %v", venueID, venueName, venueCustom)
	}
}

// venue_custom defaults to false and the names default to empty, so a site
// that has not been updated yet keeps working unchanged.
func TestIntegration_InvitationWithoutVenue(t *testing.T) {
	s := setupServer(t)
	ctx := context.Background()

	body := samplePlannerSubmission(map[string]any{"session_id": "integration-no-venue"})
	delete(body, "venue_id")
	delete(body, "venue_name")
	delete(body, "venue_custom")

	if _, err := s.pool.Exec(ctx,
		`DELETE FROM invitations WHERE session_id = 'integration-no-venue'`); err != nil {
		t.Fatalf("clean up: %v", err)
	}

	rec := do(t, s, http.MethodPost, "/api/v1/public/invitations", "", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var (
		venueName   string
		venueCustom bool
	)
	if err := s.pool.QueryRow(ctx, `
		SELECT venue_name, venue_custom
		FROM invitations WHERE session_id = 'integration-no-venue'`,
	).Scan(&venueName, &venueCustom); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if venueName != "" || venueCustom {
		t.Errorf("defaults are wrong: %q / %v", venueName, venueCustom)
	}
}

// The column is VARCHAR(80): a longer name must be trimmed by the handler, not
// rejected by PostgreSQL — losing the whole invitation over one field would be
// the wrong trade.
func TestIntegration_InvitationVenueNameIsClamped(t *testing.T) {
	s := setupServer(t)
	ctx := context.Background()

	long := ""
	for i := 0; i < 200; i++ {
		long += "b"
	}

	if _, err := s.pool.Exec(ctx,
		`DELETE FROM invitations WHERE session_id = 'integration-long-venue'`); err != nil {
		t.Fatalf("clean up: %v", err)
	}

	rec := do(t, s, http.MethodPost, "/api/v1/public/invitations", "",
		samplePlannerSubmission(map[string]any{
			"session_id": "integration-long-venue",
			"venue_name": long,
			"venue_id":   long,
		}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var venueID, venueName string
	if err := s.pool.QueryRow(ctx, `
		SELECT venue_id, venue_name
		FROM invitations WHERE session_id = 'integration-long-venue'`,
	).Scan(&venueID, &venueName); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len([]rune(venueID)) != 64 {
		t.Errorf("venue_id: %d characters, want 64", len([]rune(venueID)))
	}
	if len([]rune(venueName)) != 80 {
		t.Errorf("venue_name: %d characters, want 80", len([]rune(venueName)))
	}
}

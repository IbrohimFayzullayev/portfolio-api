package api

import (
	"net/http"
	"testing"
	"time"
)

func TestIntegration_SchedulePost(t *testing.T) {
	s := setupServer(t)
	token := registerAdmin(t, s)

	rec := do(t, s, http.MethodPost, "/api/v1/posts", token, samplePost(map[string]any{"draft": true}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var p postResponse
	decode(t, rec, &p)
	path := "/api/v1/posts/" + p.ID.String() + "/schedule"

	at := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	rec = do(t, s, http.MethodPatch, path, token, map[string]any{"publish_at": at})
	if rec.Code != http.StatusOK {
		t.Fatalf("schedule: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec, &p)
	if p.PublishAt == nil || !p.PublishAt.Equal(at) || !p.Draft {
		t.Fatalf("scheduled post: %+v", p)
	}

	for name, body := range map[string]map[string]any{
		"past":      {"publish_at": time.Now().Add(-time.Hour)},
		"too close": {"publish_at": time.Now().Add(10 * time.Second)},
		"too far":   {"publish_at": time.Now().AddDate(2, 0, 0)},
	} {
		if rec := do(t, s, http.MethodPatch, path, token, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
	if rec := do(t, s, http.MethodPatch, path, "", map[string]any{"publish_at": at}); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", rec.Code)
	}

	// Publishing by hand ends the schedule, so unpublishing later cannot
	// trigger it.
	rec = do(t, s, http.MethodPatch, "/api/v1/posts/"+p.ID.String()+"/publish", token, map[string]any{"draft": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("publish: %d", rec.Code)
	}
	p = postResponse{} // publish_at is omitted when empty: decode into a fresh value
	decode(t, rec, &p)
	var left *time.Time
	s.pool.QueryRow(t.Context(), `SELECT publish_at FROM posts WHERE id = $1`, p.ID).Scan(&left)
	if p.PublishAt != nil || left != nil {
		t.Errorf("a hand publish must clear publish_at: response %v, row %v", p.PublishAt, left)
	}
	if rec := do(t, s, http.MethodPatch, path, token, map[string]any{"publish_at": at}); rec.Code != http.StatusConflict {
		t.Errorf("scheduling a published post: %d, want 409", rec.Code)
	}

	// Back to draft, schedule, cancel.
	do(t, s, http.MethodPatch, "/api/v1/posts/"+p.ID.String()+"/publish", token, map[string]any{"draft": true})
	do(t, s, http.MethodPatch, path, token, map[string]any{"publish_at": at})
	rec = do(t, s, http.MethodPatch, path, token, map[string]any{"publish_at": nil})
	p = postResponse{}
	decode(t, rec, &p)
	if rec.Code != http.StatusOK || p.PublishAt != nil {
		t.Errorf("cancel: %d %+v", rec.Code, p.PublishAt)
	}
	if rec := do(t, s, http.MethodPatch, "/api/v1/posts/00000000-0000-0000-0000-000000000000/schedule", token,
		map[string]any{"publish_at": at}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown post: %d", rec.Code)
	}
}

func TestIntegration_GrowthLog(t *testing.T) {
	s := setupServer(t)
	if _, err := s.pool.Exec(t.Context(), `DELETE FROM growth_logs WHERE note = 'integration'`); err != nil {
		t.Fatal(err)
	}
	token := registerAdmin(t, s)

	rec := do(t, s, http.MethodPost, "/api/v1/growth/logs", token,
		map[string]any{"kind": "writing", "minutes": 45, "note": "integration"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID, Day, Kind string
		Minutes       int
	}
	decode(t, rec, &out)
	if out.ID == "" || out.Day != time.Now().In(tashkent).Format(dateLayout) || out.Minutes != 45 {
		t.Errorf("created: %+v", out)
	}

	tomorrow := time.Now().In(tashkent).AddDate(0, 0, 1).Format(dateLayout)
	for name, body := range map[string]map[string]any{
		"kind":    {"kind": "dancing", "minutes": 10},
		"minutes": {"kind": "reading", "minutes": 0},
		"future":  {"kind": "reading", "minutes": 10, "day": tomorrow},
		"old":     {"kind": "reading", "minutes": 10, "day": "2020-01-01"},
	} {
		if rec := do(t, s, http.MethodPost, "/api/v1/growth/logs", token, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
	if rec := do(t, s, http.MethodPost, "/api/v1/growth/logs", "", map[string]any{"kind": "reading", "minutes": 5}); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", rec.Code)
	}
	// Reads stay as they were.
	if rec := do(t, s, http.MethodGet, "/api/v1/growth/summary", token, nil); rec.Code != http.StatusOK {
		t.Errorf("summary: %d", rec.Code)
	}
	s.pool.Exec(t.Context(), `DELETE FROM growth_logs WHERE note = 'integration'`)
}

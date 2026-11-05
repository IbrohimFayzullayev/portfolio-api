package api

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestIntegration_AgentChanges(t *testing.T) {
	s := setupServer(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE agent_changes RESTART IDENTITY; DELETE FROM notifications WHERE kind = 'agent.change'`); err != nil {
		t.Fatal(err)
	}
	token := registerAdmin(t, s)

	change := map[string]any{
		"tool": "update_post", "target_type": "post",
		"targets": []map[string]string{{"id": "11111111-1111-1111-1111-111111111111", "label": "uz/vps-deploy"}},
		"changes": map[string]any{"title": map[string]string{"from": "Eski", "to": "Yangi"}},
		"ok":      true,
	}

	if rec := do(t, s, http.MethodPost, "/api/v1/agent-changes", "", change); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d", rec.Code)
	}
	if rec := do(t, s, http.MethodPost, "/api/v1/agent-changes", token, change); rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// A read mark is logged but not announced.
	if rec := do(t, s, http.MethodPost, "/api/v1/agent-changes", token, map[string]any{
		"tool": "mark_message_read", "target_type": "message", "ok": true,
		"targets": []map[string]string{{"id": "x", "label": "Aziz"}},
	}); rec.Code != http.StatusCreated {
		t.Fatalf("message: status = %d", rec.Code)
	}
	// A failed write is announced as a warning.
	if rec := do(t, s, http.MethodPost, "/api/v1/agent-changes", token, map[string]any{
		"tool": "set_post_published", "target_type": "post", "ok": false, "error": "API 404: post not found",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("failure: status = %d", rec.Code)
	}

	for name, bad := range map[string]map[string]any{
		"tool":    {"tool": "Update Post", "target_type": "post", "ok": true},
		"type":    {"tool": "x", "target_type": "user", "ok": true},
		"changes": {"tool": "x", "target_type": "post", "ok": true, "changes": []int{1}},
		"actor":   {"actor": "Claude!", "tool": "x", "target_type": "post", "ok": true},
	} {
		if rec := do(t, s, http.MethodPost, "/api/v1/agent-changes", token, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}

	rec := do(t, s, http.MethodGet, "/api/v1/agent-changes?limit=10", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status = %d", rec.Code)
	}
	var out struct {
		Items []struct {
			Actor   string         `json:"actor"`
			Tool    string         `json:"tool"`
			OK      bool           `json:"ok"`
			Changes map[string]any `json:"changes"`
		} `json:"items"`
	}
	decode(t, rec, &out)
	if len(out.Items) != 3 || out.Items[0].Tool != "set_post_published" || out.Items[2].Actor != "mcp" {
		t.Fatalf("list, newest first: %+v", out.Items)
	}
	if out.Items[2].Changes["title"] == nil {
		t.Errorf("changes are kept whole: %+v", out.Items[2].Changes)
	}

	// notify is fire-and-forget; give the goroutines a moment.
	var queued, warnings int
	for i := 0; i < 50; i++ {
		s.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE severity = 1)
			FROM notifications WHERE kind = 'agent.change'`).Scan(&queued, &warnings)
		if queued >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if queued != 2 || warnings != 1 {
		t.Errorf("notifications: %d queued (want 2: not the read mark), %d warnings (want 1)", queued, warnings)
	}
}

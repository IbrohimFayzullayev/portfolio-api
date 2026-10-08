package api

import (
	"context"
	"net/http"
	"testing"
)

func TestIntegration_SystemOverview(t *testing.T) {
	s := setupServer(t)
	ctx := context.Background()
	token := registerAdmin(t, s)

	if _, err := s.pool.Exec(ctx, `TRUNCATE notifications, error_groups, health_checks`); err != nil {
		t.Fatalf("clean up: %v", err)
	}

	if rec := do(t, s, http.MethodGet, "/api/v1/system", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without auth: status = %d, want 401", rec.Code)
	}

	// An empty stack is a valid state, not an error: a fresh install has no
	// history yet and the page must still load.
	rec := do(t, s, http.MethodGet, "/api/v1/system", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var empty systemOverviewResponse
	decode(t, rec, &empty)
	if empty.Errors == nil || empty.Health == nil || empty.Deploys == nil {
		t.Error("empty lists must be [] in JSON, not null")
	}
	if empty.Backup != nil {
		t.Errorf("backup = %+v, want null when none has ever finished", empty.Backup)
	}

	seed := []string{
		`INSERT INTO health_checks (target, ok, latency_ms, checked_at) VALUES
			('Sayt', true, 120, now() - interval '3 hours'),
			('Sayt', false, 0,  now() - interval '2 hours'),
			('Sayt', true, 110, now() - interval '1 hour'),
			('Sayt', true, 100, now())`,
		`INSERT INTO notifications (kind, payload, sent_at) VALUES
			('deploy.finished', '{"service":"web","sha":"abc1234def"}', now()),
			('backup.finished', '{"size":"1.2M","file":"portfolio-2026-10-07.sql.gz"}', now())`,
		`INSERT INTO notifications (kind, payload) VALUES ('reminder', '{"text":"pending"}')`,
		`INSERT INTO notifications (kind, payload, attempts) VALUES ('reminder', '{"text":"parked"}', 5)`,
		`INSERT INTO error_groups (fingerprint, service, route, message, count)
			VALUES ('abcdef123456', 'api', 'GET /api/v1/posts', 'failed to list posts', 3)`,
	}
	for _, q := range seed {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}

	rec = do(t, s, http.MethodGet, "/api/v1/system", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out systemOverviewResponse
	decode(t, rec, &out)

	if len(out.Health) != 1 {
		t.Fatalf("health targets = %d, want 1", len(out.Health))
	}
	h := out.Health[0]
	if !h.OK || h.LatencyMS != 100 {
		t.Errorf("latest check = ok %v, %d ms; want the most recent row", h.OK, h.LatencyMS)
	}
	if h.Checks7d != 4 || h.Uptime7d != 75 {
		t.Errorf("uptime = %.1f%% over %d checks, want 75.0%% over 4", h.Uptime7d, h.Checks7d)
	}
	if h.LastFailure == nil {
		t.Error("last_failure missing")
	}

	if len(out.Deploys) != 1 || out.Deploys[0].Service != "web" || out.Deploys[0].SHA != "abc1234def" {
		t.Errorf("deploys = %+v", out.Deploys)
	}
	if out.Outbox.Pending != 1 || out.Outbox.Parked != 1 || out.Outbox.LastSentAt == nil {
		t.Errorf("outbox = %+v, want 1 pending, 1 parked, a last_sent_at", out.Outbox)
	}
	if out.Backup == nil || out.Backup.Size != "1.2M" {
		t.Errorf("backup = %+v", out.Backup)
	}
	if len(out.Errors) != 1 || out.Errors[0].Count != 3 {
		t.Errorf("errors = %+v", out.Errors)
	}
	if out.Bot == nil {
		t.Error("bot heartbeat missing (the migration seeds the row)")
	}
}

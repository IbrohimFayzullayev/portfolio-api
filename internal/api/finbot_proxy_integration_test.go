package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestIntegration_FinbotProxy(t *testing.T) {
	s := setupServer(t)
	admin := registerAdmin(t, s)

	var (
		mu   sync.Mutex
		seen []string
	)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" key="+r.Header.Get("X-Finbot-Admin-Key")+
			" cookie="+r.Header.Get("Cookie")+" auth="+r.Header.Get("Authorization")+" body="+string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer fake.Close()

	// Not configured: nothing is forwarded.
	if rec := do(t, s, http.MethodGet, "/api/v1/finbot/users", admin, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unconfigured = %d", rec.Code)
	}

	s.cfg.FinbotInternalURL = fake.URL
	s.cfg.FinbotAdminKey = "k"

	// Admin session required.
	if rec := do(t, s, http.MethodGet, "/api/v1/finbot/users", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no session = %d, want 401", rec.Code)
	}

	for _, c := range []struct {
		method, path string
		body         any
		want         int
	}{
		{"GET", "/api/v1/finbot/users", nil, 200},
		{"GET", "/api/v1/finbot/users/3", nil, 200},
		{"GET", "/api/v1/finbot/users/3/finance/summary?from=2026-10-01&to=2026-10-29", nil, 200},
		{"POST", "/api/v1/finbot/users/3/status", map[string]string{"status": "active"}, 200},
		{"GET", "/api/v1/finbot/users/3/finance/../../x", nil, 404},
		{"GET", "/api/v1/finbot/users/abc", nil, 400},
		{"GET", "/api/v1/finbot/users/3/finance/secret", nil, 404},
	} {
		if rec := do(t, s, c.method, c.path, admin, c.body); rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}

	got := strings.Join(seen, "\n")
	for _, want := range []string{
		"GET /internal/admin/users key=k cookie= auth= body=",
		"GET /internal/admin/users/3 key=k",
		"GET /internal/admin/users/3/finance/summary?from=2026-10-01&to=2026-10-29 key=k",
		`POST /internal/admin/users/3/status key=k cookie= auth= body={"status":"active"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("finbot did not see %q; saw:\n%s", want, got)
		}
	}
	if len(seen) != 4 {
		t.Errorf("forwarded %d requests, want 4:\n%s", len(seen), got)
	}
}

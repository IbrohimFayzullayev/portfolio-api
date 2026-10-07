package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The contact form and the browser error intake are the two endpoints where a
// stranger chooses what ends up in the database and, eventually, on a phone.
// These tests are about that boundary: what gets stored, what gets announced,
// and what is turned away.

func sampleContact(overrides map[string]any) map[string]any {
	body := map[string]any{
		"name":    "Dilnoza Rahimova",
		"email":   "Dilnoza@Example.com",
		"message": "Salom! Loyihangiz bo'yicha gaplashmoqchi edim.",
		"locale":  "uz",
		"website": "",
	}
	for k, v := range overrides {
		body[k] = v
	}
	return body
}

func cleanContact(t *testing.T, s *Server) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(),
		`TRUNCATE contact_messages, notifications, error_groups`); err != nil {
		t.Fatalf("clean up: %v", err)
	}
}

// waitForCount polls, because notifications and error groups are written by a
// goroutine the handler deliberately does not wait for.
func waitForCount(t *testing.T, s *Server, query string, want int) int {
	t.Helper()
	var got int
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := s.pool.QueryRow(context.Background(), query).Scan(&got); err != nil {
			t.Fatalf("count: %v", err)
		}
		if got == want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestIntegration_ContactStoresAndNotifies(t *testing.T) {
	s := setupServer(t)
	cleanContact(t, s)

	rec := do(t, s, http.MethodPost, "/api/v1/public/contact", "", sampleContact(nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var name, email, locale string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT name, email, locale FROM contact_messages`).Scan(&name, &email, &locale); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if name != "Dilnoza Rahimova" || email != "dilnoza@example.com" || locale != "uz" {
		t.Errorf("stored (%q, %q, %q)", name, email, locale)
	}

	if n := waitForCount(t, s,
		`SELECT count(*) FROM notifications WHERE kind = 'contact.created'`, 1); n != 1 {
		t.Errorf("contact.created notifications = %d, want 1", n)
	}
}

func TestIntegration_ContactValidation(t *testing.T) {
	s := setupServer(t)
	cleanContact(t, s)

	cases := map[string]map[string]any{
		"no name":       {"name": "  "},
		"bad email":     {"email": "not-an-email"},
		"short message": {"message": "hi"},
		"long message":  {"message": strings.Repeat("a", contactMaxMessage+1)},
	}
	for name, override := range cases {
		rec := do(t, s, http.MethodPost, "/api/v1/public/contact", "", sampleContact(override))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}

	if n := waitForCount(t, s, `SELECT count(*) FROM contact_messages`, 0); n != 0 {
		t.Errorf("stored %d invalid messages", n)
	}
}

// A filled honeypot is answered like a success and stored nowhere.
func TestIntegration_ContactHoneypot(t *testing.T) {
	s := setupServer(t)
	cleanContact(t, s)

	rec := do(t, s, http.MethodPost, "/api/v1/public/contact", "",
		sampleContact(map[string]any{"website": "https://spam.example"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (indistinguishable from success)", rec.Code)
	}
	if n := waitForCount(t, s, `SELECT count(*) FROM contact_messages`, 0); n != 0 {
		t.Errorf("honeypot submission was stored")
	}
}

func TestIntegration_ContactRateLimit(t *testing.T) {
	s := setupServer(t)
	cleanContact(t, s)

	for i := 0; i < 5; i++ {
		if rec := do(t, s, http.MethodPost, "/api/v1/public/contact", "", sampleContact(nil)); rec.Code != http.StatusCreated {
			t.Fatalf("submission %d: status = %d", i+1, rec.Code)
		}
	}
	if rec := do(t, s, http.MethodPost, "/api/v1/public/contact", "", sampleContact(nil)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("sixth submission: status = %d, want 429", rec.Code)
	}
}

func TestIntegration_ContactAdminEndpoints(t *testing.T) {
	s := setupServer(t)
	cleanContact(t, s)
	token := registerAdmin(t, s)

	do(t, s, http.MethodPost, "/api/v1/public/contact", "", sampleContact(nil))

	if rec := do(t, s, http.MethodGet, "/api/v1/messages", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("list without auth: status = %d, want 401", rec.Code)
	}

	rec := do(t, s, http.MethodGet, "/api/v1/messages", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list contactMessageListResponse
	decode(t, rec, &list)
	if list.Total != 1 || list.Unread != 1 || len(list.Items) != 1 {
		t.Fatalf("list = total %d, unread %d, items %d", list.Total, list.Unread, len(list.Items))
	}
	id := list.Items[0].ID.String()

	if rec := do(t, s, http.MethodPatch, "/api/v1/messages/"+id+"/read", token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("mark read: status = %d", rec.Code)
	}
	rec = do(t, s, http.MethodGet, "/api/v1/messages", token, nil)
	decode(t, rec, &list)
	if list.Unread != 0 || list.Items[0].ReadAt == nil {
		t.Errorf("after mark read: unread = %d, read_at = %v", list.Unread, list.Items[0].ReadAt)
	}

	if rec := do(t, s, http.MethodDelete, "/api/v1/messages/"+id, token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d", rec.Code)
	}
	if rec := do(t, s, http.MethodPatch, "/api/v1/messages/"+id+"/read", token, nil); rec.Code != http.StatusNotFound {
		t.Errorf("mark read after delete: status = %d, want 404", rec.Code)
	}
}

/* ----------------------------- browser errors ----------------------------- */

func TestIntegration_ClientErrorsAreGrouped(t *testing.T) {
	s := setupServer(t)
	cleanContact(t, s)

	// Same failure on two pages, with a different number in the text: one
	// group, not two.
	for _, body := range []map[string]any{
		{"source": "web", "message": "Cannot read properties of undefined (item 12)", "path": "/uz/blog"},
		{"source": "web", "message": "Cannot read properties of undefined (item 98)", "path": "/en/projects"},
	} {
		if rec := do(t, s, http.MethodPost, "/api/v1/public/client-errors", "", body); rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}

	if n := waitForCount(t, s,
		`SELECT COALESCE(sum(count), 0) FROM error_groups WHERE service = 'web'`, 2); n != 2 {
		t.Fatalf("recorded occurrences = %d, want 2", n)
	}
	if n := waitForCount(t, s, `SELECT count(*) FROM error_groups WHERE service = 'web'`, 1); n != 1 {
		t.Errorf("groups = %d, want 1", n)
	}
	// Two occurrences inside one window: one message.
	if n := waitForCount(t, s,
		`SELECT count(*) FROM notifications WHERE kind = 'error.web'`, 1); n != 1 {
		t.Errorf("error.web notifications = %d, want 1", n)
	}
}

func TestIntegration_ClientErrorsAlertBudget(t *testing.T) {
	s := setupServer(t)
	cleanContact(t, s)

	// Ten DISTINCT errors: all recorded, but only the budgeted few announced.
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet"}
	for _, w := range words {
		do(t, s, http.MethodPost, "/api/v1/public/client-errors", "",
			map[string]any{"message": "boom " + w, "path": "/en"})
	}

	if n := waitForCount(t, s, `SELECT count(*) FROM error_groups WHERE service = 'web'`, len(words)); n != len(words) {
		t.Fatalf("groups = %d, want %d", n, len(words))
	}
	// Let any straggling goroutine finish before counting what was announced.
	time.Sleep(200 * time.Millisecond)
	if n := waitForCount(t, s, `SELECT count(*) FROM notifications WHERE kind = 'error.web'`, 6); n != 6 {
		t.Errorf("error.web notifications = %d, want 6 (the hourly budget)", n)
	}
}

func TestIntegration_ClientErrorRequiresContent(t *testing.T) {
	s := setupServer(t)
	cleanContact(t, s)

	rec := do(t, s, http.MethodPost, "/api/v1/public/client-errors", "",
		map[string]any{"message": "  ", "path": "/en"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

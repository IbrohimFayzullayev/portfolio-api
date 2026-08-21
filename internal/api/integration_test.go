package api

// Integration tests exercise the real HTTP router against a real PostgreSQL
// database. They are skipped automatically unless TEST_DATABASE_URL points at a
// throwaway database, so `go test ./...` stays fast and DB-free by default.
//
//	Run them with:
//	  make test-integration
//	which starts the Docker DB and sets TEST_DATABASE_URL for you.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/config"
	"github.com/ibrohimcoder/portfolio-api/internal/database"
)

// setupServer connects to the test database, applies migrations, wipes the
// content tables and returns a ready-to-use Server. It skips the test when no
// test database is configured.
func setupServer(t *testing.T) *Server {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test (run `make test-integration`)")
	}

	ctx := context.Background()
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect test DB: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := database.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate test DB: %v", err)
	}
	// Start every test from a clean slate.
	if _, err := pool.Exec(ctx, `TRUNCATE users, posts, projects RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	cfg := &config.Config{
		Env:         "test",
		JWTSecret:   "integration-test-secret",
		JWTTTL:      time.Hour,
		CORSOrigins: []string{"*"},
	}
	return NewServer(cfg, pool)
}

// do sends a request through the router and returns the recorded response.
func do(t *testing.T, s *Server, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	return rec
}

// registerAdmin creates the first admin account and returns its bearer token.
func registerAdmin(t *testing.T, s *Server) string {
	t.Helper()
	rec := do(t, s, http.MethodPost, "/api/v1/auth/register", "", map[string]any{
		"email":    "admin@example.com",
		"password": "password123",
		"name":     "Ibrohim",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out authResponse
	decode(t, rec, &out)
	if out.Token == "" {
		t.Fatal("register: empty token")
	}
	return out.Token
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
}

func samplePost(overrides map[string]any) map[string]any {
	post := map[string]any{
		"locale":      "en",
		"slug":        "hello-world",
		"title":       "Hello World",
		"description": "first post",
		"body":        "# Hello\n\nbody text",
		"tags":        []string{"go", "next"},
		"draft":       false,
		"date":        "2026-01-02",
	}
	for k, v := range overrides {
		post[k] = v
	}
	return post
}

func TestIntegration_AuthFlow(t *testing.T) {
	s := setupServer(t)
	token := registerAdmin(t, s)

	t.Run("me with token", func(t *testing.T) {
		rec := do(t, s, http.MethodGet, "/api/v1/auth/me", token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("me: status = %d", rec.Code)
		}
		var u userResponse
		decode(t, rec, &u)
		if u.Email != "admin@example.com" {
			t.Errorf("email = %q, want admin@example.com", u.Email)
		}
	})

	t.Run("me without token is 401", func(t *testing.T) {
		rec := do(t, s, http.MethodGet, "/api/v1/auth/me", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("me with bad token is 401", func(t *testing.T) {
		rec := do(t, s, http.MethodGet, "/api/v1/auth/me", "garbage.token.here", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("second registration is closed", func(t *testing.T) {
		rec := do(t, s, http.MethodPost, "/api/v1/auth/register", "", map[string]any{
			"email": "other@example.com", "password": "password123", "name": "Other",
		})
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("login wrong password is 401", func(t *testing.T) {
		rec := do(t, s, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
			"email": "admin@example.com", "password": "wrongpass",
		})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("login correct password", func(t *testing.T) {
		rec := do(t, s, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
			"email": "admin@example.com", "password": "password123",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var out authResponse
		decode(t, rec, &out)
		if out.Token == "" {
			t.Error("login returned empty token")
		}
	})
}

func TestIntegration_PostsCRUD(t *testing.T) {
	s := setupServer(t)
	token := registerAdmin(t, s)

	// Create
	rec := do(t, s, http.MethodPost, "/api/v1/posts", token, samplePost(nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created postResponse
	decode(t, rec, &created)
	if created.ID.String() == "" || created.Slug != "hello-world" {
		t.Fatalf("create: unexpected response %+v", created)
	}
	if created.PublishedAt == nil {
		t.Error("a non-draft post should have published_at set")
	}
	id := created.ID.String()

	// Duplicate (same locale+slug) -> 409
	if rec := do(t, s, http.MethodPost, "/api/v1/posts", token, samplePost(nil)); rec.Code != http.StatusConflict {
		t.Errorf("duplicate create: status = %d, want 409", rec.Code)
	}

	// Invalid payload -> 400
	if rec := do(t, s, http.MethodPost, "/api/v1/posts", token, samplePost(map[string]any{"locale": "ru"})); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid create: status = %d, want 400", rec.Code)
	}

	// Get by id
	if rec := do(t, s, http.MethodGet, "/api/v1/posts/"+id, token, nil); rec.Code != http.StatusOK {
		t.Errorf("get: status = %d, want 200", rec.Code)
	}

	// List
	rec = do(t, s, http.MethodGet, "/api/v1/posts", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status = %d", rec.Code)
	}
	var list []postResponse
	decode(t, rec, &list)
	if len(list) != 1 {
		t.Errorf("list length = %d, want 1", len(list))
	}

	// Update
	rec = do(t, s, http.MethodPut, "/api/v1/posts/"+id, token, samplePost(map[string]any{"title": "Updated Title"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated postResponse
	decode(t, rec, &updated)
	if updated.Title != "Updated Title" {
		t.Errorf("title = %q, want Updated Title", updated.Title)
	}

	// Unpublish via publish endpoint
	rec = do(t, s, http.MethodPatch, "/api/v1/posts/"+id+"/publish", token, map[string]any{"draft": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("publish: status = %d", rec.Code)
	}
	var toggled postResponse
	decode(t, rec, &toggled)
	if !toggled.Draft || toggled.PublishedAt != nil {
		t.Errorf("after unpublish: draft=%v published_at=%v, want draft=true published_at=nil", toggled.Draft, toggled.PublishedAt)
	}

	// Delete -> 204, then get -> 404
	if rec := do(t, s, http.MethodDelete, "/api/v1/posts/"+id, token, nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete: status = %d, want 204", rec.Code)
	}
	if rec := do(t, s, http.MethodGet, "/api/v1/posts/"+id, token, nil); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete: status = %d, want 404", rec.Code)
	}

	// Writes require auth
	if rec := do(t, s, http.MethodPost, "/api/v1/posts", "", samplePost(nil)); rec.Code != http.StatusUnauthorized {
		t.Errorf("create without token: status = %d, want 401", rec.Code)
	}
}

func TestIntegration_PublicEndpoints(t *testing.T) {
	s := setupServer(t)
	token := registerAdmin(t, s)

	// One published, one draft.
	if rec := do(t, s, http.MethodPost, "/api/v1/posts", token, samplePost(map[string]any{"slug": "published", "draft": false})); rec.Code != http.StatusCreated {
		t.Fatalf("seed published: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, s, http.MethodPost, "/api/v1/posts", token, samplePost(map[string]any{"slug": "hidden", "draft": true})); rec.Code != http.StatusCreated {
		t.Fatalf("seed draft: %d %s", rec.Code, rec.Body.String())
	}

	// Public list returns only the published post (no auth header).
	rec := do(t, s, http.MethodGet, "/api/v1/public/posts?locale=en", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("public list: status = %d", rec.Code)
	}
	var list []postResponse
	decode(t, rec, &list)
	if len(list) != 1 || list[0].Slug != "published" {
		t.Errorf("public list = %+v, want only the published post", list)
	}

	// Published slug is reachable.
	if rec := do(t, s, http.MethodGet, "/api/v1/public/posts/en/published", "", nil); rec.Code != http.StatusOK {
		t.Errorf("public get published: status = %d, want 200", rec.Code)
	}
	// Draft slug is hidden from the public API.
	if rec := do(t, s, http.MethodGet, "/api/v1/public/posts/en/hidden", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("public get draft: status = %d, want 404", rec.Code)
	}
}

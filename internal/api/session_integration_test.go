package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The dashboard authenticates with an HttpOnly cookie; everything else keeps
// using the Authorization header. These tests pin down the three things that
// make the cookie safe to rely on: it is set the right way, it works, and it
// is not enough on its own to change anything from the wrong origin.

func doWith(t *testing.T, s *Server, method, path string, body any, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	t.Helper()

	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}

	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	return rec
}

func sessionCookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in response", sessionCookieName)
	return nil
}

func TestIntegration_SessionCookie(t *testing.T) {
	s := setupServer(t)
	s.cfg.CORSOrigins = []string{"https://admin.example.com"}
	registerAdmin(t, s)

	rec := do(t, s, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"email": "admin@example.com", "password": "password123",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	cookie := sessionCookieFrom(t, rec)
	if !cookie.HttpOnly {
		t.Error("session cookie is readable by JavaScript (HttpOnly not set)")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Secure {
		t.Error("Secure set outside production; the cookie would not work on localhost")
	}

	// Reading with the cookie alone.
	if rec := doWith(t, s, http.MethodGet, "/api/v1/auth/me", nil, cookie, ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /auth/me with cookie: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Writing with the cookie needs an allowed Origin.
	post := samplePost(nil)
	if rec := doWith(t, s, http.MethodPost, "/api/v1/posts", post, cookie, ""); rec.Code != http.StatusForbidden {
		t.Errorf("POST with cookie and no Origin: status = %d, want 403", rec.Code)
	}
	if rec := doWith(t, s, http.MethodPost, "/api/v1/posts", post, cookie, "https://evil.example.com"); rec.Code != http.StatusForbidden {
		t.Errorf("POST with cookie from foreign Origin: status = %d, want 403", rec.Code)
	}
	if rec := doWith(t, s, http.MethodPost, "/api/v1/posts", post, cookie, "https://admin.example.com"); rec.Code != http.StatusCreated {
		t.Errorf("POST with cookie from the dashboard: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestIntegration_SessionCookieSecureInProduction(t *testing.T) {
	s := setupServer(t)
	s.cfg.Env = "production"
	rec := do(t, s, http.MethodPost, "/api/v1/auth/register", "", map[string]any{
		"email": "admin@example.com", "password": "password123", "name": "Ibrohim",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: status = %d", rec.Code)
	}
	if !sessionCookieFrom(t, rec).Secure {
		t.Error("session cookie is not Secure in production")
	}
}

func TestIntegration_Logout(t *testing.T) {
	s := setupServer(t)

	// Works without a session: this is the button people press when the
	// session has already expired.
	rec := do(t, s, http.MethodPost, "/api/v1/auth/logout", "", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: status = %d", rec.Code)
	}
	if c := sessionCookieFrom(t, rec); c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("logout did not clear the cookie: %+v", c)
	}
}

// A bearer token still works everywhere and is not subject to the Origin
// check — nothing attaches it automatically, so there is nothing to forge.
func TestIntegration_BearerUnaffectedByOriginCheck(t *testing.T) {
	s := setupServer(t)
	s.cfg.CORSOrigins = []string{"https://admin.example.com"}
	token := registerAdmin(t, s)

	if rec := do(t, s, http.MethodPost, "/api/v1/posts", token, samplePost(nil)); rec.Code != http.StatusCreated {
		t.Errorf("POST with bearer token: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestIntegration_LoginRateLimit(t *testing.T) {
	s := setupServer(t)
	registerAdmin(t, s) // one attempt against the shared limit

	wrong := map[string]any{"email": "admin@example.com", "password": "wrong-password"}
	for i := 0; i < 9; i++ {
		if rec := do(t, s, http.MethodPost, "/api/v1/auth/login", "", wrong); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rec.Code)
		}
	}

	// The eleventh request is refused before the password is even looked at —
	// including with the RIGHT password, which is the point.
	right := map[string]any{"email": "admin@example.com", "password": "password123"}
	if rec := do(t, s, http.MethodPost, "/api/v1/auth/login", "", right); rec.Code != http.StatusTooManyRequests {
		t.Errorf("after 10 attempts: status = %d, want 429", rec.Code)
	}
}

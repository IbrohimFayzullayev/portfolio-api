package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Umami's stats payload changed shape during 2.x — plain numbers early on,
// {value, prev} objects from 2.12. Both have to decode, because the alternative
// is a bot that breaks on a docker compose pull nobody connected to Telegram.

func TestStatsDecodesBothShapes(t *testing.T) {
	t.Run("plain numbers", func(t *testing.T) {
		var s umamiStats
		if err := json.Unmarshal([]byte(`{"pageviews":412,"visitors":88}`), &s); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if s.Pageviews.Value != 412 || s.Visitors.Value != 88 {
			t.Errorf("got %+v", s)
		}
	})

	t.Run("value objects", func(t *testing.T) {
		var s umamiStats
		raw := `{"pageviews":{"value":412,"prev":390},"visitors":{"value":88,"prev":70}}`
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if s.Pageviews.Value != 412 || s.Pageviews.Prev != 390 {
			t.Errorf("got %+v", s.Pageviews)
		}
	})

	t.Run("garbage is an error, not a zero", func(t *testing.T) {
		var s umamiStats
		if err := json.Unmarshal([]byte(`{"pageviews":"many"}`), &s); err == nil {
			t.Error("a string decoded as a count")
		}
	})
}

func TestUmamiEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"nothing configured", Config{}, false},
		{"url without a website", Config{UmamiURL: "http://umami:3000"}, false},
		{"website without credentials", Config{UmamiURL: "http://umami:3000", UmamiWebsiteID: "abc"}, false},
		{"api key", Config{UmamiURL: "http://umami:3000", UmamiWebsiteID: "abc", UmamiAPIKey: "k"}, true},
		{"login", Config{UmamiURL: "http://umami:3000", UmamiWebsiteID: "abc", UmamiUsername: "u", UmamiPassword: "p"}, true},
		{"half a login", Config{UmamiURL: "http://umami:3000", UmamiWebsiteID: "abc", UmamiUsername: "u"}, false},
	}

	for _, tc := range tests {
		if got := newUmamiClient(tc.cfg).enabled(); got != tc.want {
			t.Errorf("%s: enabled = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUmamiLoginIsCached(t *testing.T) {
	var logins int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/login" {
			logins++
			_, _ = w.Write([]byte(`{"token":"t0ken"}`))
			return
		}
		_, _ = w.Write([]byte(`{"pageviews":{"value":10},"visitors":{"value":3}}`))
	}))
	t.Cleanup(srv.Close)

	c := newUmamiClient(Config{
		UmamiURL: srv.URL, UmamiWebsiteID: "site", UmamiUsername: "u", UmamiPassword: "p",
	})

	now := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := c.stats(context.Background(), now.Add(-time.Hour), now); err != nil {
			t.Fatalf("stats: %v", err)
		}
	}
	if logins != 1 {
		t.Errorf("logged in %d times, want 1", logins)
	}
}

// A 401 mid-session must drop the cached token, or every later call fails too.
func TestUmamiForgetsTokenOnUnauthorised(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/login" {
			_, _ = w.Write([]byte(`{"token":"t0ken"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := newUmamiClient(Config{
		UmamiURL: srv.URL, UmamiWebsiteID: "site", UmamiUsername: "u", UmamiPassword: "p",
	})

	now := time.Now()
	if _, err := c.stats(context.Background(), now.Add(-time.Hour), now); err == nil {
		t.Fatal("expected an error")
	}
	if c.token != "" {
		t.Error("token survived a 401")
	}
}

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func seenAddress(t *testing.T, headers map[string]string) string {
	t.Helper()
	var seen string
	h := realIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = clientIP(r)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.18.0.5:41000" // the reverse proxy, on the Docker network
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return seen
}

func TestRealIP(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"no header keeps the peer", nil, "172.18.0.5"},
		{"forwarded address is used", map[string]string{"X-Forwarded-For": "203.0.113.7"}, "203.0.113.7"},
		{"first entry of a chain", map[string]string{"X-Forwarded-For": "203.0.113.7, 10.0.0.1"}, "203.0.113.7"},
		{"ipv6", map[string]string{"X-Forwarded-For": "2001:db8::1"}, "2001:db8::1"},
		{"garbage is ignored", map[string]string{"X-Forwarded-For": "not-an-ip"}, "172.18.0.5"},
		// The whole reason this middleware exists: these two headers come
		// straight from the client and must not move the rate-limit key.
		{"X-Real-IP is not trusted", map[string]string{"X-Real-IP": "198.51.100.9"}, "172.18.0.5"},
		{"True-Client-IP is not trusted", map[string]string{"True-Client-IP": "198.51.100.9"}, "172.18.0.5"},
		{
			"spoofed headers do not override the forwarded one",
			map[string]string{"X-Forwarded-For": "203.0.113.7", "X-Real-IP": "198.51.100.9"},
			"203.0.113.7",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := seenAddress(t, tc.headers); got != tc.want {
				t.Errorf("address = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRateLimiterWindow(t *testing.T) {
	rl := newRateLimiter(2, time.Hour)

	if !rl.allow("a") || !rl.allow("a") {
		t.Fatal("first two requests must pass")
	}
	if rl.allow("a") {
		t.Error("third request within the window must be refused")
	}
	if !rl.allow("b") {
		t.Error("a different key must have its own allowance")
	}
}

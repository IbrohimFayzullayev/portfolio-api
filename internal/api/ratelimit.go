package api

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type rateLimiter struct {
	mu      sync.Mutex
	hits    map[string]*rateWindow
	limit   int
	window  time.Duration
	lastGC  time.Time
}

type rateWindow struct {
	count int
	start time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		hits:   make(map[string]*rateWindow),
		limit:  limit,
		window: window,
		lastGC: time.Now(),
	}
}

func (rl *rateLimiter) allow(key string) bool {
	now := time.Now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	if now.Sub(rl.lastGC) > rl.window {
		for k, w := range rl.hits {
			if now.Sub(w.start) > rl.window {
				delete(rl.hits, k)
			}
		}
		rl.lastGC = now
	}

	w, ok := rl.hits[key]
	if !ok || now.Sub(w.start) > rl.window {
		rl.hits[key] = &rateWindow{count: 1, start: now}
		return true
	}
	if w.count >= rl.limit {
		return false
	}
	w.count++
	return true
}

func (rl *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "too many requests, please slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

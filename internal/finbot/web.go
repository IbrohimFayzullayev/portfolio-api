package finbot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/auth"
	"github.com/ibrohimcoder/portfolio-api/internal/tgauth"
)

// The web API behind the Telegram Mini App.
//
// The page is opened from the bot's buttons; Telegram hands it signed init
// data, and POST /api/auth trades that for a short-lived Bearer token. From
// then on every request is that user's and only that user's: the token
// names a Telegram account, the account must still be active, and every
// query filters on its user id.
//
// The routes under /api/finance answer in exactly the JSON the portfolio
// API's /finance routes do, so the dashboard's money components draw both
// without knowing which one they are talking to. Unlike the portfolio's
// read-only Mini App, writes are allowed here: the data is the signed-in
// person's own.
//
// No cookies anywhere: the token lives in the page's memory and travels as a
// header, so no other site can make a request that carries it.

// How long init data stays usable after Telegram signed it.
const initDataMaxAge = 24 * time.Hour

// How long a token lives; reopening the Mini App issues a fresh one.
const tokenTTL = 4 * time.Hour

type ctxKey string

const userKey ctxKey = "finbotUser"

// Handler is the whole web API.
func (b *Bot) Handler() http.Handler {
	// The signing key is derived from the bot token: no new secret to keep,
	// and a different key from anything the portfolio API signs with.
	tokens := auth.NewViewerTokens(b.cfg.Token, tokenTTL)
	limiter := newAuthLimiter(20, time.Minute)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := b.pool.Ping(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/auth", func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "too many sign-in attempts")
			return
		}
		b.handleAuth(w, r, tokens)
	})

	authed := func(h http.HandlerFunc) http.Handler { return b.requireUser(tokens, h) }
	mux.Handle("GET /api/finance/meta", authed(b.handleMeta))
	mux.Handle("GET /api/finance/summary", authed(b.handleSummary))
	mux.Handle("GET /api/finance/entries", authed(b.handleListEntries))
	mux.Handle("POST /api/finance/entries", authed(b.handleCreateEntry))
	mux.Handle("PUT /api/finance/entries/{id}", authed(b.handleUpdateEntry))
	mux.Handle("DELETE /api/finance/entries/{id}", authed(b.handleDeleteEntry))
	mux.Handle("GET /api/finance/debts", authed(b.handleDebts))
	mux.Handle("GET /api/finance/checkpoints", authed(b.handleListCheckpoints))
	mux.Handle("POST /api/finance/checkpoints", authed(b.handleCreateCheckpoint))
	mux.Handle("DELETE /api/finance/checkpoints/{id}", authed(b.handleDeleteCheckpoint))

	b.mountAdmin(mux)

	return b.withCORS(withRecover(mux))
}

/* ---------------------------------- auth ---------------------------------- */

type authResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      struct {
		ID        int64  `json:"id"`
		FirstName string `json:"first_name"`
	} `json:"user"`
}

func (b *Bot) handleAuth(w http.ResponseWriter, r *http.Request, tokens *auth.ViewerTokens) {
	var in struct {
		InitData string `json:"init_data"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	data, err := tgauth.Verify(strings.TrimSpace(in.InitData), b.cfg.Token, initDataMaxAge, b.now())
	if err != nil {
		if errors.Is(err, tgauth.ErrExpired) {
			writeError(w, http.StatusUnauthorized, "telegram session expired; reopen the app from the bot")
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid telegram init data")
		return
	}
	// A valid signature proves who this is, not that they are let in.
	u, err := b.userByTelegram(r.Context(), data.User.ID)
	if err != nil {
		serverError(w, "auth lookup", err)
		return
	}
	if u == nil || u.Status != statusActive {
		writeError(w, http.StatusForbidden, "this telegram account has no access")
		return
	}
	token, expiresAt, err := tokens.Generate(data.User.ID)
	if err != nil {
		serverError(w, "issue token", err)
		return
	}
	var out authResponse
	out.Token, out.ExpiresAt = token, expiresAt
	out.User.ID, out.User.FirstName = data.User.ID, firstNonEmpty(data.User.FirstName, u.FirstName)
	writeJSON(w, http.StatusOK, out)
}

// requireUser checks the token AND the account on every request, so
// blocking someone takes effect at once rather than when the token expires.
func (b *Bot) requireUser(tokens *auth.ViewerTokens, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing token")
			return
		}
		claims, err := tokens.Parse(strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		u, err := b.userByTelegram(r.Context(), claims.TelegramID)
		if err != nil {
			serverError(w, "user lookup", err)
			return
		}
		if u == nil || u.Status != statusActive {
			writeError(w, http.StatusForbidden, "this telegram account has no access")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

func currentUser(r *http.Request) *user {
	u, _ := r.Context().Value(userKey).(*user)
	return u
}

/* --------------------------------- helpers -------------------------------- */

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func serverError(w http.ResponseWriter, what string, err error) {
	log.Printf("finbot web: %s: %v", what, err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body is empty")
		}
		return err
	}
	return nil
}

func withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("finbot web: panic on %s %s: %v", r.Method, r.URL.Path, v)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withCORS allows the configured origins only (local development: the page
// on :3001, the API on :8090). In production the page and the API share one
// origin behind Caddy and no CORS header is ever sent.
func (b *Bot) withCORS(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range b.cfg.CORSOrigins {
		if o = strings.TrimSpace(o); o != "" {
			allowed[o] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && allowed[origin] {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP trusts X-Forwarded-For only for its last hop: Caddy appends the
// address it saw, so that entry is the one a client cannot forge.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i != -1 {
		host = host[:i]
	}
	return host
}

// authLimiter is a fixed-window count per IP for the one unauthenticated
// route. Init data is an HMAC, so this is about load, not guessing.
type authLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	seen   map[string]*window
}

type window struct {
	start time.Time
	n     int
}

func newAuthLimiter(max int, per time.Duration) *authLimiter {
	return &authLimiter{max: max, window: per, seen: map[string]*window{}}
}

func (l *authLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.seen) > 10_000 { // bounded memory, whatever arrives
		l.seen = map[string]*window{}
	}
	w, ok := l.seen[ip]
	if !ok || now.Sub(w.start) > l.window {
		l.seen[ip] = &window{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= l.max
}

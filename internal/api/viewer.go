package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/tgauth"
)

// The Telegram Mini App: read-only, signed in by Telegram itself.
//
// Two kinds of credential reach this API and they are kept apart on purpose:
//
//   - admin: the dashboard's session (HttpOnly cookie, or a Bearer token from
//     /auth/login). Accepted everywhere behind authMiddleware.
//   - viewer: a short-lived Bearer token from POST /tg/auth, issued for
//     Telegram init data from an allow-listed account. Signed with a
//     different key (auth.ViewerTokens), so authMiddleware rejects it at
//     the signature. Accepted ONLY by adminOrViewer, which is mounted only
//     on GET routes.
//
// A viewer token is never stored by the page beyond memory and is never a
// cookie, so it cannot ride along with a request the page did not make.

// How long init data stays usable after Telegram signed it. The page signs in
// once on open; this only bounds a captured string.
const initDataMaxAge = 24 * time.Hour

// How long a viewer token lives. Long enough for a session of looking at
// numbers; reopening the Mini App issues a fresh one.
const viewerTokenTTL = 4 * time.Hour

const viewerIDKey contextKey = "telegramViewerID"

type telegramAuthInput struct {
	InitData string `json:"init_data"`
}

type telegramAuthResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      struct {
		ID        int64  `json:"id"`
		FirstName string `json:"first_name"`
	} `json:"user"`
}

func (s *Server) handleTelegramAuth(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.TelegramWebAppEnabled() {
		writeError(w, http.StatusNotFound, "telegram web app is not configured")
		return
	}

	var in telegramAuthInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	data, err := tgauth.Verify(strings.TrimSpace(in.InitData), s.cfg.TelegramBotToken,
		initDataMaxAge, time.Now())
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, tgauth.ErrExpired) {
			// Distinct message: the fix is "reopen from Telegram", not
			// "something is wrong".
			writeError(w, status, "telegram session expired; reopen the app from the bot")
			return
		}
		writeError(w, status, "invalid telegram init data")
		return
	}

	// A valid signature only proves Telegram vouches for this account — not
	// that this account may see anything. Same allow-list as the bot.
	if !s.cfg.TelegramAllowed(data.User.ID) {
		log.Printf("api: telegram web app sign-in refused for user %d (@%s)",
			data.User.ID, data.User.Username)
		writeError(w, http.StatusForbidden, "this telegram account has no access")
		return
	}

	token, expiresAt, err := s.viewers.Generate(data.User.ID)
	if err != nil {
		s.serverError(w, r, "issue viewer token", err)
		return
	}

	var out telegramAuthResponse
	out.Token, out.ExpiresAt = token, expiresAt
	out.User.ID, out.User.FirstName = data.User.ID, data.User.FirstName
	writeJSON(w, http.StatusOK, out)
}

// adminOrViewer lets a viewer token through to read-only routes and hands
// everything else to authMiddleware unchanged. The allow-list is checked on
// every request, so removing an id from the environment revokes access at
// the next restart rather than when the token expires.
func (s *Server) adminOrViewer(next http.Handler) http.Handler {
	admin := s.authMiddleware(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isSafeMethod(r.Method) {
			// Never: viewer tokens are for reading. A write route mounted
			// here by mistake still demands an admin.
			admin.ServeHTTP(w, r)
			return
		}
		if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
			if claims, err := s.viewers.Parse(strings.TrimPrefix(header, "Bearer ")); err == nil {
				if !s.cfg.TelegramAllowed(claims.TelegramID) {
					writeError(w, http.StatusForbidden, "this telegram account has no access")
					return
				}
				ctx := context.WithValue(r.Context(), viewerIDKey, claims.TelegramID)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
		admin.ServeHTTP(w, r)
	})
}

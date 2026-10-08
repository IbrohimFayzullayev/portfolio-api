package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type contextKey string

const userIDKey contextKey = "userID"

// authMiddleware accepts either a Bearer token or the session cookie, and
// stores the user ID in context.
//
// The header wins when both are present: it is explicit, and it is what every
// non-browser client sends.
//
// A cookie is attached by the browser whether or not the page that made the
// request is ours, so a cookie-authenticated request that CHANGES something
// must also prove where it came from. SameSite already stops other sites; the
// Origin check covers what SameSite does not — a page on a sibling subdomain,
// which the browser considers the same site.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		fromCookie := false

		if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
			token = strings.TrimPrefix(header, "Bearer ")
		} else if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
			token = c.Value
			fromCookie = true
		}

		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing credentials")
			return
		}

		if fromCookie && !isSafeMethod(r.Method) && !s.originAllowed(r.Header.Get("Origin")) {
			writeError(w, http.StatusForbidden, "request origin not allowed")
			return
		}

		claims, err := s.tokens.Parse(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}

		userID, err := claims.UserID()
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid token subject")
			return
		}

		ctx := context.WithValue(r.Context(), userIDKey, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func userIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDKey).(uuid.UUID)
	return id, ok
}

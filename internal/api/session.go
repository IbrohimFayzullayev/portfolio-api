package api

import (
	"net/http"
	"time"
)

// The dashboard's session cookie.
//
// The dashboard used to keep the JWT in a cookie written by its own
// JavaScript, which means readable by its own JavaScript — and by any script
// that ever runs on that page: a compromised dependency, an XSS in rendered
// Markdown. For a CMS that renders user-authored MDX, that is the realistic
// way to lose the admin account.
//
// An HttpOnly cookie set by the API is the same token in a place scripts
// cannot reach. The browser attaches it; the page never sees it.
//
// The Authorization header keeps working exactly as before. Scripts, curl and
// the tests use it, and it carries no ambient authority: a request only has a
// bearer token if something deliberately put it there.

const sessionCookieName = "pf_session"

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		// Plain HTTP only exists on localhost during development.
		Secure: s.cfg.IsProduction(),
		// Lax, not Strict: the dashboard and the API are different subdomains
		// of one site, so its requests are same-site and carry the cookie
		// either way; Lax keeps a link into the dashboard from another site
		// from landing on a logged-out page.
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.IsProduction(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// originAllowed reports whether a browser origin is one this API was told to
// trust. It reads the same list CORS does, so there is one place to configure.
func (s *Server) originAllowed(origin string) bool {
	if origin == "" {
		return false
	}
	for _, allowed := range s.cfg.CORSOrigins {
		if allowed == "*" || allowed == origin {
			return true
		}
	}
	return false
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

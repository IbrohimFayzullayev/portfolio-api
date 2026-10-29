package api

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// The finance bot's admin view, for the dashboard's "Moliya boti" pages.
//
// The finance bot keeps its own database and is the only process that reads
// it. This API does not connect to that database: it checks the dashboard's
// admin session (these routes sit behind authMiddleware) and forwards the
// request to the finance bot over the Docker network, with the shared
// FINBOT_ADMIN_KEY. Only the paths below are forwarded, and the browser's
// cookies and headers never are.

var finbotFinanceParts = map[string]bool{
	"meta": true, "summary": true, "entries": true, "debts": true, "checkpoints": true,
}

var finbotClient = &http.Client{Timeout: 20 * time.Second}

func (s *Server) handleFinbotProxy(w http.ResponseWriter, r *http.Request) {
	if s.cfg.FinbotAdminKey == "" || s.cfg.FinbotInternalURL == "" {
		writeError(w, http.StatusNotFound, "finance bot admin is not configured (FINBOT_ADMIN_KEY)")
		return
	}

	// Rebuild the path from validated parts rather than passing the
	// incoming one through.
	path := "/internal/admin/users"
	if id := chi.URLParam(r, "id"); id != "" {
		if n, err := strconv.ParseInt(id, 10, 64); err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		path += "/" + id
		switch {
		case strings.HasSuffix(r.URL.Path, "/status"):
			path += "/status"
		case chi.URLParam(r, "part") != "":
			part := chi.URLParam(r, "part")
			if !finbotFinanceParts[part] {
				writeError(w, http.StatusNotFound, "unknown report")
				return
			}
			path += "/finance/" + part
		}
	}
	target := s.cfg.FinbotInternalURL + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	var body io.Reader
	if r.Method == http.MethodPost {
		body = io.LimitReader(r.Body, 1<<16)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		s.serverError(w, r, "finbot proxy request", err)
		return
	}
	req.Header.Set("X-Finbot-Admin-Key", s.cfg.FinbotAdminKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := finbotClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "finance bot is not reachable")
		return
	}
	defer res.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(res.Body, 8<<20))
}

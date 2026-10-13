package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ibrohimcoder/portfolio-api/notes"
)

// Personal reference documents (notes/*.md), embedded in the binary. Read by
// the dashboard and the Telegram Mini App; never written through the API.

func (s *Server) handleListNotes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"notes": notes.List()})
}

func (s *Server) handleGetNote(w http.ResponseWriter, r *http.Request) {
	n, ok := notes.Get(chi.URLParam(r, "slug"))
	if !ok {
		writeError(w, http.StatusNotFound, "note not found")
		return
	}
	writeJSON(w, http.StatusOK, n)
}

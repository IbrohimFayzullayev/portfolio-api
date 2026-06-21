package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ibrohimcoder/portfolio-api/internal/db"
)

// The /public/* routes are unauthenticated and expose only published
// (draft = false) content. They are consumed by the public website.

func localeParam(r *http.Request) *string {
	if loc := r.URL.Query().Get("locale"); loc != "" {
		return &loc
	}
	return nil
}

func (s *Server) handlePublicListPosts(w http.ResponseWriter, r *http.Request) {
	posts, err := s.q.ListPublishedPosts(r.Context(), localeParam(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list posts")
		return
	}
	writeJSON(w, http.StatusOK, toPostResponses(posts))
}

func (s *Server) handlePublicGetPost(w http.ResponseWriter, r *http.Request) {
	post, err := s.q.GetPublishedPostBySlug(r.Context(), db.GetPublishedPostBySlugParams{
		Locale: chi.URLParam(r, "locale"),
		Slug:   chi.URLParam(r, "slug"),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "post not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load post")
		return
	}
	writeJSON(w, http.StatusOK, toPostResponse(post))
}

func (s *Server) handlePublicListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.q.ListPublishedProjects(r.Context(), localeParam(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list projects")
		return
	}
	writeJSON(w, http.StatusOK, toProjectResponses(projects))
}

func (s *Server) handlePublicGetProject(w http.ResponseWriter, r *http.Request) {
	project, err := s.q.GetPublishedProjectBySlug(r.Context(), db.GetPublishedProjectBySlugParams{
		Locale: chi.URLParam(r, "locale"),
		Slug:   chi.URLParam(r, "slug"),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load project")
		return
	}
	writeJSON(w, http.StatusOK, toProjectResponse(project))
}

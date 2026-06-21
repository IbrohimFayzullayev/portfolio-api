package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/ibrohimcoder/portfolio-api/internal/db"
)

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	var params db.ListProjectsParams

	if loc := r.URL.Query().Get("locale"); loc != "" {
		params.Locale = &loc
	}
	switch r.URL.Query().Get("status") {
	case "draft":
		v := true
		params.Draft = &v
	case "published":
		v := false
		params.Draft = &v
	}

	projects, err := s.q.ListProjects(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list projects")
		return
	}
	writeJSON(w, http.StatusOK, toProjectResponses(projects))
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	project, err := s.q.GetProjectByID(r.Context(), id)
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

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var in projectInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	project, err := s.q.CreateProject(r.Context(), db.CreateProjectParams{
		Locale:      in.Locale,
		Slug:        in.Slug,
		Title:       in.Title,
		Description: in.Description,
		Body:        in.Body,
		Tags:        nonNil(in.Tags),
		Stack:       nonNil(in.Stack),
		Url:         in.URL,
		Repo:        in.Repo,
		SortOrder:   in.Order,
		Featured:    in.Featured,
		Draft:       in.Draft,
		ContentDate: parseContentDate(in.Date),
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a project with this locale and slug already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create project")
		return
	}
	writeJSON(w, http.StatusCreated, toProjectResponse(project))
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	if _, err := s.q.GetProjectByID(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load project")
		return
	}

	var in projectInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	project, err := s.q.UpdateProject(r.Context(), db.UpdateProjectParams{
		ID:          id,
		Locale:      in.Locale,
		Slug:        in.Slug,
		Title:       in.Title,
		Description: in.Description,
		Body:        in.Body,
		Tags:        nonNil(in.Tags),
		Stack:       nonNil(in.Stack),
		Url:         in.URL,
		Repo:        in.Repo,
		SortOrder:   in.Order,
		Featured:    in.Featured,
		Draft:       in.Draft,
		ContentDate: parseContentDate(in.Date),
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a project with this locale and slug already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update project")
		return
	}
	writeJSON(w, http.StatusOK, toProjectResponse(project))
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.q.DeleteProject(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete project")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

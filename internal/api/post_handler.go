package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ibrohimcoder/portfolio-api/internal/db"
)

func idParam(r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(chi.URLParam(r, "id"))
}

func (s *Server) handleListPosts(w http.ResponseWriter, r *http.Request) {
	var params db.ListPostsParams

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

	posts, err := s.q.ListPosts(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list posts")
		return
	}
	writeJSON(w, http.StatusOK, toPostResponses(posts))
}

func (s *Server) handleGetPost(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	post, err := s.q.GetPostByID(r.Context(), id)
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

func (s *Server) handleCreatePost(w http.ResponseWriter, r *http.Request) {
	var in postInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	post, err := s.q.CreatePost(r.Context(), db.CreatePostParams{
		Locale:      in.Locale,
		Slug:        in.Slug,
		Title:       in.Title,
		Description: in.Description,
		Body:        in.Body,
		Tags:        nonNil(in.Tags),
		Cover:       in.Cover,
		Featured:    in.Featured,
		Draft:       in.Draft,
		ContentDate: parseContentDate(in.Date),
		PublishedAt: publishedAtFor(in.Draft, nil),
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a post with this locale and slug already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create post")
		return
	}
	writeJSON(w, http.StatusCreated, toPostResponse(post))
}

func (s *Server) handleUpdatePost(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	existing, err := s.q.GetPostByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "post not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load post")
		return
	}

	var in postInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	post, err := s.q.UpdatePost(r.Context(), db.UpdatePostParams{
		ID:          id,
		Locale:      in.Locale,
		Slug:        in.Slug,
		Title:       in.Title,
		Description: in.Description,
		Body:        in.Body,
		Tags:        nonNil(in.Tags),
		Cover:       in.Cover,
		Featured:    in.Featured,
		Draft:       in.Draft,
		ContentDate: parseContentDate(in.Date),
		PublishedAt: publishedAtFor(in.Draft, existing.PublishedAt),
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a post with this locale and slug already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update post")
		return
	}
	writeJSON(w, http.StatusOK, toPostResponse(post))
}

func (s *Server) handlePublishPost(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	existing, err := s.q.GetPostByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "post not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load post")
		return
	}

	var body struct {
		Draft bool `json:"draft"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	post, err := s.q.SetPostPublished(r.Context(), db.SetPostPublishedParams{
		ID:          id,
		Draft:       body.Draft,
		PublishedAt: publishedAtFor(body.Draft, existing.PublishedAt),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update publish state")
		return
	}
	writeJSON(w, http.StatusOK, toPostResponse(post))
}

func (s *Server) handleDeletePost(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.q.DeletePost(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete post")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

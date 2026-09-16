package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ibrohimcoder/portfolio-api/internal/auth"
	"github.com/ibrohimcoder/portfolio-api/internal/db"
)

// handleRegister bootstraps the first admin account. Once any user exists,
// registration is closed (this is a single-author CMS).
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	count, err := s.q.CountUsers(r.Context())
	if err != nil {
		s.serverError(w, r, "failed to check existing users", err)
		return
	}
	if count > 0 {
		writeError(w, http.StatusForbidden, "registration is closed")
		return
	}

	var in registerInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	if !emailRe.MatchString(in.Email) {
		writeError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	if len(in.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		s.serverError(w, r, "failed to hash password", err)
		return
	}

	user, err := s.q.CreateUser(r.Context(), db.CreateUserParams{
		Email:        in.Email,
		PasswordHash: hash,
		Name:         strings.TrimSpace(in.Name),
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "email already registered")
			return
		}
		s.serverError(w, r, "failed to create user", err)
		return
	}

	s.respondWithToken(w, r, http.StatusCreated, user)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in loginInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	user, err := s.q.GetUserByEmail(r.Context(), in.Email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "invalid email or password")
			return
		}
		s.serverError(w, r, "login failed", err)
		return
	}

	if !auth.CheckPassword(user.PasswordHash, in.Password) {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	s.respondWithToken(w, r, http.StatusOK, user)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	user, err := s.q.GetUserByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "user not found")
			return
		}
		s.serverError(w, r, "failed to load user", err)
		return
	}

	writeJSON(w, http.StatusOK, toUserResponse(user))
}

// Takes the request only so a failure here can be reported like any other:
// issuing a token cannot fail for a reason the caller could have prevented, so
// when it does, someone should hear about it.
func (s *Server) respondWithToken(
	w http.ResponseWriter, r *http.Request, status int, user db.User,
) {
	token, expiresAt, err := s.tokens.Generate(user.ID, user.Email)
	if err != nil {
		s.serverError(w, r, "failed to issue token", err)
		return
	}
	writeJSON(w, status, authResponse{
		Token:     token,
		ExpiresAt: expiresAt,
		User:      toUserResponse(user),
	})
}

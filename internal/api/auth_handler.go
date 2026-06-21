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
		writeError(w, http.StatusInternalServerError, "failed to check existing users")
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
		writeError(w, http.StatusInternalServerError, "failed to hash password")
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
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	s.respondWithToken(w, http.StatusCreated, user)
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
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}

	if !auth.CheckPassword(user.PasswordHash, in.Password) {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	s.respondWithToken(w, http.StatusOK, user)
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
		writeError(w, http.StatusInternalServerError, "failed to load user")
		return
	}

	writeJSON(w, http.StatusOK, toUserResponse(user))
}

func (s *Server) respondWithToken(w http.ResponseWriter, status int, user db.User) {
	token, expiresAt, err := s.tokens.Generate(user.ID, user.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}
	writeJSON(w, status, authResponse{
		Token:     token,
		ExpiresAt: expiresAt,
		User:      toUserResponse(user),
	})
}

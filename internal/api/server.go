package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ibrohimcoder/portfolio-api/internal/auth"
	"github.com/ibrohimcoder/portfolio-api/internal/config"
	"github.com/ibrohimcoder/portfolio-api/internal/db"
)

// Server wires together configuration, the database and the HTTP router.
type Server struct {
	cfg    *config.Config
	pool   *pgxpool.Pool
	q      *db.Queries
	tokens *auth.TokenManager
	router *chi.Mux
}

func NewServer(cfg *config.Config, pool *pgxpool.Pool) *Server {
	s := &Server{
		cfg:    cfg,
		pool:   pool,
		q:      db.New(pool),
		tokens: auth.NewTokenManager(cfg.JWTSecret, cfg.JWTTTL),
	}
	s.router = s.routes()
	return s
}

func (s *Server) Router() http.Handler { return s.router }

func (s *Server) routes() *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   s.cfg.CORSOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/healthz", s.handleHealth)

	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/auth", func(r chi.Router) {
			r.Post("/register", s.handleRegister)
			r.Post("/login", s.handleLogin)
			r.With(s.authMiddleware).Get("/me", s.handleMe)
		})

		// Public, unauthenticated read-only API for the website.
		// Returns only published (draft = false) content.
		r.Route("/public", func(r chi.Router) {
			r.Get("/posts", s.handlePublicListPosts)
			r.Get("/posts/{locale}/{slug}", s.handlePublicGetPost)
			r.Get("/projects", s.handlePublicListProjects)
			r.Get("/projects/{locale}/{slug}", s.handlePublicGetProject)
		})

		r.Group(func(r chi.Router) {
			r.Use(s.authMiddleware)

			r.Route("/posts", func(r chi.Router) {
				r.Get("/", s.handleListPosts)
				r.Post("/", s.handleCreatePost)
				r.Get("/{id}", s.handleGetPost)
				r.Put("/{id}", s.handleUpdatePost)
				r.Patch("/{id}/publish", s.handlePublishPost)
				r.Delete("/{id}", s.handleDeletePost)
			})

			r.Route("/projects", func(r chi.Router) {
				r.Get("/", s.handleListProjects)
				r.Post("/", s.handleCreateProject)
				r.Get("/{id}", s.handleGetProject)
				r.Put("/{id}", s.handleUpdateProject)
				r.Delete("/{id}", s.handleDeleteProject)
			})
		})
	})

	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.pool.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

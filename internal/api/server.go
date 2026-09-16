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

	// Guards the one public write endpoint (invitation submissions).
	invLimiter *rateLimiter

	// Sliding window of recent 5xx responses, for the burst alert.
	errRate *errorRate
}

func NewServer(cfg *config.Config, pool *pgxpool.Pool) *Server {
	s := &Server{
		cfg:    cfg,
		pool:   pool,
		q:      db.New(pool),
		tokens: auth.NewTokenManager(cfg.JWTSecret, cfg.JWTTTL),
		// 10 submissions per IP per hour is far above real use and far below
		// anything worth calling spam.
		invLimiter: newRateLimiter(10, time.Hour),
		errRate:    &errorRate{},
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
	// Ours instead of middleware.Recoverer: two recoveries in one chain means
	// the inner one swallows the panic and the outer one never hears about it,
	// and the whole point of this one is that Telegram hears about it.
	r.Use(s.recoverPanic)
	// Outside the router's own error handling, so it sees the status that was
	// actually written — including the 500 a panic turns into.
	r.Use(s.watchErrorRate)
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

			// Write endpoint for the standalone invitation site. Public by
			// necessity — the visitor is not a user of this system.
			r.With(s.invLimiter.middleware).
				Post("/invitations", s.handleCreateInvitation)
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

			// Submissions from the invitation site — read-only from here.
			r.Route("/invitations", func(r chi.Router) {
				r.Get("/", s.handleListInvitations)
				r.Get("/{id}", s.handleGetInvitation)
				r.Delete("/{id}", s.handleDeleteInvitation)
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

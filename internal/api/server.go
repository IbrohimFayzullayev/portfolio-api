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

	// Read-only tokens for the Telegram Mini App (see viewer.go).
	viewers *auth.ViewerTokens

	// Guards the one public write endpoint (invitation submissions).
	invLimiter *rateLimiter

	// Credential guessing is the one attack a single-user CMS is certain to
	// see. Per IP, across login and register.
	authLimiter *rateLimiter

	// The contact form: per IP, and one shared budget for the whole endpoint.
	contactLimiter *rateLimiter
	contactBudget  *rateLimiter

	// Browser error reports: per IP, and a cap on how many become alerts.
	clientErrorLimiter *rateLimiter
	clientErrorBudget  *rateLimiter

	// Sliding window of recent 5xx responses, for the burst alert.
	errRate *errorRate
}

func NewServer(cfg *config.Config, pool *pgxpool.Pool) *Server {
	s := &Server{
		cfg:     cfg,
		pool:    pool,
		q:       db.New(pool),
		tokens:  auth.NewTokenManager(cfg.JWTSecret, cfg.JWTTTL),
		viewers: auth.NewViewerTokens(cfg.JWTSecret, viewerTokenTTL),
		// 10 submissions per IP per hour is far above real use and far below
		// anything worth calling spam.
		invLimiter: newRateLimiter(10, time.Hour),
		// Ten tries per quarter hour is invisible to someone who mistyped a
		// password and turns a dictionary run into a matter of years.
		authLimiter: newRateLimiter(10, 15*time.Minute),
		// Nobody writes five messages an hour by hand.
		contactLimiter: newRateLimiter(5, time.Hour),
		contactBudget:  newRateLimiter(30, time.Hour),
		// A page stuck in an error loop reports the same thing repeatedly;
		// thirty is enough to see that without storing all of it.
		clientErrorLimiter: newRateLimiter(30, time.Hour),
		clientErrorBudget:  newRateLimiter(6, time.Hour),
		errRate:            &errorRate{},
	}
	s.router = s.routes()
	return s
}

func (s *Server) Router() http.Handler { return s.router }

func (s *Server) routes() *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	// Ours, not middleware.RealIP — see realIP for why that one is unsafe here.
	r.Use(realIP)
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
		AllowedOrigins: s.cfg.CORSOrigins,
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type"},
		// The dashboard's session lives in an HttpOnly cookie, and a browser
		// only attaches a cookie to a cross-origin request when the server
		// says so here. Safe with an explicit origin list; the cookie-auth
		// path additionally checks Origin itself (see authMiddleware).
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/healthz", s.handleHealth)

	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/auth", func(r chi.Router) {
			r.With(s.authLimiter.middleware).Post("/register", s.handleRegister)
			r.With(s.authLimiter.middleware).Post("/login", s.handleLogin)
			// No auth on purpose: logging out must work with an expired or
			// already-invalid session, which is when people press it.
			r.Post("/logout", s.handleLogout)
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

			// The public site's contact form and its error boundaries. Both
			// arrive through the site's own server routes, never straight
			// from the browser — the site knows this API only by its
			// internal address.
			r.With(s.contactLimiter.middleware).
				Post("/contact", s.handleCreateContact)
			r.With(s.clientErrorLimiter.middleware).
				Post("/client-errors", s.handleClientError)
		})

		// The Telegram Mini App signs in with Telegram's init data and gets a
		// read-only token. Same limiter as password login: both are ways in.
		r.With(s.authLimiter.middleware).Post("/tg/auth", s.handleTelegramAuth)

		// The money log. Reads are open to the Mini App's viewer token as
		// well as the admin session; writes are admin only.
		r.Route("/finance", func(r chi.Router) {
			r.Group(func(r chi.Router) {
				r.Use(s.adminOrViewer)
				r.Get("/meta", s.handleFinanceMeta)
				r.Get("/summary", s.handleFinanceSummary)
				r.Get("/entries", s.handleListFinanceEntries)
				r.Get("/debts", s.handleFinanceDebts)
				r.Get("/checkpoints", s.handleListFinanceCheckpoints)
			})
			r.Group(func(r chi.Router) {
				r.Use(s.authMiddleware)
				r.Post("/entries", s.handleCreateFinanceEntry)
				r.Put("/entries/{id}", s.handleUpdateFinanceEntry)
				r.Delete("/entries/{id}", s.handleDeleteFinanceEntry)
				r.Post("/checkpoints", s.handleCreateFinanceCheckpoint)
				r.Delete("/checkpoints/{id}", s.handleDeleteFinanceCheckpoint)
			})
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

			// Contact form messages.
			r.Route("/messages", func(r chi.Router) {
				r.Get("/", s.handleListContactMessages)
				r.Patch("/{id}/read", s.handleMarkContactMessageRead)
				r.Delete("/{id}", s.handleDeleteContactMessage)
			})

			// One read-only snapshot of how the stack is doing.
			r.Get("/system", s.handleSystemOverview)

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

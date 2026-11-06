// Command portfolio-mcp is the portfolio's MCP server. It holds no data of
// its own and reads no database; every tool is a call to the portfolio API.
// See internal/portfoliomcp.
//
// Two ways to run it:
//
//   - stdio (default): an MCP client on the laptop starts it - over SSH,
//     inside the api container - and talks through stdin and stdout.
//   - Streamable HTTP (-http :8091, or MCP_HTTP_ADDR): the long-running
//     `mcp` service behind Caddy, for a custom connector in the Claude app
//     (web, desktop, phone). OAuth in front: internal/mcpauth.
//
// Environment:
//
//	PORTFOLIO_API_URL       default http://localhost:8080 (http://api:8080 in compose)
//	PORTFOLIO_API_EMAIL     the admin login the tools act as, with
//	PORTFOLIO_API_PASSWORD
//	PORTFOLIO_API_TOKEN     or a token from /api/v1/auth/login instead
//	MCP_HTTP_ADDR           HTTP mode: listen address
//	MCP_PUBLIC_URL          HTTP mode: https://mcp.fayzullayev.uz
//	MCP_SIGNING_KEY         HTTP mode: 32+ random bytes (openssl rand -hex 32)
//
// docs/portfolio-mcp.md has the client setup.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/mcp"
	"github.com/ibrohimcoder/portfolio-api/internal/mcpauth"
	"github.com/ibrohimcoder/portfolio-api/internal/portfoliomcp"
)

func main() {
	// In stdio mode stdout is the protocol; logs always go to stderr.
	log.SetOutput(os.Stderr)
	log.SetPrefix("portfolio-mcp: ")
	log.SetFlags(0)

	httpAddr := flag.String("http", os.Getenv("MCP_HTTP_ADDR"), "serve Streamable HTTP with OAuth on this address instead of stdio")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	base := os.Getenv("PORTFOLIO_API_URL")
	if base == "" {
		base = "http://localhost:8080"
	}
	client := &portfoliomcp.Client{
		BaseURL:  base,
		Email:    os.Getenv("PORTFOLIO_API_EMAIL"),
		Password: os.Getenv("PORTFOLIO_API_PASSWORD"),
		Token:    os.Getenv("PORTFOLIO_API_TOKEN"),
	}
	if client.Token == "" && (client.Email == "" || client.Password == "") {
		log.Print("no credentials set; every tool will fail until PORTFOLIO_API_EMAIL and PORTFOLIO_API_PASSWORD (or PORTFOLIO_API_TOKEN) are given")
	}

	srv := portfoliomcp.New(client, nil)
	log.Printf("v%s, API %s", portfoliomcp.Version, base)

	if *httpAddr == "" {
		if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
			log.Fatal(err)
		}
		return
	}
	if err := serveHTTP(ctx, *httpAddr, srv, client); err != nil {
		log.Fatal(err)
	}
}

func serveHTTP(ctx context.Context, addr string, srv *mcp.Server, client *portfoliomcp.Client) error {
	key := os.Getenv("MCP_SIGNING_KEY")
	if key == "" {
		// Like the finance bot without a token: the container stays up and
		// idle instead of restarting in a loop until the key is in .env.
		log.Print("MCP_SIGNING_KEY is not set; the HTTP server stays off (openssl rand -hex 32)")
		<-ctx.Done()
		return nil
	}
	auth, err := mcpauth.New(mcpauth.Config{
		PublicURL: os.Getenv("MCP_PUBLIC_URL"),
		Key:       []byte(key),
		// Signing in to the connector = signing in to the dashboard: the
		// API decides, there is no second password.
		CheckLogin: func(ctx context.Context, email, password string) error {
			err := client.VerifyLogin(ctx, email, password)
			var apiErr *portfoliomcp.APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
				return mcpauth.ErrBadCredentials
			}
			return err
		},
	})
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	auth.Register(mux)
	mux.Handle("/mcp", auth.Protect(srv.HTTPHandler()))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("Portfolio MCP server. Add " + auth.Resource() + " as a custom connector in Claude.\n"))
	})

	hs := &http.Server{
		Addr:              addr,
		Handler:           accessLog(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute, // a tool call waits on the API
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	log.Printf("HTTP on %s, resource %s", addr, auth.Resource())
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// accessLog: method, path, status, time. Never the query or headers - they
// carry codes and tokens.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/healthz" {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
		}
	})
}

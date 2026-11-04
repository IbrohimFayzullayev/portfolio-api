// Command portfolio-mcp is the portfolio's MCP server over stdio: an MCP
// client (Claude Code, Claude Desktop) starts it and talks to it through
// stdin and stdout. It holds no data of its own and reads no database; every
// tool is a call to the portfolio API. See internal/portfoliomcp.
//
//	PORTFOLIO_API_URL       default http://localhost:8080 (http://api:8080 in compose)
//	PORTFOLIO_API_EMAIL     the admin login, with
//	PORTFOLIO_API_PASSWORD
//	PORTFOLIO_API_TOKEN     or a token from /api/v1/auth/login instead
//
// docs/portfolio-mcp.md has the client setup.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ibrohimcoder/portfolio-api/internal/portfoliomcp"
)

func main() {
	// stdout is the protocol; logs go to stderr, where clients show them.
	log.SetOutput(os.Stderr)
	log.SetPrefix("portfolio-mcp: ")
	log.SetFlags(0)

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
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

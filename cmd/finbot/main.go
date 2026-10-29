// Command finbot runs the stand-alone finance bot: the Telegram bot and the
// web API behind its Mini App, in one process with its own database.
//
// It ships in the same image as the API (Dockerfile) and runs as its own
// container. See internal/finbot for what it is and why it is kept apart.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	// The runtime image has no zone database; dates are Tashkent dates.
	_ "time/tzdata"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	finbotmigrations "github.com/ibrohimcoder/portfolio-api/db/finbot"
	"github.com/ibrohimcoder/portfolio-api/internal/database"
	"github.com/ibrohimcoder/portfolio-api/internal/finbot"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, databaseURL, err := loadConfig()
	if errors.Is(err, errDisabled) {
		// Not configured on this server yet. Idle instead of exiting: the
		// container restarts whatever the exit code, and a crash loop would
		// only fill the log with the same line.
		log.Println("finbot: FINBOT_TOKEN is not set; idle until configured")
		<-ctx.Done()
		return
	}
	if err != nil {
		log.Fatalf("finbot: config: %v", err)
	}

	pool, err := connect(ctx, databaseURL)
	if err != nil {
		log.Fatalf("finbot: database: %v", err)
	}
	defer pool.Close()

	applied, err := database.MigrateFS(ctx, pool, finbotmigrations.FS, "migrations")
	if err != nil {
		log.Fatalf("finbot: migrate: %v", err)
	}
	if len(applied) > 0 {
		log.Printf("finbot: applied migrations %v", applied)
	}

	b := finbot.New(cfg, pool)

	if cfg.HTTPAddr != "" {
		srv := &http.Server{
			Addr:              cfg.HTTPAddr,
			Handler:           b.Handler(),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
		}
		go func() {
			log.Printf("finbot: web API on %s", cfg.HTTPAddr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("finbot: http: %v", err)
			}
		}()
		defer func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
		}()
	}

	if err := b.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("finbot: %v", err)
		return
	}
	log.Println("finbot: stopped")
}

var errDisabled = errors.New("finbot disabled")

func loadConfig() (finbot.Config, string, error) {
	token := strings.TrimSpace(os.Getenv("FINBOT_TOKEN"))
	if token == "" {
		return finbot.Config{}, "", errDisabled
	}
	var admin int64
	if raw := strings.TrimSpace(os.Getenv("FINBOT_ADMIN_ID")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return finbot.Config{}, "", fmt.Errorf("FINBOT_ADMIN_ID: %q is not a number", raw)
		}
		admin = id
	}
	if admin == 0 {
		// Without an admin nobody can ever be let in.
		return finbot.Config{}, "", errors.New("FINBOT_ADMIN_ID is required")
	}
	databaseURL := os.Getenv("FINBOT_DATABASE_URL")
	if databaseURL == "" {
		return finbot.Config{}, "", errors.New("FINBOT_DATABASE_URL is required")
	}
	var origins []string
	if raw := os.Getenv("FINBOT_CORS_ORIGINS"); raw != "" {
		origins = strings.Split(raw, ",")
	}
	// The admin may see amounts unless this says otherwise. Users are told
	// which it is (see privacyLine), so the switch and the promise agree.
	seesMoney := true
	if raw := strings.TrimSpace(os.Getenv("FINBOT_ADMIN_SEES_MONEY")); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return finbot.Config{}, "", fmt.Errorf("FINBOT_ADMIN_SEES_MONEY: %q is not true/false", raw)
		}
		seesMoney = v
	}
	return finbot.Config{
		AdminKey:       strings.TrimSpace(os.Getenv("FINBOT_ADMIN_KEY")),
		AdminSeesMoney: seesMoney,
		Token:          token,
		AdminID:        admin,
		WebAppURL:      strings.TrimSpace(os.Getenv("FINBOT_WEBAPP_URL")),
		HTTPAddr:       getenv("FINBOT_HTTP_ADDR", ":8090"),
		CORSOrigins:    origins,
	}, databaseURL, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// connect opens the finance database, creating it first if this is the
// first start. It lives on the same PostgreSQL server as the portfolio's,
// and the server's own entrypoint only ever creates POSTGRES_DB - doing it
// here saves a manual step that would otherwise be forgotten.
func connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	for attempt := 1; ; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		pool, err := database.Connect(cctx, databaseURL)
		cancel()
		if err == nil {
			return pool, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "3D000" { // invalid_catalog_name
			if cerr := createDatabase(ctx, databaseURL); cerr != nil {
				return nil, fmt.Errorf("create database: %w", cerr)
			}
			continue
		}
		// The database container may still be starting.
		if attempt >= 10 || ctx.Err() != nil {
			return nil, err
		}
		log.Printf("finbot: database not ready (%v), retrying", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func createDatabase(ctx context.Context, databaseURL string) error {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return err
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" {
		return errors.New("FINBOT_DATABASE_URL names no database")
	}
	u.Path = "/postgres"
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	log.Printf("finbot: creating database %q", name)
	_, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	return err
}

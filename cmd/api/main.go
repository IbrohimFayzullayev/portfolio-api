package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/api"
	"github.com/ibrohimcoder/portfolio-api/internal/config"
	"github.com/ibrohimcoder/portfolio-api/internal/database"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx := context.Background()

	connectCtx, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
	defer cancelConnect()
	pool, err := database.Connect(connectCtx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()

	// Apply pending migrations automatically on startup.
	migrateCtx, cancelMigrate := context.WithTimeout(ctx, 30*time.Second)
	defer cancelMigrate()
	applied, err := database.Migrate(migrateCtx, pool)
	if err != nil {
		log.Fatalf("migrate: %v", err)
	}
	if len(applied) > 0 {
		log.Printf("applied %d migration(s): %v", len(applied), applied)
	} else {
		log.Println("database schema up to date")
	}

	srv := api.NewServer(cfg, pool)

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("API listening on :%s (env=%s)", cfg.Port, cfg.Env)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("shutting down...")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("server stopped")
}

// Command bot runs the operations bot for this stack.
//
// It ships in the same image as the API but runs as its own container, so a
// crashed API does not take the bot down with it.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	// The runtime image is alpine without tzdata; embedding the zone database
	// keeps Asia/Tashkent timestamps correct without an extra apk package.
	_ "time/tzdata"

	"github.com/ibrohimcoder/portfolio-api/internal/bot"
	"github.com/ibrohimcoder/portfolio-api/internal/database"
)

func main() {
	cfg, databaseURL, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := database.Connect(connectCtx, databaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()

	// Migrations belong to the API — the bot only reads. If it starts first,
	// the tables may not exist yet; that surfaces as a command error, not a
	// crash, and resolves itself once the API has run.
	b := bot.New(cfg, pool)

	if err := b.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("bot: %v", err)
	}
	log.Println("bot stopped")
}

func loadConfig() (bot.Config, string, error) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return bot.Config{}, "", errors.New("TELEGRAM_BOT_TOKEN is required")
	}

	rawID := os.Getenv("TELEGRAM_ALLOWED_USER_ID")
	if rawID == "" {
		return bot.Config{}, "", errors.New("TELEGRAM_ALLOWED_USER_ID is required")
	}
	userID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return bot.Config{}, "", errors.New("TELEGRAM_ALLOWED_USER_ID must be a number")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return bot.Config{}, "", errors.New("DATABASE_URL is required")
	}

	return bot.Config{
		Token:          token,
		AllowedUserID:  userID,
		InternalAPIURL: getenv("INTERNAL_API_URL", "http://api:8080"),
		PublicSiteURL:  getenv("PUBLIC_SITE_URL", "https://fayzullayev.uz"),
		AdminSiteURL:   getenv("ADMIN_SITE_URL", "https://admin.fayzullayev.uz"),
		PublicAPIURL:   getenv("PUBLIC_API_URL", "https://api.fayzullayev.uz"),
	}, databaseURL, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

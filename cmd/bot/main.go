// Command bot runs the operations bot for this stack.
//
// It ships in the same image as the API but runs as its own container, so a
// crashed API does not take the bot down with it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
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

	// One id, or several separated by commas. The first is where every
	// notification is delivered; the rest may only talk to the bot.
	rawIDs := os.Getenv("TELEGRAM_ALLOWED_USER_ID")
	if rawIDs == "" {
		return bot.Config{}, "", errors.New("TELEGRAM_ALLOWED_USER_ID is required")
	}

	var userIDs []int64
	for _, field := range strings.Split(rawIDs, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return bot.Config{}, "", fmt.Errorf(
				"TELEGRAM_ALLOWED_USER_ID: %q is not a number", field)
		}
		userIDs = append(userIDs, id)
	}
	if len(userIDs) == 0 {
		return bot.Config{}, "", errors.New("TELEGRAM_ALLOWED_USER_ID is empty")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return bot.Config{}, "", errors.New("DATABASE_URL is required")
	}

	return bot.Config{
		Token:          token,
		AllowedUserIDs: userIDs,
		InternalAPIURL: getenv("INTERNAL_API_URL", "http://api:8080"),
		PublicSiteURL:  getenv("PUBLIC_SITE_URL", "https://fayzullayev.uz"),
		AdminSiteURL:   getenv("ADMIN_SITE_URL", "https://admin.fayzullayev.uz"),
		PublicAPIURL:   getenv("PUBLIC_API_URL", "https://api.fayzullayev.uz"),

		// Optional. Missing values mean the bot never prints traffic numbers,
		// which is the correct behaviour for a stack without analytics — not
		// an error to fail startup over.
		HeartbeatPingURL: os.Getenv("HEARTBEAT_PING_URL"),

		UmamiURL:       os.Getenv("UMAMI_URL"),
		UmamiWebsiteID: os.Getenv("UMAMI_WEBSITE_ID"),
		UmamiUsername:  os.Getenv("UMAMI_USERNAME"),
		UmamiPassword:  os.Getenv("UMAMI_PASSWORD"),
		UmamiAPIKey:    os.Getenv("UMAMI_API_KEY"),
	}, databaseURL, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

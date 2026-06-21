package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all runtime configuration, loaded from the environment.
type Config struct {
	Port        string
	Env         string
	DatabaseURL string
	JWTSecret   string
	JWTTTL      time.Duration
	CORSOrigins []string
}

// Load reads configuration from a .env file (if present) and the environment.
func Load() (*Config, error) {
	_ = godotenv.Load()

	ttlHours := 24
	if v := os.Getenv("JWT_TTL_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ttlHours = n
		}
	}

	cfg := &Config{
		Port:        getenv("PORT", "8080"),
		Env:         getenv("ENV", "development"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		JWTSecret:   os.Getenv("JWT_SECRET"),
		JWTTTL:      time.Duration(ttlHours) * time.Hour,
		CORSOrigins: splitAndTrim(getenv("CORS_ORIGINS", "http://localhost:3001")),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required")
	}

	return cfg, nil
}

func (c *Config) IsProduction() bool {
	return c.Env == "production"
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

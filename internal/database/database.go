package database

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	migrations "github.com/ibrohimcoder/portfolio-api/db"
)

// Connect opens a pgx connection pool and verifies connectivity.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// Migrate applies any pending *.up.sql migrations embedded in the binary.
// It tracks applied versions in the schema_migrations table, so it is safe to
// run on every startup.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (applied []string, err error) {
	if _, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrations.FS, "migrations")
	if err != nil {
		return nil, err
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		version := strings.TrimSuffix(name, ".up.sql")

		var exists bool
		if err = pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`,
			version,
		).Scan(&exists); err != nil {
			return applied, err
		}
		if exists {
			continue
		}

		content, readErr := fs.ReadFile(migrations.FS, "migrations/"+name)
		if readErr != nil {
			return applied, readErr
		}

		tx, beginErr := pool.Begin(ctx)
		if beginErr != nil {
			return applied, beginErr
		}
		// No arguments -> pgx uses the simple protocol, which allows the
		// multi-statement SQL in a migration file.
		if _, execErr := tx.Exec(ctx, string(content)); execErr != nil {
			_ = tx.Rollback(ctx)
			return applied, fmt.Errorf("apply %s: %w", name, execErr)
		}
		if _, insErr := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, version,
		); insErr != nil {
			_ = tx.Rollback(ctx)
			return applied, insErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return applied, commitErr
		}
		applied = append(applied, version)
	}

	return applied, nil
}

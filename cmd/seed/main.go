// Command seed imports the Markdown/MDX content under the public site's
// content/ directory into PostgreSQL. It is idempotent: running it again
// updates existing rows (matched on locale+slug) instead of duplicating them.
//
// Usage:
//
//	go run ./cmd/seed                 # uses ../portfolio/content
//	go run ./cmd/seed -dir /path/to/content
//	CONTENT_DIR=/path/to/content go run ./cmd/seed
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/config"
	"github.com/ibrohimcoder/portfolio-api/internal/database"
	"github.com/ibrohimcoder/portfolio-api/internal/db"
)

const dateLayout = "2006-01-02"

func main() {
	dirFlag := flag.String("dir", "", "path to the content directory (defaults to $CONTENT_DIR or ../portfolio/content)")
	flag.Parse()

	contentDir := *dirFlag
	if contentDir == "" {
		contentDir = os.Getenv("CONTENT_DIR")
	}
	if contentDir == "" {
		contentDir = filepath.Join("..", "portfolio", "content")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx := context.Background()
	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()

	// Make sure the tables exist before seeding.
	if _, err := database.Migrate(ctx, pool); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	q := db.New(pool)

	posts, err := seedPosts(ctx, q, filepath.Join(contentDir, "posts"))
	if err != nil {
		log.Fatalf("seed posts: %v", err)
	}
	projects, err := seedProjects(ctx, q, filepath.Join(contentDir, "projects"))
	if err != nil {
		log.Fatalf("seed projects: %v", err)
	}

	log.Printf("seeded %d post(s) and %d project(s) from %s", posts, projects, contentDir)
}

/* ------------------------------- posts ---------------------------------- */

func seedPosts(ctx context.Context, q *db.Queries, dir string) (int, error) {
	files, err := mdxFiles(dir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, path := range files {
		fm, body, err := parseFile(path)
		if err != nil {
			return count, fmt.Errorf("%s: %w", path, err)
		}

		locale := fm.str("locale")
		if locale == "" {
			locale = localeFromPath(path)
		}
		slug := fm.str("slug")
		if slug == "" {
			slug = slugFromPath(path)
		}
		draft := fm.boolean("draft", false)
		date := fm.date("date")

		_, err = q.UpsertPost(ctx, db.UpsertPostParams{
			Locale:      locale,
			Slug:        slug,
			Title:       fm.str("title"),
			Description: fm.str("description"),
			Body:        body,
			Tags:        fm.list("tags"),
			Cover:       fm.str("cover"),
			Featured:    fm.boolean("featured", false),
			Draft:       draft,
			ContentDate: date,
			PublishedAt: publishedAt(draft, date),
		})
		if err != nil {
			return count, fmt.Errorf("%s: upsert: %w", path, err)
		}
		count++
		log.Printf("  post  %-5s %s", locale, slug)
	}
	return count, nil
}

/* ------------------------------ projects -------------------------------- */

func seedProjects(ctx context.Context, q *db.Queries, dir string) (int, error) {
	files, err := mdxFiles(dir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, path := range files {
		fm, body, err := parseFile(path)
		if err != nil {
			return count, fmt.Errorf("%s: %w", path, err)
		}

		locale := fm.str("locale")
		if locale == "" {
			locale = localeFromPath(path)
		}
		slug := fm.str("slug")
		if slug == "" {
			slug = slugFromPath(path)
		}

		_, err = q.UpsertProject(ctx, db.UpsertProjectParams{
			Locale:      locale,
			Slug:        slug,
			Title:       fm.str("title"),
			Description: fm.str("description"),
			Body:        body,
			Tags:        fm.list("tags"),
			Stack:       fm.list("stack"),
			Url:         fm.str("url"),
			Repo:        fm.str("repo"),
			SortOrder:   int32(fm.number("order")),
			Featured:    fm.boolean("featured", false),
			Draft:       fm.boolean("draft", false),
			ContentDate: fm.date("date"),
		})
		if err != nil {
			return count, fmt.Errorf("%s: upsert: %w", path, err)
		}
		count++
		log.Printf("  proj  %-5s %s", locale, slug)
	}
	return count, nil
}

/* ------------------------------- helpers -------------------------------- */

func mdxFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if ext := strings.ToLower(filepath.Ext(path)); ext == ".mdx" || ext == ".md" {
			files = append(files, path)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return files, err
}

// frontmatter is a parsed key -> raw value map.
type frontmatter map[string]string

func (f frontmatter) str(key string) string {
	return unquote(strings.TrimSpace(f[key]))
}

func (f frontmatter) boolean(key string, def bool) bool {
	v := strings.TrimSpace(f[key])
	if v == "" {
		return def
	}
	return v == "true"
}

func (f frontmatter) number(key string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(f[key]))
	return n
}

func (f frontmatter) date(key string) time.Time {
	v := strings.TrimSpace(f[key])
	if t, err := time.Parse(dateLayout, v); err == nil {
		return t
	}
	return time.Now().UTC().Truncate(24 * time.Hour)
}

// list parses an inline YAML array: tags: [a, b, c]
func (f frontmatter) list(key string) []string {
	v := strings.TrimSpace(f[key])
	out := []string{}
	if v == "" {
		return out
	}
	v = strings.TrimPrefix(v, "[")
	v = strings.TrimSuffix(v, "]")
	for _, part := range strings.Split(v, ",") {
		if item := unquote(strings.TrimSpace(part)); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// parseFile splits an .mdx file into its frontmatter map and Markdown body.
func parseFile(path string) (frontmatter, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")

	fm := frontmatter{}
	if !strings.HasPrefix(content, "---\n") {
		// No frontmatter; whole file is the body.
		return fm, strings.TrimSpace(content), nil
	}

	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end == -1 {
		return fm, strings.TrimSpace(content), nil
	}
	header := rest[:end]
	body := rest[end+len("\n---"):]
	body = strings.TrimPrefix(body, "\n")

	for _, line := range strings.Split(header, "\n") {
		line = strings.TrimRight(line, " ")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx == -1 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		fm[key] = val
	}

	return fm, strings.TrimSpace(body), nil
}

func localeFromPath(path string) string {
	// .../posts/en/slug.mdx -> "en"
	parent := filepath.Base(filepath.Dir(path))
	return parent
}

func slugFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// publishedAt mirrors the API's logic: drafts have no published_at; published
// content is stamped with its content date.
func publishedAt(draft bool, date time.Time) *time.Time {
	if draft {
		return nil
	}
	t := date.UTC()
	return &t
}

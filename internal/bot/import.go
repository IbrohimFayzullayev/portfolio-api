package bot

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Writing a post as a file.
//
// A Telegram message stops at 4096 characters, which is roughly a third of a
// real article, and the chat box is a bad editor besides. So the long form is
// a file: write it wherever you like, send the .md, and the bot reads the
// front matter for the metadata and the rest as the body.
//
// Sending the same file again updates the post it created rather than failing
// on the unique (locale, slug) — the file stays the source of truth, which is
// what makes editing in a real editor practical.

const (
	// A generous ceiling for prose. Anything far past this is a mistake worth
	// refusing cheaply rather than downloading.
	maxUploadBytes = 512 << 10
	frontMatterSep = "---"
)

func (b *Bot) importDocument(ctx context.Context, doc *Document, caption string) string {
	name := strings.ToLower(doc.FileName)
	switch {
	case !(strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".markdown") || strings.HasSuffix(name, ".txt")):
		return "Faqat <code>.md</code> fayl qabul qilinadi."
	case doc.FileSize > maxUploadBytes:
		return fmt.Sprintf("Fayl juda katta (%d KB). Chegara — %d KB.",
			doc.FileSize/1024, maxUploadBytes/1024)
	}

	path, err := b.tg.getFile(ctx, doc.FileID)
	if err != nil {
		return "❌ Faylni olib bo'lmadi: " + escapeHTML(shortError(err))
	}
	raw, err := b.tg.downloadFile(ctx, path, maxUploadBytes)
	if err != nil {
		return "❌ Yuklab bo'lmadi: " + escapeHTML(shortError(err))
	}

	meta, body := parseFrontMatter(string(raw))
	title, body := postTitle(meta, body, doc.FileName)
	if title == "" {
		return "Sarlavha topilmadi — front matter'da <code>title:</code> yoki matnda <code># Sarlavha</code> bo'lsin."
	}

	locale := firstNonEmpty(meta["locale"], "uz")
	slug := slugify(firstNonEmpty(meta["slug"], title))
	if slug == "" {
		slug = slugify(strings.TrimSuffix(doc.FileName, ".md"))
	}
	if slug == "" {
		slug = "qoralama-" + time.Now().In(tashkent).Format("2006-01-02-1504")
	}

	// draft:false in a file is taken at its word — the file said to publish —
	// but the default is draft, because the common case is a work in progress.
	draft := meta["draft"] != "false"

	var (
		id       string
		inserted bool
	)
	err = b.pool.QueryRow(ctx, `
		INSERT INTO posts (locale, slug, title, description, body, tags,
		                   draft, content_date, translation_key)
		VALUES ($1, $2, $3, $4, $5, $6::text[], $7, CURRENT_DATE, $8)
		ON CONFLICT (locale, slug) DO UPDATE
		SET title       = EXCLUDED.title,
		    description = EXCLUDED.description,
		    body        = EXCLUDED.body,
		    tags        = EXCLUDED.tags,
		    -- An existing key is kept unless the file names a new one: the
		    -- pairing is usually set up in the dashboard, not in the file.
		    translation_key = CASE WHEN EXCLUDED.translation_key <> ''
		                          THEN EXCLUDED.translation_key
		                          ELSE posts.translation_key END,
		    updated_at  = now()
		RETURNING id::text, (xmax = 0) AS inserted`,
		locale, slug, title,
		firstNonEmpty(meta["description"], caption),
		body,
		splitTags(meta["tags"]),
		draft,
		meta["translation_key"],
	).Scan(&id, &inserted)
	if err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}

	verb := "yangilandi"
	if inserted {
		verb = "yaratildi"
	}
	state := "⚪️ qoralama"
	if !draft {
		state = "🟢 nashr"
	}

	return fmt.Sprintf(
		"✅ <b>Post %s</b>\n\n%s\n<code>%s/%s</code> · %s\n%d belgi\n\n<code>/tahrir %s</code>",
		verb, escapeHTML(title), escapeHTML(locale), escapeHTML(slug), state,
		len([]rune(body)), escapeHTML(slug))
}

/* ----------------------------- front matter ------------------------------ */

// parseFrontMatter reads the leading "---" block as key: value pairs. A file
// without one is not an error — it is just a body, and the title is then found
// in the text.
func parseFrontMatter(raw string) (map[string]string, string) {
	meta := map[string]string{}

	text := strings.ReplaceAll(raw, "\r\n", "\n")
	text = strings.TrimLeft(text, "\ufeff \n\t") // BOM, from some editors

	if !strings.HasPrefix(text, frontMatterSep) {
		return meta, strings.TrimSpace(text)
	}

	rest := strings.TrimPrefix(text, frontMatterSep)
	end := strings.Index(rest, "\n"+frontMatterSep)
	if end < 0 {
		// An opening fence with no closing one: treat the whole thing as body
		// rather than silently swallowing the article.
		return meta, strings.TrimSpace(text)
	}

	block := rest[:end]
	body := rest[end+len("\n"+frontMatterSep):]

	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		// Nothing here needs YAML lists; tags are a comma-separated line, and
		// square brackets are stripped so both spellings work.
		value = strings.Trim(value, "[]")
		meta[strings.ToLower(strings.TrimSpace(key))] = value
	}

	return meta, strings.TrimSpace(body)
}

// postTitle prefers the front matter, then a leading "# heading" (which it
// removes, so the title is not repeated as the first line of the body), then
// the file name.
func postTitle(meta map[string]string, body, fileName string) (string, string) {
	if t := strings.TrimSpace(meta["title"]); t != "" {
		return t, body
	}

	lines := strings.SplitN(body, "\n", 2)
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "# ") {
		title := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[0]), "# "))
		rest := ""
		if len(lines) > 1 {
			rest = strings.TrimSpace(lines[1])
		}
		return title, rest
	}

	base := strings.TrimSuffix(strings.TrimSuffix(fileName, ".md"), ".markdown")
	base = strings.TrimSuffix(base, ".txt")
	base = strings.ReplaceAll(strings.ReplaceAll(base, "-", " "), "_", " ")
	return strings.TrimSpace(base), body
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

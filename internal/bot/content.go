package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Content commands: listing posts and changing their publish state from
// Telegram.
//
// These write to PostgreSQL directly rather than calling the API, for the same
// reason the bot READS it directly: the bot's whole purpose is to still work
// when the API is the thing that is broken, and an ops tool that needs the
// broken service to fix the broken service is not an ops tool. The writes here
// are narrow — a boolean and a timestamp, or one INSERT with validated input —
// so there is no business logic being bypassed, only a network hop.
//
// Everything is scoped to the single authorised user; handle() has already
// rejected everyone else before any of this runs.

const postsListLimit = 10

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

type postRow struct {
	id     string
	locale string
	slug   string
	title  string
	draft  bool
}

/* -------------------------------- /posts --------------------------------- */

func (b *Bot) sendPostsList(ctx context.Context, chatID int64) {
	text, markup := b.postsList(ctx)
	if err := b.tg.sendMessageMarkup(ctx, chatID, text, markup); err != nil {
		log.Printf("bot: sendPostsList failed: %v", err)
	}
}

// postsList renders the message body and its keyboard together — they have to
// agree, and building them in one place is how they stay in step when a post is
// published and the list is redrawn.
func (b *Bot) postsList(ctx context.Context) (string, *inlineKeyboard) {
	posts, err := b.recentPosts(ctx)
	if err != nil {
		return "❌ Postlarni o'qib bo'lmadi: " + escapeHTML(shortError(err)), nil
	}
	if len(posts) == 0 {
		return "Hozircha post yo'q.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>Oxirgi %d post</b>\n", len(posts)))

	rows := make([][]inlineButton, 0, len(posts))
	for _, p := range posts {
		state := "🟢 nashr"
		action := "Draft"
		data := "drf:" + p.id
		if p.draft {
			state = "⚪️ qoralama"
			action = "Nashr"
			data = "pub:" + p.id
		}
		sb.WriteString(fmt.Sprintf("\n%s · <code>%s</code>\n%s\n",
			state, escapeHTML(p.locale), escapeHTML(p.title)))

		rows = append(rows, []inlineButton{{
			Text:         fmt.Sprintf("%s — %s", action, truncate(p.title, 24)),
			CallbackData: data,
		}})
	}

	return sb.String(), &inlineKeyboard{InlineKeyboard: rows}
}

func (b *Bot) recentPosts(ctx context.Context) ([]postRow, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT id::text, locale, slug, title, draft
		FROM posts
		ORDER BY updated_at DESC
		LIMIT $1`, postsListLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []postRow
	for rows.Next() {
		var p postRow
		if err := rows.Scan(&p.id, &p.locale, &p.slug, &p.title, &p.draft); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

/* ---------------------------- button callbacks --------------------------- */

// applyPostAction handles one "pub:<id>" / "drf:<id>" press and returns the
// short text Telegram shows in the button's toast.
func (b *Bot) applyPostAction(ctx context.Context, data string) string {
	action, id, found := strings.Cut(data, ":")
	if !found || id == "" {
		return "Noma'lum amal"
	}

	var draft bool
	switch action {
	case "pub":
		draft = false
	case "drf":
		draft = true
	default:
		return "Noma'lum amal"
	}

	var locale, slug, title string
	// published_at is set on the first publish and kept afterwards, so
	// unpublishing and republishing does not rewrite the original date.
	err := b.pool.QueryRow(ctx, `
		UPDATE posts
		SET draft = $2::boolean,
		    published_at = CASE
		        WHEN published_at IS NULL AND NOT $2::boolean THEN now()
		        ELSE published_at
		    END,
		    updated_at = now()
		WHERE id = $1::uuid
		RETURNING locale, slug, title`,
		id, draft).Scan(&locale, &slug, &title)
	if errors.Is(err, pgx.ErrNoRows) {
		return "Post topilmadi"
	}
	if err != nil {
		return "Xato: " + shortError(err)
	}

	if !draft {
		// Same row the API would write, so the announcement path is identical
		// whichever side did the publishing.
		b.queue(ctx, "post.published", map[string]any{
			"locale": locale,
			"slug":   slug,
			"title":  title,
		})
		return "Nashr qilindi"
	}
	return "Qoralamaga qaytarildi"
}

/* ------------------------------- /qoralama ------------------------------- */

// createDraft turns a Telegram message into a draft post: first line is the
// title, the rest is the body. Always a draft — publishing stays a deliberate
// second step through /posts.
func (b *Bot) createDraft(ctx context.Context, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "Matn kerak.\n\n<code>/qoralama Sarlavha\nBirinchi xatboshi…</code>"
	}

	title, body, _ := strings.Cut(text, "\n")
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" {
		return "Sarlavha bo'sh."
	}

	slug := slugify(title)
	if slug == "" {
		// Non-Latin titles slugify to nothing; a date-based slug is still a
		// valid draft that can be renamed in the dashboard.
		slug = "qoralama-" + time.Now().In(tashkent).Format("2006-01-02-1504")
	}

	_, err := b.pool.Exec(ctx, `
		INSERT INTO posts (locale, slug, title, body, draft, content_date)
		VALUES ($1, $2, $3, $4, true, CURRENT_DATE)`,
		"uz", slug, title, body)
	if err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}

	return fmt.Sprintf(
		"✅ <b>Qoralama saqlandi</b>\n\n%s\n<code>uz/%s</code>\n\nTahrirlash: %s",
		escapeHTML(title), escapeHTML(slug),
		escapeHTML(strings.TrimSuffix(b.cfg.AdminSiteURL, "/")+"/posts"))
}

/* -------------------------------- helpers -------------------------------- */

// cyrillic transliterates Uzbek and Russian Cyrillic into the Latin alphabet
// the site's URLs use.
//
// This used to be left out on purpose: a wrong guess at a transliteration was
// judged worse than an empty slug the caller replaces with a dated one. In
// practice the dated fallback meant every Cyrillic title produced a slug like
// "qoralama-2026-09-10-1430" that had to be fixed by hand in the dashboard,
// which is the same wrong guess with extra steps. The mapping below is the
// ordinary Uzbek one; where the two languages disagree (ц, щ, ы) the Uzbek
// reading wins, and the slug is editable either way.
var cyrillic = strings.NewReplacer(
	"а", "a", "б", "b", "в", "v", "г", "g", "д", "d",
	"е", "e", "ё", "yo", "ж", "j", "з", "z", "и", "i",
	"й", "y", "к", "k", "л", "l", "м", "m", "н", "n",
	"о", "o", "п", "p", "р", "r", "с", "s", "т", "t",
	"у", "u", "ф", "f", "х", "x", "ц", "ts", "ч", "ch",
	"ш", "sh", "щ", "sh", "ъ", "", "ы", "i", "ь", "",
	"э", "e", "ю", "yu", "я", "ya",
	// Uzbek-specific letters.
	"ў", "o", "қ", "q", "ғ", "g", "ҳ", "h",
)

// slugify mirrors the dashboard's rule: lowercase ASCII, hyphen separated.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("ʻ", "", "'", "", "’", "", "‘", "").Replace(s)
	// After lowercasing, so only the lowercase forms need mapping.
	s = cyrillic.Replace(s)
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

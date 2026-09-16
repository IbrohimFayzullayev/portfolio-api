package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Editing a post from Telegram.
//
// /tahrir draws a card: what the post says now, and a button per field. A
// button asks a question; the answer is applied by applyEdit. The card is
// redrawn in place afterwards, so the screen never disagrees with the database.
//
// Writes go straight to PostgreSQL, like the rest of the bot, and for the same
// stated reason (see content.go). The fields here are narrow — one column each,
// from a fixed list — so there is no validation being bypassed, only a hop.

// editableFields is the whitelist. The column name is interpolated into SQL,
// so it can only ever come from this map, never from a message.
var editableFields = map[string]struct {
	column      string
	label       string
	button      string
	hint        string
	placeholder string
}{
	"post.title": {
		column: "title", label: "Sarlavha", button: "Sarlavha",
		placeholder: "Yangi sarlavha",
	},
	"post.body": {
		column: "body", label: "Matn", button: "Matn",
		hint:        "Uzun matn uchun .md faylni yuboring — Telegram xabari 4096 belgi bilan cheklangan.",
		placeholder: "Post matni",
	},
	"post.description": {
		column: "description", label: "Tavsif", button: "Tavsif",
		hint:        "Ro'yxatlarda va qidiruv natijalarida ko'rinadi.",
		placeholder: "Qisqa tavsif",
	},
	"post.tags": {
		column: "tags", label: "Teglar", button: "Teglar",
		hint:        "Vergul bilan ajrating.",
		placeholder: "go, docker, deploy",
	},
	"post.slug": {
		column: "slug", label: "Slug", button: "Slug",
		hint:        "URL o'zgaradi — e'lon qilingan postda eski havola ishlamay qoladi.",
		placeholder: "yangi-slug",
	},
	"post.translation_key": {
		column: "translation_key", label: "Juftlik kaliti", button: "Juftlik kaliti",
		hint:        "uz va en versiyalarni bog'laydi. O'chirish uchun <code>-</code> yuboring.",
		placeholder: "translation key",
	},
}

// Button order, because a map has none. Two per row: any more and the labels
// are unreadable on a phone.
var editButtonRows = [][]string{
	{"post.title", "post.body"},
	{"post.description", "post.tags"},
	{"post.slug", "post.translation_key"},
}

type postDetail struct {
	id             string
	locale         string
	slug           string
	title          string
	description    string
	tags           []string
	draft          bool
	translationKey string
	hasSibling     bool
}

/* -------------------------------- /tahrir -------------------------------- */

func (b *Bot) sendEditCard(ctx context.Context, chatID int64, ref string) {
	text, markup := b.editCard(ctx, ref)
	if err := b.tg.sendMessageMarkup(ctx, chatID, text, markup); err != nil {
		log.Printf("bot: sendEditCard failed: %v", err)
	}
}

// editCard renders the card and its keyboard together — the same pairing the
// posts list uses, for the same reason: they must agree after every redraw.
func (b *Bot) editCard(ctx context.Context, ref string) (string, *inlineKeyboard) {
	p, err := b.findPost(ctx, ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return "Post topilmadi: <code>" + escapeHTML(ref) + "</code>\n\n/posts", nil
	}
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err)), nil
	}

	state := "🟢 nashr"
	if p.draft {
		state = "⚪️ qoralama"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✏️ <b>%s</b>\n", escapeHTML(p.title)))
	sb.WriteString(fmt.Sprintf("<code>%s/%s</code> · %s\n", escapeHTML(p.locale), escapeHTML(p.slug), state))

	if p.description != "" {
		sb.WriteString("\n" + escapeHTML(truncate(p.description, 200)) + "\n")
	}
	if len(p.tags) > 0 {
		sb.WriteString("\n🏷 " + escapeHTML(strings.Join(p.tags, ", ")) + "\n")
	}
	if p.translationKey != "" {
		sibling := "juftligi yo'q"
		if p.hasSibling {
			sibling = "juftligi bor"
		}
		sb.WriteString(fmt.Sprintf("🔗 <code>%s</code> · %s\n", escapeHTML(p.translationKey), sibling))
	}
	sb.WriteString("\n" + escapeHTML(b.dashboardURL(p.id)))

	rows := make([][]inlineButton, 0, len(editButtonRows)+2)
	for _, row := range editButtonRows {
		buttons := make([]inlineButton, 0, len(row))
		for _, intent := range row {
			f := editableFields[intent]
			buttons = append(buttons, inlineButton{
				Text: f.button,
				// "ed:" + short key + ":" + uuid — 41 bytes of the 64 Telegram
				// allows, so the id needs no shortening.
				CallbackData: "ed:" + shortKey(intent) + ":" + p.id,
			})
		}
		rows = append(rows, buttons)
	}

	if !p.hasSibling {
		other := "en"
		if p.locale != "uz" {
			other = "uz"
		}
		rows = append(rows, []inlineButton{{
			Text:         strings.ToUpper(other) + " juftini yaratish",
			CallbackData: "tr:" + p.id,
		}})
	}

	action, data := "Nashr qilish", "epub:"+p.id
	if !p.draft {
		action, data = "Qoralamaga qaytarish", "edrf:"+p.id
	}
	rows = append(rows, []inlineButton{{Text: action, CallbackData: data}})

	return sb.String(), &inlineKeyboard{InlineKeyboard: rows}
}

// findPost accepts what a person would actually type: a slug, "locale/slug",
// a post id, or "oxirgi" for the most recently touched one.
func (b *Bot) findPost(ctx context.Context, ref string) (postDetail, error) {
	ref = strings.TrimSpace(ref)

	query := `
		SELECT p.id::text, p.locale, p.slug, p.title, p.description, p.tags,
		       p.draft, p.translation_key,
		       EXISTS (
		           SELECT 1 FROM posts s
		           WHERE s.translation_key <> ''
		             AND s.translation_key = p.translation_key
		             AND s.locale <> p.locale
		       ) AS has_sibling
		FROM posts p`

	var (
		p   postDetail
		row pgx.Row
	)
	if ref == "" || ref == "oxirgi" {
		row = b.pool.QueryRow(ctx, query+` ORDER BY p.updated_at DESC LIMIT 1`)
	} else {
		row = b.pool.QueryRow(ctx, query+`
			WHERE p.slug = $1
			   OR p.locale || '/' || p.slug = $1
			   OR ($1 ~ '^[0-9a-f-]{36}$' AND p.id = $1::uuid)
			ORDER BY p.updated_at DESC
			LIMIT 1`, ref)
	}

	err := row.Scan(&p.id, &p.locale, &p.slug, &p.title, &p.description, &p.tags,
		&p.draft, &p.translationKey, &p.hasSibling)
	return p, err
}

/* ------------------------------ applying ---------------------------------- */

// startEdit is the button press: ask the question, record what it was about.
func (b *Bot) startEdit(ctx context.Context, chatID int64, key, postID string) string {
	intent := intentForKey(key)
	f, ok := editableFields[intent]
	if !ok {
		return "Noma'lum maydon"
	}

	current := b.currentValue(ctx, f.column, postID)
	text, placeholder := promptForField(intent, current)
	b.ask(ctx, chatID, intent, postID, text, placeholder)

	return f.label + " — javob yozing"
}

func (b *Bot) currentValue(ctx context.Context, column, postID string) string {
	// tags is the one array column; everything else reads as text. The
	// expression is built from editableFields, never from a message.
	expr := column + "::text"
	if column == "tags" {
		expr = "array_to_string(tags, ', ')"
	}

	var value string
	if err := b.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT %s FROM posts WHERE id = $1::uuid`, expr),
		postID).Scan(&value); err != nil {
		return ""
	}
	return value
}

// applyEdit writes one answered question to one column.
func (b *Bot) applyEdit(ctx context.Context, intent, postID, value string) string {
	f, ok := editableFields[intent]
	if !ok {
		return "Noma'lum maydon"
	}

	var (
		locale, slug, title string
		err                 error
	)

	switch f.column {
	case "tags":
		err = b.pool.QueryRow(ctx, `
			UPDATE posts SET tags = $2::text[], updated_at = now()
			WHERE id = $1::uuid
			RETURNING locale, slug, title`, postID, splitTags(value),
		).Scan(&locale, &slug, &title)

	case "slug":
		clean := slugify(value)
		if clean == "" {
			return "Slug bo'sh bo'lib qoldi — lotin harflar yoki raqamlar kerak."
		}
		err = b.pool.QueryRow(ctx, `
			UPDATE posts SET slug = $2, updated_at = now()
			WHERE id = $1::uuid
			RETURNING locale, slug, title`, postID, clean,
		).Scan(&locale, &slug, &title)

	case "translation_key":
		key := strings.TrimSpace(value)
		if key == "-" {
			key = "" // the documented way to clear it
		}
		err = b.pool.QueryRow(ctx, `
			UPDATE posts SET translation_key = $2, updated_at = now()
			WHERE id = $1::uuid
			RETURNING locale, slug, title`, postID, key,
		).Scan(&locale, &slug, &title)

	default:
		err = b.pool.QueryRow(ctx, fmt.Sprintf(`
			UPDATE posts SET %[1]s = $2, updated_at = now()
			WHERE id = $1::uuid
			RETURNING locale, slug, title`, f.column), postID, value,
		).Scan(&locale, &slug, &title)
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return "Post topilmadi — o'chirilgan bo'lishi mumkin."
	}
	if err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}

	return fmt.Sprintf("✅ <b>%s yangilandi</b>\n\n%s\n<code>%s/%s</code>\n\n<code>/tahrir %s</code>",
		f.label, escapeHTML(title), escapeHTML(locale), escapeHTML(slug), escapeHTML(slug))
}

/* --------------------------- translation pair ----------------------------- */

// createTranslationPair opens the sibling draft: same translation key, other
// locale, title copied so there is something to edit rather than an empty row.
//
// This is where the translation_key migration finally pays: linking the two
// languages used to be a dashboard chore, and now it is one button.
func (b *Bot) createTranslationPair(ctx context.Context, postID string) string {
	p, err := b.findPost(ctx, postID)
	if err != nil {
		return "Post topilmadi"
	}
	if p.hasSibling {
		return "Juftligi allaqachon bor"
	}

	key := p.translationKey
	if key == "" {
		// A post that was never linked gets its key from its own slug: stable,
		// readable, and already unique within the locale.
		key = p.slug
		if _, err := b.pool.Exec(ctx,
			`UPDATE posts SET translation_key = $2, updated_at = now() WHERE id = $1::uuid`,
			p.id, key); err != nil {
			return "❌ Kalit saqlanmadi: " + escapeHTML(shortError(err))
		}
	}

	other := "en"
	if p.locale != "uz" {
		other = "uz"
	}

	var newID, newSlug string
	err = b.pool.QueryRow(ctx, `
		INSERT INTO posts (locale, slug, title, description, body, tags,
		                   draft, content_date, translation_key)
		VALUES ($1, $2, $3, '', '', '{}', true, CURRENT_DATE, $4)
		RETURNING id::text, slug`,
		other, p.slug, p.title, key,
	).Scan(&newID, &newSlug)
	if err != nil {
		return "❌ Yaratilmadi: " + escapeHTML(shortError(err))
	}

	return fmt.Sprintf("🔗 <b>%s qoralama yaratildi</b>\n\n<code>%s/%s</code>\n\n<code>/tahrir %s/%s</code>",
		strings.ToUpper(other), escapeHTML(other), escapeHTML(newSlug),
		escapeHTML(other), escapeHTML(newSlug))
}

/* -------------------------------- helpers --------------------------------- */

// Short keys keep callback_data inside Telegram's 64 bytes with room to spare.
var fieldKeys = map[string]string{
	"post.title":           "t",
	"post.body":            "b",
	"post.description":     "d",
	"post.tags":            "g",
	"post.slug":            "s",
	"post.translation_key": "k",
}

func shortKey(intent string) string { return fieldKeys[intent] }

func intentForKey(key string) string {
	for intent, k := range fieldKeys {
		if k == key {
			return intent
		}
	}
	return ""
}

func splitTags(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (b *Bot) dashboardURL(postID string) string {
	return strings.TrimSuffix(b.cfg.AdminSiteURL, "/") + "/posts/" + postID
}

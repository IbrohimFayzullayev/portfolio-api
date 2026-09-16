package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// /xatolar and /xato — the reading half of the API's error_groups table.
//
// Read directly from PostgreSQL, like everything else here, and for the same
// reason: the moment you most want to ask what is broken is the moment the API
// cannot tell you.

const (
	errorsListLimit = 8
	// A day is the useful horizon for "what is wrong right now". Anything
	// older belongs to a conversation about trends, not to a phone screen.
	errorsWindow = 24 * time.Hour
)

type errorGroup struct {
	fingerprint string
	route       string
	message     string
	stack       string
	count       int64
	firstSeen   time.Time
	lastSeen    time.Time
	mutedUntil  *time.Time
}

func (g errorGroup) muted(now time.Time) bool {
	return g.mutedUntil != nil && g.mutedUntil.After(now)
}

/* ------------------------------- /xatolar -------------------------------- */

func (b *Bot) sendErrorsList(ctx context.Context, chatID int64) {
	text, markup := b.errorsList(ctx)
	if err := b.tg.sendMessageMarkup(ctx, chatID, text, markup); err != nil {
		log.Printf("bot: sendErrorsList failed: %v", err)
	}
}

// errorsList renders the message and its keyboard together, so a mute press
// can redraw both in place and they cannot drift apart.
func (b *Bot) errorsList(ctx context.Context) (string, *inlineKeyboard) {
	groups, err := b.recentErrors(ctx)
	if err != nil {
		return "❌ Xatolarni o'qib bo'lmadi: " + escapeHTML(shortError(err)), nil
	}
	if len(groups) == 0 {
		return "✅ Oxirgi 24 soatda xato yo'q.", nil
	}

	now := time.Now()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>Xatolar · oxirgi 24 soat</b> (%d guruh)\n", len(groups)))

	rows := make([][]inlineButton, 0, len(groups))
	for _, g := range groups {
		mark := "🔥"
		action := "🔕 Jimlatish"
		if g.muted(now) {
			mark = "🔇"
			action = "🔔 Ovozini qaytarish"
		}

		sb.WriteString(fmt.Sprintf("\n%s <b>×%d</b> · %s\n<code>%s</code>\n%s\n<code>/xato %s</code>\n",
			mark, g.count,
			g.lastSeen.In(tashkent).Format("02.01 15:04"),
			escapeHTML(g.route),
			escapeHTML(truncate(g.message, 140)),
			g.fingerprint))

		rows = append(rows, []inlineButton{{
			// "mute:" + 12 hex characters — 17 bytes of Telegram's 64.
			Text:         fmt.Sprintf("%s — %s", action, truncate(g.route, 18)),
			CallbackData: "mute:" + g.fingerprint,
		}})
	}

	return sb.String(), &inlineKeyboard{InlineKeyboard: rows}
}

func (b *Bot) recentErrors(ctx context.Context) ([]errorGroup, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT fingerprint, route, message, count, first_seen, last_seen, muted_until
		FROM error_groups
		WHERE last_seen > now() - make_interval(secs => $1)
		ORDER BY last_seen DESC
		LIMIT $2`, errorsWindow.Seconds(), errorsListLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []errorGroup
	for rows.Next() {
		var g errorGroup
		if err := rows.Scan(&g.fingerprint, &g.route, &g.message, &g.count,
			&g.firstSeen, &g.lastSeen, &g.mutedUntil); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

/* --------------------------------- /xato --------------------------------- */

// errorDetail is the one place the stack trace is shown. The list deliberately
// does not carry it: eight stacks in one message is a wall nobody reads.
func (b *Bot) errorDetail(ctx context.Context, fingerprint string) string {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return "Qaysi xato?\n\n<code>/xato &lt;id&gt;</code> — id ni /xatolar ro'yxatidan oling."
	}

	var g errorGroup
	err := b.pool.QueryRow(ctx, `
		SELECT fingerprint, route, message, sample_stack, count,
		       first_seen, last_seen, muted_until
		FROM error_groups
		WHERE fingerprint = $1`, fingerprint,
	).Scan(&g.fingerprint, &g.route, &g.message, &g.stack, &g.count,
		&g.firstSeen, &g.lastSeen, &g.mutedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return "Bunday xato guruhi yo'q: <code>" + escapeHTML(fingerprint) + "</code>"
	}
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔥 <b>Xato</b> <code>%s</code>\n\n", escapeHTML(g.fingerprint)))
	sb.WriteString(fmt.Sprintf("<code>%s</code>\n%s\n\n", escapeHTML(g.route), escapeHTML(g.message)))
	sb.WriteString(fmt.Sprintf("Jami: <b>%d</b>\nBirinchi: %s\nOxirgi: %s\n",
		g.count,
		g.firstSeen.In(tashkent).Format("02.01 15:04"),
		g.lastSeen.In(tashkent).Format("02.01 15:04")))
	if g.muted(time.Now()) {
		sb.WriteString(fmt.Sprintf("🔇 Jim: %s gacha\n",
			g.mutedUntil.In(tashkent).Format("02.01 15:04")))
	}
	if g.stack != "" {
		sb.WriteString("\n<pre>" + escapeHTML(truncate(g.stack, 1200)) + "</pre>")
	}
	return sb.String()
}

/* ------------------------------ mute button ------------------------------ */

// muteError toggles. One button that both silences and restores is fewer
// buttons to read, and the state is already visible in the row above it.
func (b *Bot) muteError(ctx context.Context, fingerprint string) string {
	if fingerprint == "" {
		return "Noma'lum xato"
	}

	var mutedUntil *time.Time
	err := b.pool.QueryRow(ctx, `
		UPDATE error_groups
		SET muted_until = CASE
		        WHEN muted_until IS NOT NULL AND muted_until > now() THEN NULL
		        ELSE now() + make_interval(secs => $2)
		    END
		WHERE fingerprint = $1
		RETURNING muted_until`, fingerprint, errorsWindow.Seconds(),
	).Scan(&mutedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return "Xato topilmadi"
	}
	if err != nil {
		return "Xato: " + shortError(err)
	}

	if mutedUntil == nil {
		return "Ovozi qaytarildi"
	}
	return "24 soatga jimlatildi"
}

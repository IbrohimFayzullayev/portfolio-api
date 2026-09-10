package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// The outbox: the bot's side of the notifications table.
//
// The API writes rows; this drains them. Nothing here talks to the API, so a
// notification queued while the API was healthy still goes out after the API
// has fallen over — which is precisely when you want to hear about it.

const (
	outboxInterval = 10 * time.Second
	outboxBatch    = 20
	// After this many failures a row is parked. Telegram rejecting one message
	// (bad HTML, a chat the bot was removed from) must not wedge the queue
	// behind it forever.
	maxAttempts = 5
)

type notification struct {
	id      int64
	kind    string
	payload map[string]any
}

// runOutbox drains pending notifications until ctx is cancelled.
func (b *Bot) runOutbox(ctx context.Context) {
	ticker := time.NewTicker(outboxInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.drainOutbox(ctx)
		}
	}
}

func (b *Bot) drainOutbox(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	pending, err := b.pendingNotifications(cctx)
	if err != nil {
		// The database being unreachable is worth one log line, not a crash:
		// the next tick tries again.
		log.Printf("bot: outbox read failed: %v", err)
		return
	}

	for _, n := range pending {
		text := b.renderNotification(n)
		if text == "" {
			// An unknown kind is not an error — a newer API may queue kinds
			// this build has never heard of. Mark it delivered so it does not
			// come round again on every tick.
			b.markSent(cctx, n.id)
			continue
		}

		if err := b.tg.sendMessage(cctx, b.cfg.AllowedUserID, text); err != nil {
			log.Printf("bot: outbox send %d (%s) failed: %v", n.id, n.kind, err)
			b.markFailed(cctx, n.id, err)
			continue
		}
		b.markSent(cctx, n.id)
	}
}

func (b *Bot) pendingNotifications(ctx context.Context) ([]notification, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT id, kind, payload
		FROM notifications
		WHERE sent_at IS NULL AND attempts < $1
		ORDER BY created_at
		LIMIT $2`, maxAttempts, outboxBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []notification
	for rows.Next() {
		var (
			n   notification
			raw []byte
		)
		if err := rows.Scan(&n.id, &n.kind, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &n.payload); err != nil {
			// A row we cannot parse is a row we can never send.
			log.Printf("bot: outbox row %d has unreadable payload: %v", n.id, err)
			n.payload = map[string]any{}
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (b *Bot) markSent(ctx context.Context, id int64) {
	_, err := b.pool.Exec(ctx,
		`UPDATE notifications SET sent_at = now(), attempts = attempts + 1 WHERE id = $1`, id)
	if err != nil {
		// Worst case the message is delivered twice on the next tick, which is
		// better than losing it.
		log.Printf("bot: could not mark notification %d sent: %v", id, err)
	}
}

func (b *Bot) markFailed(ctx context.Context, id int64, cause error) {
	_, err := b.pool.Exec(ctx,
		`UPDATE notifications SET attempts = attempts + 1, last_error = $2 WHERE id = $1`,
		id, shortError(cause))
	if err != nil {
		log.Printf("bot: could not record failure for notification %d: %v", id, err)
	}
}

/* ------------------------------ rendering -------------------------------- */

// str reads a string field, tolerating a missing or wrongly typed value —
// payloads come from a table, and an older or newer API may write a shape this
// build does not expect.
func str(payload map[string]any, key string) string {
	if v, ok := payload[key].(string); ok {
		return v
	}
	return ""
}

// renderNotification turns one row into a Telegram message, or "" for a kind
// this build does not know about.
func (b *Bot) renderNotification(n notification) string {
	switch n.kind {
	case "invitation.created":
		var sb strings.Builder
		sb.WriteString("💌 <b>Yangi taklifnoma</b>\n")
		if d := str(n.payload, "date"); d != "" {
			sb.WriteString(fmt.Sprintf("\n📅 %s", escapeHTML(d)))
			if t := str(n.payload, "time"); t != "" {
				sb.WriteString(" · " + escapeHTML(t))
			}
			sb.WriteString("\n")
		}
		if label := str(n.payload, "food_label"); label != "" {
			sb.WriteString(fmt.Sprintf("%s %s\n",
				str(n.payload, "food_emoji"), escapeHTML(label)))
		}
		if label := str(n.payload, "place_label"); label != "" {
			sb.WriteString(fmt.Sprintf("%s %s\n",
				str(n.payload, "place_emoji"), escapeHTML(label)))
		}
		sb.WriteString("\n/invitations")
		return sb.String()

	case "post.published":
		title := str(n.payload, "title")
		locale := str(n.payload, "locale")
		slug := str(n.payload, "slug")
		url := ""
		if locale != "" && slug != "" {
			url = b.publicURL(fmt.Sprintf("/%s/blog/%s", locale, slug))
		}
		return fmt.Sprintf("📝 <b>Post e'lon qilindi</b>\n\n%s\n%s",
			escapeHTML(title), escapeHTML(url))

	case "deploy.finished":
		service := str(n.payload, "service")
		sha := str(n.payload, "sha")
		if len(sha) > 7 {
			sha = sha[:7]
		}
		return fmt.Sprintf("🚀 <b>Deploy</b>\n\n%s · <code>%s</code>",
			escapeHTML(service), escapeHTML(sha))

	case "health.down":
		return fmt.Sprintf("🔴 <b>Servis javob bermayapti</b>\n\n%s\n%s",
			escapeHTML(str(n.payload, "label")),
			escapeHTML(str(n.payload, "detail")))

	case "health.recovered":
		return fmt.Sprintf("🟢 <b>Servis tiklandi</b>\n\n%s",
			escapeHTML(str(n.payload, "label")))
	}
	return ""
}

// publicURL builds links here rather than storing them in the payload, so a
// change of domain does not leave queued rows pointing at the old one.
func (b *Bot) publicURL(path string) string {
	return strings.TrimSuffix(b.cfg.PublicSiteURL, "/") + path
}

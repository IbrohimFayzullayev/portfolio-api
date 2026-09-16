package bot

import (
	"context"
	"encoding/json"
	"errors"
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

	// Telegram allows roughly one message per second to a single chat. A batch
	// of twenty fired back to back is the surest way to be told to slow down,
	// so the bot paces itself instead of learning it from a 429.
	sendPause = 350 * time.Millisecond

	// Ceiling for the retry backoff. Past this, waiting longer only delays the
	// recovery without sparing anyone anything.
	maxRetryDelay = 30 * time.Minute
)

// How loudly a message is allowed to arrive. Only critical is worth a phone
// lighting up at 03:00; everything else can wait for the morning summary.
const (
	sevInfo     = 0
	sevWarning  = 1
	sevCritical = 2
)

// Quiet hours, in Asia/Tashkent wall-clock terms: nothing below critical is
// delivered from 23:00 until 08:00, it is held for 09:00 instead — the same
// hour the daily summary goes out, so the morning is one burst rather than a
// trickle.
const (
	quietFromHour    = 23
	quietUntilHour   = 8
	quietReleaseHour = 9
)

type notification struct {
	id       int64
	kind     string
	payload  map[string]any
	attempts int
	severity int
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

	for i, n := range pending {
		// Pace the batch. The first message goes immediately; the rest are
		// spaced out so a backlog drains steadily instead of in a burst.
		if i > 0 {
			select {
			case <-cctx.Done():
				return
			case <-time.After(sendPause):
			}
		}

		// A routine message that became due in the middle of the night waits
		// for the morning. The check is here rather than at the INSERT so that
		// every writer — the API, the CI job, the bot itself — gets the same
		// policy without knowing it exists.
		if until, quiet := quietHoursDefer(time.Now().In(tashkent), severityOf(n)); quiet {
			b.deferUntil(cctx, n.id, until)
			continue
		}

		text := b.renderNotification(n)
		if text == "" {
			// An unknown kind is not an error — a newer API may queue kinds
			// this build has never heard of. Mark it delivered so it does not
			// come round again on every tick.
			b.markSent(cctx, n.id)
			continue
		}

		if err := b.tg.sendMessage(cctx, b.cfg.notifyTarget(), text); err != nil {
			log.Printf("bot: outbox send %d (%s) failed: %v", n.id, n.kind, err)
			b.recordFailure(cctx, n, err)
			continue
		}
		b.markSent(cctx, n.id)
	}
}

func (b *Bot) pendingNotifications(ctx context.Context) ([]notification, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT id, kind, payload, attempts, severity
		FROM notifications
		WHERE sent_at IS NULL
		  AND attempts < $1
		  AND (deliver_after IS NULL OR deliver_after <= now())
		ORDER BY COALESCE(deliver_after, created_at)
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
		if err := rows.Scan(&n.id, &n.kind, &raw, &n.attempts, &n.severity); err != nil {
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

// recordFailure decides what a failed send means for the row.
//
// Before this existed every failure cost an attempt, so five network blips
// spread over a week were enough to park a message that was never wrong. The
// three cases are genuinely different and are now treated that way.
func (b *Bot) recordFailure(ctx context.Context, n notification, cause error) {
	var te *telegramError

	switch {
	case errors.As(cause, &te) && te.RetryAfter > 0:
		// Telegram asked for exactly this much patience. Obeying it is not a
		// failed attempt — the message was never rejected.
		b.holdFor(ctx, n.id, te.RetryAfter, cause)

	case errors.As(cause, &te) && te.permanent():
		// Malformed HTML, a blocked bot, a chat that no longer exists: the
		// fifth attempt will fail exactly like the first. Park it now and keep
		// the reason.
		b.park(ctx, n.id, cause)

	default:
		// Network trouble, a 5xx, a timeout. Worth another try, but not
		// immediately.
		b.retryLater(ctx, n, cause)
	}
}

// deferUntil holds a row back to a specific moment without spending an attempt.
func (b *Bot) deferUntil(ctx context.Context, id int64, until time.Time) {
	_, err := b.pool.Exec(ctx,
		`UPDATE notifications SET deliver_after = $2 WHERE id = $1`, id, until)
	if err != nil {
		log.Printf("bot: could not defer notification %d: %v", id, err)
	}
}

func (b *Bot) holdFor(ctx context.Context, id int64, d time.Duration, cause error) {
	_, err := b.pool.Exec(ctx, `
		UPDATE notifications
		SET deliver_after = now() + make_interval(secs => $2),
		    last_error    = $3
		WHERE id = $1`, id, d.Seconds(), shortError(cause))
	if err != nil {
		log.Printf("bot: could not hold notification %d: %v", id, err)
	}
}

func (b *Bot) retryLater(ctx context.Context, n notification, cause error) {
	delay := retryDelay(n.attempts + 1)
	_, err := b.pool.Exec(ctx, `
		UPDATE notifications
		SET attempts      = attempts + 1,
		    deliver_after = now() + make_interval(secs => $2),
		    last_error    = $3
		WHERE id = $1`, n.id, delay.Seconds(), shortError(cause))
	if err != nil {
		log.Printf("bot: could not schedule retry for notification %d: %v", n.id, err)
	}
}

func (b *Bot) park(ctx context.Context, id int64, cause error) {
	_, err := b.pool.Exec(ctx,
		`UPDATE notifications SET attempts = $2, last_error = $3 WHERE id = $1`,
		id, maxAttempts, shortError(cause))
	if err != nil {
		log.Printf("bot: could not park notification %d: %v", id, err)
	}
}

/* ------------------------- scheduling arithmetic ------------------------- */

// retryDelay backs off exponentially from one minute, capped. Attempt numbers
// start at 1: 1m, 2m, 4m, 8m, 16m.
func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 20 { // guard the shift, not a real case
		attempt = 20
	}
	d := time.Duration(1<<uint(attempt-1)) * time.Minute
	if d > maxRetryDelay {
		return maxRetryDelay
	}
	return d
}

// quietHoursDefer reports whether a message should wait, and until when.
// Pure on purpose: the interesting cases are all about the clock, and this way
// they can be tested without a database or a Telegram token.
func quietHoursDefer(now time.Time, severity int) (time.Time, bool) {
	if severity >= sevCritical {
		return time.Time{}, false
	}

	y, m, d := now.Date()
	release := func(dayOffset int) time.Time {
		return time.Date(y, m, d+dayOffset, quietReleaseHour, 0, 0, 0, now.Location())
	}

	switch h := now.Hour(); {
	case h >= quietFromHour:
		return release(1), true // late evening → tomorrow morning
	case h < quietUntilHour:
		return release(0), true // small hours → this morning
	default:
		return time.Time{}, false
	}
}

// defaultSeverity is the severity a kind gets when whoever queued it did not
// say — an older API build, the CI job's psql one-liner, a kind added later.
func defaultSeverity(kind string) int {
	switch kind {
	case "health.down", "error.panic", "error.rate",
		"cert.expiring", "backup.missing", "host.alert":
		return sevCritical
	case "error.api":
		// Loud enough to be seen in the morning, not loud enough to be worth
		// waking up for: one failing query is not an outage, and the rate
		// watch is what notices when it is.
		return sevWarning
	case "health.recovered":
		// Deliberately critical too, even though good news never woke anyone.
		// A recovery held until 09:00 while the outage that caused it arrived
		// at 02:00 would leave the night looking like the service is still
		// down: the pair has to travel together or not at all.
		return sevCritical
	}
	return sevInfo
}

// severityOf prefers what the row says and falls back to the kind's default,
// so a row written before this column existed still behaves sensibly.
func severityOf(n notification) int {
	if n.severity > sevInfo {
		return n.severity
	}
	return defaultSeverity(n.kind)
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

// num reads a numeric field. Counts arrive as JSON numbers, which decode into
// float64 whatever the API meant to write.
func num(payload map[string]any, key string) int64 {
	switch v := payload[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

// venueLine renders the exact venue under the place category, marking the ones
// the visitor typed themselves — a name off the list is a known place, a name
// they wrote is one you may have to look up.
func venueLine(payload map[string]any) string {
	name := str(payload, "venue_name")
	if name == "" {
		return ""
	}
	line := "📌 " + escapeHTML(name)
	if custom, ok := payload["venue_custom"].(bool); ok && custom {
		line += " <i>(o'zi yozgan)</i>"
	}
	return line + "\n"
}

// renderNotification turns one row into a Telegram message, or "" for a kind
// this build does not know about.
func (b *Bot) renderNotification(n notification) string {
	switch n.kind {
	case "invitation.created":
		var sb strings.Builder
		sb.WriteString("💌 <b>Yangi taklifnoma</b>\n")
		if name := str(n.payload, "guest_name"); name != "" {
			sb.WriteString("\n👤 " + escapeHTML(name))
		}
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
		sb.WriteString(venueLine(n.payload))
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

	case "daily.summary":
		// Already rendered when it was queued — see dailySummary.
		return str(n.payload, "text")

	case "health.recovered":
		return fmt.Sprintf("🟢 <b>Servis tiklandi</b>\n\n%s",
			escapeHTML(str(n.payload, "label")))

	case "error.api":
		// The count is what happened SINCE the last message about this group,
		// not the total — the total is one /xato away and would only make the
		// number look alarming on a long-standing, already-known error.
		return fmt.Sprintf("🔥 <b>Xato</b> ×%d\n\n<code>%s</code>\n%s\n\n<code>/xato %s</code>",
			num(n.payload, "count"),
			escapeHTML(str(n.payload, "route")),
			escapeHTML(truncate(str(n.payload, "message"), 300)),
			escapeHTML(str(n.payload, "fingerprint")))

	case "error.panic":
		return fmt.Sprintf("💥 <b>Panic</b> ×%d\n\n<code>%s</code>\n%s\n\n<code>/xato %s</code>",
			num(n.payload, "count"),
			escapeHTML(str(n.payload, "route")),
			escapeHTML(truncate(str(n.payload, "message"), 300)),
			escapeHTML(str(n.payload, "fingerprint")))

	case "invitation.reminder":
		var sb strings.Builder
		switch str(n.payload, "when") {
		case "2h":
			sb.WriteString("⏰ <b>Uchrashuvga 2 soat</b>\n")
		case "24h":
			sb.WriteString("⏰ <b>Uchrashuvga bir kun</b>\n")
		default:
			sb.WriteString("⏰ <b>Ertaga uchrashuv</b>\n")
		}
		if name := str(n.payload, "guest_name"); name != "" {
			sb.WriteString("\n👤 " + escapeHTML(name))
		}
		sb.WriteString("\n📅 " + escapeHTML(str(n.payload, "date")))
		if t := str(n.payload, "time"); t != "" {
			sb.WriteString(" · " + escapeHTML(t))
		}
		sb.WriteString("\n")
		if label := str(n.payload, "food_label"); label != "" {
			sb.WriteString(fmt.Sprintf("%s %s\n",
				str(n.payload, "food_emoji"), escapeHTML(label)))
		}
		if label := str(n.payload, "place_label"); label != "" {
			sb.WriteString(fmt.Sprintf("%s %s\n",
				str(n.payload, "place_emoji"), escapeHTML(label)))
		}
		sb.WriteString(venueLine(n.payload))
		return sb.String()

	case "reminder":
		return "⏰ <b>Eslatma</b>\n\n" + escapeHTML(str(n.payload, "text"))

	case "cert.expiring":
		days := num(n.payload, "days")
		head := fmt.Sprintf("🔐 <b>Sertifikat %d kundan keyin tugaydi</b>", days)
		if days <= 0 {
			head = "🔐 <b>Sertifikat muddati tugagan</b>"
		}
		return fmt.Sprintf("%s\n\n%s · %s\n\n<i>Caddy o'zi yangilaydi — demak yangilash ishlamayapti.</i>",
			head, escapeHTML(str(n.payload, "host")), escapeHTML(str(n.payload, "until")))

	case "backup.finished":
		return fmt.Sprintf("💾 <b>Backup</b>\n\n%s · %s",
			escapeHTML(str(n.payload, "size")), escapeHTML(str(n.payload, "duration")))

	case "backup.missing":
		return fmt.Sprintf("💾 <b>Backup yo'q</b>\n\nOxirgisi: %s (%d soat oldin)",
			escapeHTML(str(n.payload, "last")), num(n.payload, "hours"))

	case "host.metrics":
		return fmt.Sprintf("🖥 <b>Server</b>\n\nDisk: %s\nXotira: %s\nYuk: %s",
			escapeHTML(str(n.payload, "disk")),
			escapeHTML(str(n.payload, "memory")),
			escapeHTML(str(n.payload, "load")))

	case "weekly.summary":
		return str(n.payload, "text")

	case "error.rate":
		return fmt.Sprintf("⚠️ <b>Xatolar to'lqini</b>\n\n%d daqiqada %d ta 5xx javob.\n\n/xatolar",
			num(n.payload, "minutes"), num(n.payload, "count"))
	}
	return ""
}

// publicURL builds links here rather than storing them in the payload, so a
// change of domain does not leave queued rows pointing at the old one.
func (b *Bot) publicURL(path string) string {
	return strings.TrimSuffix(b.cfg.PublicSiteURL, "/") + path
}

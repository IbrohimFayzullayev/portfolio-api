package bot

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The daily summary.
//
// One message a morning. The database numbers are the backbone — they cannot
// fail for an interesting reason — and Umami's are added when Umami answers.
// The original rule still holds and is enforced in umami.go: if traffic cannot
// be read, the section is left out rather than printed as zeros. A summary that
// says "0 views" because an endpoint moved looks like a bad day, and you act on
// it.

// 09:00 Asia/Tashkent. The zone database is embedded in the image (cmd/bot
// imports time/tzdata), so this is correct without tzdata installed in alpine.
const dailyHour = 9

func (b *Bot) runDailySummary(ctx context.Context) {
	for {
		wait := untilNext(time.Now().In(tashkent), dailyHour)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		b.queue(cctx, "daily.summary", map[string]any{"text": b.dailySummary(cctx)})
		cancel()
	}
}

// untilNext returns the duration to the next occurrence of `hour` local time.
// Computed from the wall clock each round rather than with a 24h ticker, so a
// daylight-saving change or a paused container cannot drift the send time.
func untilNext(now time.Time, hour int) time.Duration {
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(now)
}

// dailySummary is also what /kunlik answers with, so the scheduled message and
// the on-demand one can never disagree.
func (b *Bot) dailySummary(ctx context.Context) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>Kunlik hisobot</b> — %s\n",
		time.Now().In(tashkent).Format("02.01.2006")))

	var invToday, invTotal int64
	err := b.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE created_at >= date_trunc('day', now())),
			count(*)
		FROM invitations`).Scan(&invToday, &invTotal)
	if err != nil {
		sb.WriteString("\n💌 taklifnomalar: o'qib bo'lmadi\n")
	} else {
		sb.WriteString(fmt.Sprintf("\n💌 Taklifnoma: bugun %d · jami %d\n", invToday, invTotal))
	}

	var published, drafts int64
	err = b.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE NOT draft),
			count(*) FILTER (WHERE draft)
		FROM posts`).Scan(&published, &drafts)
	if err == nil {
		sb.WriteString(fmt.Sprintf("📝 Post: %d nashr · %d qoralama\n", published, drafts))
	}

	// Content with no counterpart in the other language — the number the
	// hreflang work exists to bring down.
	var unpaired int64
	if err := b.pool.QueryRow(ctx, `
		SELECT count(*) FROM posts
		WHERE NOT draft AND translation_key = ''`).Scan(&unpaired); err == nil && unpaired > 0 {
		sb.WriteString(fmt.Sprintf("🌐 Juftlanmagan post: %d\n", unpaired))
	}

	// A backlog here means the bot could not deliver something — worth seeing
	// in the same glance.
	var pending int64
	if err := b.pool.QueryRow(ctx, `
		SELECT count(*) FROM notifications
		WHERE sent_at IS NULL`).Scan(&pending); err == nil && pending > 0 {
		sb.WriteString(fmt.Sprintf("⚠️ Yuborilmagan xabar: %d\n", pending))
	}

	// Parked rows are worse than pending ones: nothing will retry them, and
	// without a line here they are invisible until someone reads the table.
	var parked int64
	if err := b.pool.QueryRow(ctx, `
		SELECT count(*) FROM notifications
		WHERE sent_at IS NULL AND attempts >= $1`, maxAttempts).Scan(&parked); err == nil && parked > 0 {
		sb.WriteString(fmt.Sprintf("🚫 Yuborilmaydigan xabar: %d\n", parked))
	}

	// Yesterday's errors, grouped. The count is groups, not occurrences: ten
	// thousand repeats of one broken query are one thing to fix.
	var errorGroups, errorEvents int64
	if err := b.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(count), 0) FROM error_groups
		WHERE last_seen > now() - interval '24 hours'
		  AND (muted_until IS NULL OR muted_until < now())`,
	).Scan(&errorGroups, &errorEvents); err == nil && errorGroups > 0 {
		sb.WriteString(fmt.Sprintf("🔥 Xato: %d guruh · %d marta — /xatolar\n",
			errorGroups, errorEvents))
	}

	// Anything queued to go out today, so the morning shows what the day
	// already has in it.
	var scheduled int64
	if err := b.pool.QueryRow(ctx, `
		SELECT count(*) FROM notifications
		WHERE sent_at IS NULL AND deliver_after IS NOT NULL
		  AND deliver_after < date_trunc('day', now()) + interval '1 day'`).Scan(&scheduled); err == nil && scheduled > 0 {
		sb.WriteString(fmt.Sprintf("⏰ Bugunga rejalashtirilgan: %d\n", scheduled))
	}

	// Traffic last, and only if Umami answers.
	now := time.Now().In(tashkent)
	sb.WriteString(b.trafficSection(ctx, period{
		label: "kun",
		from:  now.AddDate(0, 0, -1),
		to:    now,
	}))

	return sb.String()
}

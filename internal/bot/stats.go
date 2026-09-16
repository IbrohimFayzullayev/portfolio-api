package bot

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// /stat and the weekly report.
//
// Two layers, and the order matters. The database numbers are ours: they
// cannot fail for an interesting reason and they are always printed. Umami is
// someone else's service on the same box, so its section is printed only when
// it answers — see umami.go.
//
// Every figure is shown against the previous period of the same length. A bare
// "412 views" says nothing: whether that is good is entirely a question of
// what last week was, and a number nobody can act on is decoration.

type period struct {
	label string
	from  time.Time
	to    time.Time
}

// previous returns the window of the same length immediately before this one.
func (p period) previous() period {
	span := p.to.Sub(p.from)
	return period{label: p.label, from: p.from.Add(-span), to: p.from}
}

func periodFor(arg string, now time.Time) period {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "oy", "month":
		return period{label: "oy", from: now.AddDate(0, -1, 0), to: now}
	case "kun", "day":
		return period{label: "kun", from: now.AddDate(0, 0, -1), to: now}
	default:
		return period{label: "hafta", from: now.AddDate(0, 0, -7), to: now}
	}
}

/* --------------------------------- /stat --------------------------------- */

func (b *Bot) statsReport(ctx context.Context, arg string) string {
	now := time.Now().In(tashkent)
	p := periodFor(arg, now)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 <b>Statistika — %s</b>\n<i>%s – %s</i>\n",
		p.label, p.from.Format("02.01"), p.to.Format("02.01")))

	sb.WriteString(b.trafficSection(ctx, p))
	sb.WriteString(b.contentSection(ctx, p))

	return sb.String()
}

// trafficSection is empty when Umami is not configured or does not answer.
// Deliberately: zeros here would read as "nobody visited".
func (b *Bot) trafficSection(ctx context.Context, p period) string {
	if !b.umami.enabled() {
		return ""
	}

	current, err := b.umami.stats(ctx, p.from, p.to)
	if err != nil {
		return ""
	}

	// The previous window is fetched rather than read from Umami's own `prev`
	// field, which only exists in newer versions. One extra request buys
	// version independence.
	var previous umamiStats
	if prev, err := b.umami.stats(ctx, p.previous().from, p.previous().to); err == nil {
		previous = prev
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n👁 Ko'rish: <b>%d</b>%s\n",
		current.Pageviews.Value, delta(current.Pageviews.Value, previous.Pageviews.Value)))
	sb.WriteString(fmt.Sprintf("👤 Tashrifchi: <b>%d</b>%s\n",
		current.Visitors.Value, delta(current.Visitors.Value, previous.Visitors.Value)))

	if pages, err := b.umami.metrics(ctx, p.from, p.to, "url", 5); err == nil && len(pages) > 0 {
		sb.WriteString("\n<b>Sahifalar</b>\n")
		for _, m := range pages {
			sb.WriteString(fmt.Sprintf("%s — %d\n", escapeHTML(truncate(m.Name, 40)), m.Count))
		}
	}

	if refs, err := b.umami.metrics(ctx, p.from, p.to, "referrer", 5); err == nil && len(refs) > 0 {
		sb.WriteString("\n<b>Manbalar</b>\n")
		for _, m := range refs {
			name := m.Name
			if name == "" {
				name = "to'g'ridan-to'g'ri"
			}
			sb.WriteString(fmt.Sprintf("%s — %d\n", escapeHTML(truncate(name, 40)), m.Count))
		}
	}

	return sb.String()
}

// contentSection comes from our own database, so it is always printed.
func (b *Bot) contentSection(ctx context.Context, p period) string {
	var sb strings.Builder
	sb.WriteString("\n<b>Shu davrda</b>\n")

	var published, invitations, deploys, errorGroups int64

	_ = b.pool.QueryRow(ctx, `
		SELECT count(*) FROM posts
		WHERE NOT draft AND published_at >= $1 AND published_at < $2`,
		p.from, p.to).Scan(&published)

	_ = b.pool.QueryRow(ctx, `
		SELECT count(*) FROM invitations
		WHERE created_at >= $1 AND created_at < $2`,
		p.from, p.to).Scan(&invitations)

	_ = b.pool.QueryRow(ctx, `
		SELECT count(*) FROM notifications
		WHERE kind = 'deploy.finished' AND created_at >= $1 AND created_at < $2`,
		p.from, p.to).Scan(&deploys)

	_ = b.pool.QueryRow(ctx, `
		SELECT count(*) FROM error_groups
		WHERE last_seen >= $1 AND last_seen < $2`,
		p.from, p.to).Scan(&errorGroups)

	sb.WriteString(fmt.Sprintf("📝 Nashr: %d\n", published))
	sb.WriteString(fmt.Sprintf("💌 Taklifnoma: %d\n", invitations))
	sb.WriteString(fmt.Sprintf("🚀 Deploy: %d\n", deploys))
	if errorGroups > 0 {
		sb.WriteString(fmt.Sprintf("🔥 Xato guruhi: %d — /xatolar\n", errorGroups))
	}

	return sb.String()
}

// delta renders the change against the previous period. No previous data means
// no claim: "+100%" from a missing baseline is a lie with a decimal point.
func delta(current, previous int64) string {
	if previous <= 0 {
		return ""
	}
	change := float64(current-previous) / float64(previous) * 100
	switch {
	case change >= 0.5:
		return fmt.Sprintf(" <i>(+%.0f%%)</i>", change)
	case change <= -0.5:
		return fmt.Sprintf(" <i>(%.0f%%)</i>", change)
	default:
		return " <i>(≈)</i>"
	}
}

/* ----------------------------- weekly report ------------------------------ */

const (
	weeklyWeekday = time.Sunday
	weeklyHour    = 20
)

// runWeeklyReport sends one message a week: the same shape as /stat, at a time
// when there is space to read it.
func (b *Bot) runWeeklyReport(ctx context.Context) {
	for {
		wait := untilNextWeekly(time.Now().In(tashkent))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		text := "🗓 <b>Haftalik hisobot</b>\n" + b.statsReport(cctx, "hafta")
		b.queue(cctx, "weekly.summary", map[string]any{"text": text})
		cancel()
	}
}

// untilNextWeekly is computed from the wall clock every round, like the daily
// summary, so a stopped container cannot drift the schedule.
func untilNextWeekly(now time.Time) time.Duration {
	next := time.Date(now.Year(), now.Month(), now.Day(), weeklyHour, 0, 0, 0, now.Location())
	days := (int(weeklyWeekday) - int(now.Weekday()) + 7) % 7
	next = next.AddDate(0, 0, days)
	if !next.After(now) {
		next = next.AddDate(0, 0, 7)
	}
	return next.Sub(now)
}

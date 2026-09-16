package bot

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

var tashkent = mustLoadLocation("Asia/Tashkent")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

/* -------------------------------- /status -------------------------------- */

type checkResult struct {
	label   string
	ok      bool
	detail  string
	latency time.Duration
}

// statusReport checks the stack from two sides at once.
//
// Splitting external (through Caddy and TLS) from internal (straight to the
// container) is the whole value: if internal is green and external is red, the
// application is fine and the problem is Caddy, DNS or the certificate.
func (b *Bot) statusReport(ctx context.Context) string {
	external := []struct{ label, url string }{
		{"fayzullayev.uz", b.cfg.PublicSiteURL},
		{"admin.fayzullayev.uz", b.cfg.AdminSiteURL},
		{"api.fayzullayev.uz", strings.TrimSuffix(b.cfg.PublicAPIURL, "/") + "/healthz"},
	}

	results := make([]checkResult, len(external))
	var wg sync.WaitGroup
	for i, e := range external {
		wg.Add(1)
		go func(i int, label, url string) {
			defer wg.Done()
			results[i] = b.probe(ctx, label, url)
		}(i, e.label, e.url)
	}

	var internal checkResult
	wg.Add(1)
	go func() {
		defer wg.Done()
		internal = b.probe(ctx, "api:8080",
			strings.TrimSuffix(b.cfg.InternalAPIURL, "/")+"/healthz")
	}()

	var dbLine string
	wg.Add(1)
	go func() {
		defer wg.Done()
		dbLine = b.dbStatus(ctx)
	}()

	wg.Wait()

	var sb strings.Builder
	sb.WriteString("<b>Stack holati</b>\n\n")

	sb.WriteString("<b>Tashqi</b> — Caddy va TLS orqali\n")
	for _, r := range results {
		sb.WriteString(formatCheck(r))
	}

	sb.WriteString("\n<b>Ichki</b> — Docker tarmog'idan to'g'ridan-to'g'ri\n")
	sb.WriteString(formatCheck(internal))

	sb.WriteString("\n<b>Baza</b>\n")
	sb.WriteString(dbLine)

	allExternalDown := true
	for _, r := range results {
		if r.ok {
			allExternalDown = false
		}
	}
	if allExternalDown && internal.ok {
		sb.WriteString("\n<i>Ilova sog'lom, lekin tashqaridan ochilmayapti — " +
			"Caddy, DNS yoki sertifikatni tekshiring.</i>")
	}

	sb.WriteString(b.uptimeSection(ctx))

	sb.WriteString(fmt.Sprintf("\n\n<i>%s</i>", time.Now().In(tashkent).Format("02.01.2006 15:04")))
	return sb.String()
}

// uptimeSection answers the question /status could never answer before: not
// "is it up now" but "has it been". The watcher used to keep its verdict in
// memory, so an outage that healed before anyone looked left no trace.
func (b *Bot) uptimeSection(ctx context.Context) string {
	rows, err := b.pool.Query(ctx, `
		SELECT target,
		       count(*)                        AS total,
		       count(*) FILTER (WHERE ok)      AS good,
		       max(checked_at) FILTER (WHERE NOT ok) AS last_failure
		FROM health_checks
		WHERE checked_at > now() - interval '7 days'
		GROUP BY target
		ORDER BY target`)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var sb strings.Builder
	for rows.Next() {
		var (
			target      string
			total, good int64
			lastFailure *time.Time
		)
		if err := rows.Scan(&target, &total, &good, &lastFailure); err != nil {
			return ""
		}
		if total == 0 {
			continue
		}

		line := fmt.Sprintf("%s — %.1f%%", escapeHTML(target), float64(good)/float64(total)*100)
		if lastFailure != nil {
			line += fmt.Sprintf(" · oxirgi uzilish %s",
				lastFailure.In(tashkent).Format("02.01 15:04"))
		}
		sb.WriteString(line + "\n")
	}
	if sb.Len() == 0 {
		return ""
	}

	return "\n<b>7 kunlik uptime</b>\n" + sb.String()
}

func (b *Bot) probe(ctx context.Context, label, url string) checkResult {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return checkResult{label: label, detail: "noto'g'ri manzil"}
	}
	res, err := b.http.Do(req)
	if err != nil {
		return checkResult{label: label, detail: shortError(err), latency: time.Since(start)}
	}
	defer res.Body.Close()

	return checkResult{
		label:   label,
		ok:      res.StatusCode >= 200 && res.StatusCode < 400,
		detail:  fmt.Sprintf("HTTP %d", res.StatusCode),
		latency: time.Since(start),
	}
}

func (b *Bot) dbStatus(ctx context.Context) string {
	start := time.Now()
	if err := b.pool.Ping(ctx); err != nil {
		return "❌ ulanib bo'lmadi — " + escapeHTML(shortError(err)) + "\n"
	}
	ping := time.Since(start)

	var invitations int64
	var posts int64
	_ = b.pool.QueryRow(ctx, "SELECT count(*) FROM invitations").Scan(&invitations)
	_ = b.pool.QueryRow(ctx, "SELECT count(*) FROM posts WHERE draft = false").Scan(&posts)

	return fmt.Sprintf("✅ %s · %d taklifnoma · %d nashr etilgan post\n",
		humanMS(ping), invitations, posts)
}

func formatCheck(r checkResult) string {
	mark := "❌"
	if r.ok {
		mark = "✅"
	}
	if r.latency > 0 {
		return fmt.Sprintf("%s %s — %s, %s\n",
			mark, escapeHTML(r.label), escapeHTML(r.detail), humanMS(r.latency))
	}
	return fmt.Sprintf("%s %s — %s\n", mark, escapeHTML(r.label), escapeHTML(r.detail))
}

func humanMS(d time.Duration) string {
	return fmt.Sprintf("%d ms", d.Milliseconds())
}

// shortError keeps Telegram messages readable — Go's network errors carry the
// whole dial chain, which is noise on a phone.
func shortError(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i != -1 && len(s)-i < 60 {
		s = s[i+2:]
	}
	if len(s) > 90 {
		s = s[:90] + "…"
	}
	return s
}

/* ----------------------------- /invitations ------------------------------ */

func (b *Bot) invitationsReport(ctx context.Context) string {
	var total int64
	if err := b.pool.QueryRow(ctx, "SELECT count(*) FROM invitations").Scan(&total); err != nil {
		return "❌ Bazaga ulanib bo'lmadi: " + escapeHTML(shortError(err))
	}
	if total == 0 {
		return "📭 Hali birorta taklifnoma yo'q."
	}

	rows, err := b.pool.Query(ctx, `
		SELECT guest_name, event_date, event_time, food_emoji, food_label,
		       place_emoji, place_label, venue_name, venue_custom, created_at
		FROM invitations
		ORDER BY created_at DESC
		LIMIT 5`)
	if err != nil {
		return "❌ So'rov bajarilmadi: " + escapeHTML(shortError(err))
	}
	defer rows.Close()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>Oxirgi taklifnomalar</b> — jami %d\n", total))

	for rows.Next() {
		var (
			guestName                       string
			eventDate                       time.Time
			eventTime, foodEmoji, foodLabel string
			placeEmoji, placeLabel          string
			venueName                       string
			venueCustom                     bool
			createdAt                       time.Time
		)
		if err := rows.Scan(&guestName, &eventDate, &eventTime, &foodEmoji, &foodLabel,
			&placeEmoji, &placeLabel, &venueName, &venueCustom, &createdAt); err != nil {
			return "❌ Natijani o'qib bo'lmadi: " + escapeHTML(shortError(err))
		}

		// The name leads when there is one — it is the first thing you want to
		// know about an invitation, and the rows from before the field existed
		// simply do not have it.
		heading := createdAt.In(tashkent).Format("02.01 15:04")
		if guestName != "" {
			heading = escapeHTML(guestName) + " · " + heading
		}
		sb.WriteString(fmt.Sprintf("\n<b>%s</b>\n", heading))
		sb.WriteString(fmt.Sprintf("📅 %s · %s\n",
			eventDate.Format("2006-01-02"), escapeHTML(eventTime)))
		if foodLabel != "" {
			sb.WriteString(fmt.Sprintf("%s %s\n", foodEmoji, escapeHTML(foodLabel)))
		}
		if placeLabel != "" {
			sb.WriteString(fmt.Sprintf("%s %s\n", placeEmoji, escapeHTML(placeLabel)))
		}
		sb.WriteString(venueLine(map[string]any{
			"venue_name":   venueName,
			"venue_custom": venueCustom,
		}))
	}
	if err := rows.Err(); err != nil {
		return "❌ O'qishda xato: " + escapeHTML(shortError(err))
	}

	if total > 5 {
		sb.WriteString(fmt.Sprintf("\n<i>Qolgani adminkada: %s</i>",
			escapeHTML(strings.TrimSuffix(b.cfg.AdminSiteURL, "/")+"/invitations")))
	}
	return sb.String()
}

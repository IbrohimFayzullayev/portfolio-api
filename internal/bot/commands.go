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

	sb.WriteString(fmt.Sprintf("\n\n<i>%s</i>", time.Now().In(tashkent).Format("02.01.2006 15:04")))
	return sb.String()
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
		SELECT event_date, event_time, food_emoji, food_label,
		       place_emoji, place_label, created_at
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
			eventDate                       time.Time
			eventTime, foodEmoji, foodLabel string
			placeEmoji, placeLabel          string
			createdAt                       time.Time
		)
		if err := rows.Scan(&eventDate, &eventTime, &foodEmoji, &foodLabel,
			&placeEmoji, &placeLabel, &createdAt); err != nil {
			return "❌ Natijani o'qib bo'lmadi: " + escapeHTML(shortError(err))
		}

		sb.WriteString(fmt.Sprintf("\n<b>%s</b>\n", createdAt.In(tashkent).Format("02.01 15:04")))
		sb.WriteString(fmt.Sprintf("📅 %s · %s\n",
			eventDate.Format("2006-01-02"), escapeHTML(eventTime)))
		if foodLabel != "" {
			sb.WriteString(fmt.Sprintf("%s %s\n", foodEmoji, escapeHTML(foodLabel)))
		}
		if placeLabel != "" {
			sb.WriteString(fmt.Sprintf("%s %s\n", placeEmoji, escapeHTML(placeLabel)))
		}
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

package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The scheduler: the fourth goroutine, and the one that sends nothing.
//
// Its entire job is to notice that something is due and put a row in the
// outbox for it. Delivery, retries, quiet hours and rate limiting all stay
// where they already live. That division is deliberate: the moment a second
// piece of code is allowed to talk to Telegram, every rule the outbox enforces
// has to be re-implemented beside it, and one of the two copies will be wrong.
//
// Everything it writes carries a dedupe_key, so running the same minute twice
// — a restart, a clock nudge, a slow tick — queues nothing the second time.

const (
	schedulerInterval = time.Minute
	// How far ahead to look. Reminders are queued the moment the row appears,
	// not at the last second, so a bot that is down for an hour still delivers
	// them on time.
	scheduleHorizon = 60
)

func (b *Bot) runScheduler(ctx context.Context) {
	ticker := time.NewTicker(schedulerInterval)
	defer ticker.Stop()

	// Once at startup as well: a restart should not mean waiting a minute for
	// something that was already due.
	b.runSchedule(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.runSchedule(ctx)
		}
	}
}

func (b *Bot) runSchedule(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	b.scheduleInvitationReminders(cctx)
	b.publishDuePosts(cctx)
	b.watchBackups(cctx)
	b.scheduleRestoreTest(cctx)
	b.forgetExpiredPrompts(cctx)
	b.pruneHealthHistory(cctx)
}

/* -------------------------- invitation reminders -------------------------- */

type upcomingInvitation struct {
	id          string
	guestName   string
	date        time.Time
	timeText    string
	foodLabel   string
	foodEmoji   string
	placeLabel  string
	placeEmoji  string
	venueName   string
	venueCustom bool
}

// scheduleInvitationReminders queues the reminders for every upcoming
// invitation that does not have them yet.
func (b *Bot) scheduleInvitationReminders(ctx context.Context) {
	rows, err := b.pool.Query(ctx, `
		SELECT id::text, guest_name, event_date, event_time,
		       food_label, food_emoji, place_label, place_emoji,
		       venue_name, venue_custom
		FROM invitations
		WHERE event_date >= (now() AT TIME ZONE 'Asia/Tashkent')::date
		ORDER BY event_date
		LIMIT $1`, scheduleHorizon)
	if err != nil {
		log.Printf("bot: scheduler could not read invitations: %v", err)
		return
	}
	defer rows.Close()

	var upcoming []upcomingInvitation
	for rows.Next() {
		var inv upcomingInvitation
		if err := rows.Scan(&inv.id, &inv.guestName, &inv.date, &inv.timeText,
			&inv.foodLabel, &inv.foodEmoji, &inv.placeLabel, &inv.placeEmoji,
			&inv.venueName, &inv.venueCustom); err != nil {
			log.Printf("bot: scheduler could not scan invitation: %v", err)
			return
		}
		upcoming = append(upcoming, inv)
	}
	if err := rows.Err(); err != nil {
		log.Printf("bot: scheduler could not read invitations: %v", err)
		return
	}

	now := time.Now().In(tashkent)
	for _, inv := range upcoming {
		at, exact := eventMoment(inv.date, inv.timeText)

		if !exact {
			// The time is free text the visitor typed ("kechqurun", or
			// nothing at all). A two-hour warning for a moment nobody knows is
			// a guess dressed up as a fact, so there is one reminder instead:
			// the morning before.
			b.queueReminder(ctx, inv, "day", at, morningBefore(at), now)
			continue
		}

		b.queueReminder(ctx, inv, "24h", at, at.Add(-24*time.Hour), now)
		b.queueReminder(ctx, inv, "2h", at, at.Add(-2*time.Hour), now)
	}
}

// queueReminder writes one reminder row, unless it is already there or its
// moment has passed.
func (b *Bot) queueReminder(
	ctx context.Context, inv upcomingInvitation, when string,
	eventAt, deliverAt, now time.Time,
) {
	// A reminder whose moment has gone is not queued at all. An invitation
	// that arrives three hours before the event should produce the two-hour
	// warning and nothing else — not a "tomorrow" message about tonight.
	if !deliverAt.After(now) {
		return
	}

	b.queueScheduled(ctx, "invitation.reminder", map[string]any{
		"when":         when,
		"guest_name":   inv.guestName,
		"date":         eventAt.Format("02.01.2006"),
		"time":         inv.timeText,
		"food_label":   inv.foodLabel,
		"food_emoji":   inv.foodEmoji,
		"place_label":  inv.placeLabel,
		"place_emoji":  inv.placeEmoji,
		"venue_name":   inv.venueName,
		"venue_custom": inv.venueCustom,
	}, sevInfo, "inv-"+when+":"+inv.id, deliverAt)
}

/* --------------------------- scheduled publishing ------------------------- */

// publishDuePosts flips drafts whose moment has come.
//
// One statement does the whole thing, and the RETURNING clause is what makes
// it safe to run every minute from a process that might be restarted mid-flight:
// a row is only announced if this call is the one that changed it.
func (b *Bot) publishDuePosts(ctx context.Context) {
	rows, err := b.pool.Query(ctx, `
		UPDATE posts
		SET draft        = false,
		    published_at = COALESCE(published_at, now()),
		    publish_at   = NULL,
		    updated_at   = now()
		WHERE draft AND publish_at IS NOT NULL AND publish_at <= now()
		RETURNING locale, slug, title`)
	if err != nil {
		log.Printf("bot: scheduler could not publish due posts: %v", err)
		return
	}
	defer rows.Close()

	type published struct{ locale, slug, title string }
	var done []published
	for rows.Next() {
		var p published
		if err := rows.Scan(&p.locale, &p.slug, &p.title); err != nil {
			log.Printf("bot: scheduler could not scan published post: %v", err)
			return
		}
		done = append(done, p)
	}
	if err := rows.Err(); err != nil {
		log.Printf("bot: scheduler publish failed: %v", err)
		return
	}

	for _, p := range done {
		log.Printf("bot: published %s/%s on schedule", p.locale, p.slug)
		b.queue(ctx, "post.published", map[string]any{
			"locale": p.locale,
			"slug":   p.slug,
			"title":  p.title,
		})
	}
}

/* ------------------------------ backups ---------------------------------- */

// A backup is the one failure that is invisible in the ordinary run of things:
// nothing breaks when it does not happen, and you find out on the day you need
// it. So it is watched by ABSENCE — no row for more than a day means something
// is wrong, whether the script failed, was removed, or never ran.
const backupMaxAge = 26 * time.Hour

func (b *Bot) watchBackups(ctx context.Context) {
	var lastBackup *time.Time
	err := b.pool.QueryRow(ctx, `
		SELECT max(created_at) FROM notifications
		WHERE kind = 'backup.finished'`).Scan(&lastBackup)
	if err != nil {
		log.Printf("bot: could not check backups: %v", err)
		return
	}
	if lastBackup == nil {
		// Nothing has ever reported a backup. That is a setup state, not an
		// incident — saying it every minute from day one would train you to
		// ignore the message before the script even exists.
		return
	}
	if time.Since(*lastBackup) < backupMaxAge {
		return
	}

	b.queueScheduled(ctx, "backup.missing", map[string]any{
		"last":  lastBackup.In(tashkent).Format("02.01 15:04"),
		"hours": int(time.Since(*lastBackup).Hours()),
	}, sevCritical,
		// Once a day while it stays broken.
		"backup-missing:"+time.Now().In(tashkent).Format("2006-01-02"),
		time.Time{})
}

// scheduleRestoreTest queues one reminder a month. An untested backup is not a
// backup, and the only way that test happens is if something asks for it.
func (b *Bot) scheduleRestoreTest(ctx context.Context) {
	now := time.Now().In(tashkent)
	// The first of next month, in the morning batch.
	next := time.Date(now.Year(), now.Month(), 1, quietReleaseHour, 0, 0, 0, tashkent).AddDate(0, 1, 0)

	b.queueScheduled(ctx, "reminder", map[string]any{
		"text": "Oylik backup restore sinovi — zaxiradan haqiqiy tiklanishni tekshiring.",
	}, sevInfo, "restore-test:"+next.Format("2006-01"), next)
}

/* -------------------------------- queueing -------------------------------- */

// queueScheduled writes a row the outbox will pick up when its moment arrives.
// ON CONFLICT DO NOTHING against the partial unique index on dedupe_key is
// what makes running this every minute harmless.
func (b *Bot) queueScheduled(
	ctx context.Context, kind string, payload map[string]any,
	severity int, dedupeKey string, deliverAt time.Time,
) {
	raw, err := json.Marshal(payload)
	if err != nil {
		log.Printf("bot: could not marshal %s payload: %v", kind, err)
		return
	}

	var (
		key   *string
		after *time.Time
	)
	if dedupeKey != "" {
		key = &dedupeKey
	}
	if !deliverAt.IsZero() {
		after = &deliverAt
	}

	_, err = b.pool.Exec(ctx, `
		INSERT INTO notifications (kind, payload, severity, dedupe_key, deliver_after)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING`,
		kind, raw, severity, key, after)
	if err != nil {
		log.Printf("bot: could not queue %s: %v", kind, err)
	}
}

/* ------------------------------ time parsing ------------------------------ */

// event_time is free text from the invitation site — "19:00", "19.00",
// "soat 19 da". This finds a clock in it and gives up gracefully when there is
// none, which is the difference between a reminder that is right and one that
// is confidently wrong.
var clockPattern = regexp.MustCompile(`(\d{1,2})[:.](\d{2})`)

func parseEventTime(s string) (hour, minute int, ok bool) {
	m := clockPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(m[1])
	min, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil || h > 23 || min > 59 {
		return 0, 0, false
	}
	return h, min, true
}

// eventMoment combines the stored date with whatever time could be read from
// the free-text field. The bool says whether the clock is real or assumed.
func eventMoment(date time.Time, timeText string) (time.Time, bool) {
	y, m, d := date.Date()
	if h, min, ok := parseEventTime(timeText); ok {
		return time.Date(y, m, d, h, min, 0, 0, tashkent), true
	}
	return time.Date(y, m, d, 0, 0, 0, 0, tashkent), false
}

// morningBefore is 09:00 the day before — the same hour the daily summary
// goes out, so an unknown-time event lands in the morning batch.
func morningBefore(eventAt time.Time) time.Time {
	y, m, d := eventAt.Date()
	return time.Date(y, m, d-1, quietReleaseHour, 0, 0, 0, tashkent)
}

/* ------------------------------- /eslatma --------------------------------- */

var (
	datePattern  = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})(?:\.(\d{2,4}))?$`)
	clockOnlyPat = regexp.MustCompile(`^(\d{1,2})[:.](\d{2})$`)
)

// parseWhen reads a moment off the front of a command: "20.09 14:00",
// "20.09", "14:00". It returns what is left over, so the same parser serves
// /eslatma (the rest is the message) and /rejalash (there is no rest).
//
// A date alone means 09:00 that day; a time alone means today if it is still
// ahead and tomorrow if it is not.
func parseWhen(now time.Time, fields []string) (time.Time, []string, error) {
	if len(fields) == 0 {
		return time.Time{}, nil, fmt.Errorf("empty")
	}

	var (
		day, month, year = 0, 0, 0
		hour, minute     = -1, -1
		used             int
	)

	for _, f := range fields {
		if m := datePattern.FindStringSubmatch(f); m != nil && day == 0 {
			day, _ = strconv.Atoi(m[1])
			month, _ = strconv.Atoi(m[2])
			if m[3] != "" {
				year, _ = strconv.Atoi(m[3])
				if year < 100 {
					year += 2000
				}
			}
			used++
			continue
		}
		if m := clockOnlyPat.FindStringSubmatch(f); m != nil && hour < 0 {
			hour, _ = strconv.Atoi(m[1])
			minute, _ = strconv.Atoi(m[2])
			used++
			continue
		}
		break // the rest is the message
	}

	if day == 0 && hour < 0 {
		return time.Time{}, nil, fmt.Errorf("no date or time")
	}
	rest := fields[used:]

	if hour < 0 {
		hour, minute = quietReleaseHour, 0
	}
	if hour > 23 || minute > 59 {
		return time.Time{}, nil, fmt.Errorf("bad clock")
	}

	if day == 0 {
		// Time only: today if it is still ahead, otherwise tomorrow. The
		// alternative — refusing — is unhelpful at 23:50.
		at := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, tashkent)
		if !at.After(now) {
			at = at.AddDate(0, 0, 1)
		}
		return at, rest, nil
	}

	if year == 0 {
		year = now.Year()
	}
	if month < 1 || month > 12 || day > 31 {
		return time.Time{}, nil, fmt.Errorf("bad date")
	}
	at := time.Date(year, time.Month(month), day, hour, minute, 0, 0, tashkent)
	if at.Before(now) && year == now.Year() {
		// "05.01" typed in December means next January, not eleven months ago.
		at = at.AddDate(1, 0, 0)
	}
	if !at.After(now) {
		return time.Time{}, nil, fmt.Errorf("in the past")
	}
	return at, rest, nil
}

// createReminder is the /eslatma handler.
func (b *Bot) createReminder(ctx context.Context, input string) string {
	at, rest, err := parseWhen(time.Now().In(tashkent), strings.Fields(input))
	text := strings.TrimSpace(strings.Join(rest, " "))
	if err == nil && text == "" {
		err = fmt.Errorf("no text")
	}
	if err != nil {
		return strings.Join([]string{
			"Vaqt tushunarsiz.",
			"",
			"<code>/eslatma 20.09 14:00 backup restore sinovi</code>",
			"<code>/eslatma 20.09 oylik hisobot</code> — 09:00 da",
			"<code>/eslatma 18:30 kitob</code> — bugun yoki ertaga",
		}, "\n")
	}

	b.queueScheduled(ctx, "reminder", map[string]any{"text": text}, sevInfo, "", at)

	return fmt.Sprintf("⏰ <b>Eslatma qo'yildi</b>\n\n%s\n<i>%s</i>",
		escapeHTML(text), at.Format("02.01.2006 15:04"))
}

/* ------------------------------- /rejalash -------------------------------- */

// schedulePost is the /rejalash handler: "<ref> <sana> <vaqt>", where ref is a
// slug, "locale/slug", or a post id.
func (b *Bot) schedulePost(ctx context.Context, input string) string {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return b.scheduledPostsList(ctx)
	}
	if len(fields) < 2 {
		return "<code>/rejalash &lt;slug&gt; 20.09 10:00</code>"
	}

	ref := fields[0]
	at, _, err := parseWhen(time.Now().In(tashkent), fields[1:])
	if err != nil {
		return "Vaqt tushunarsiz.\n\n<code>/rejalash &lt;slug&gt; 20.09 10:00</code>"
	}

	var locale, slug, title string
	err = b.pool.QueryRow(ctx, `
		UPDATE posts
		SET publish_at = $2, updated_at = now()
		WHERE draft AND (
		        slug = $1
		     OR locale || '/' || slug = $1
		     OR ($1 ~ '^[0-9a-f-]{36}$' AND id = $1::uuid)
		    )
		RETURNING locale, slug, title`, ref, at,
	).Scan(&locale, &slug, &title)
	if err != nil {
		return "Qoralama topilmadi: <code>" + escapeHTML(ref) + "</code>\n\n/posts"
	}

	return fmt.Sprintf("🗓 <b>Rejalashtirildi</b>\n\n%s\n<code>%s/%s</code>\n<i>%s</i>",
		escapeHTML(title), escapeHTML(locale), escapeHTML(slug),
		at.Format("02.01.2006 15:04"))
}

func (b *Bot) scheduledPostsList(ctx context.Context) string {
	rows, err := b.pool.Query(ctx, `
		SELECT locale, slug, title, publish_at
		FROM posts
		WHERE draft AND publish_at IS NOT NULL
		ORDER BY publish_at`)
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}
	defer rows.Close()

	var sb strings.Builder
	count := 0
	for rows.Next() {
		var locale, slug, title string
		var at time.Time
		if err := rows.Scan(&locale, &slug, &title, &at); err != nil {
			return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
		}
		count++
		sb.WriteString(fmt.Sprintf("\n<b>%s</b>\n<code>%s/%s</code> · %s\n",
			escapeHTML(title), escapeHTML(locale), escapeHTML(slug),
			at.In(tashkent).Format("02.01 15:04")))
	}
	if count == 0 {
		return "Rejalashtirilgan post yo'q.\n\n<code>/rejalash &lt;slug&gt; 20.09 10:00</code>"
	}
	return fmt.Sprintf("<b>Rejalashtirilgan postlar</b> (%d)\n%s", count, sb.String())
}

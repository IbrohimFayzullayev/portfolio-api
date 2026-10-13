package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/growth"
)

// Personal growth: English (IELTS) practice in short sprints.
//
// The point of this file is to make progress that is otherwise invisible
// visible. Code gives a result the same evening; a language does not. So every
// session is one line (/qildim 45 w), every voice message is kept, the evening
// asks once if the day is still empty, and a sprint ends by playing the first
// and last recording back to back.
//
// The rules themselves — what counts, how a streak survives, when to nudge —
// live in internal/growth and are tested there. This is the glue.

const dateFmt = "2006-01-02"

/* --------------------------------- state ---------------------------------- */

type sprintRow struct {
	id     string
	goal   string
	starts time.Time
	ends   time.Time
}

type growthState struct {
	hasAny          bool
	todayMinutes    int
	streak          int
	missedYesterday bool
	sprint          *sprintRow
}

// tashkentDay turns a DATE read from PostgreSQL (midnight UTC) into the same
// calendar day in Tashkent.
func tashkentDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, tashkent)
}

// minutesByDay sums the logs per day from `from` on.
func (b *Bot) minutesByDay(ctx context.Context, from time.Time) (map[string]int, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT day, sum(minutes)::int FROM growth_logs
		WHERE day >= $1::date
		GROUP BY day`, from.Format(dateFmt))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var (
			d time.Time
			m int
		)
		if err := rows.Scan(&d, &m); err != nil {
			return nil, err
		}
		out[d.Format(dateFmt)] = m
	}
	return out, rows.Err()
}

func (b *Bot) openSprint(ctx context.Context) (*sprintRow, error) {
	var sp sprintRow
	err := b.pool.QueryRow(ctx, `
		SELECT id::text, goal, starts_on, ends_on FROM growth_sprints
		WHERE closed_at IS NULL`).Scan(&sp.id, &sp.goal, &sp.starts, &sp.ends)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	sp.starts, sp.ends = tashkentDay(sp.starts), tashkentDay(sp.ends)
	return &sp, nil
}

// growthNow reads everything the cards and the nudge need in one place.
func (b *Bot) growthNow(ctx context.Context, now time.Time) (growthState, error) {
	var st growthState
	today := dayStart(now)

	var first *time.Time
	if err := b.pool.QueryRow(ctx, `SELECT min(day) FROM growth_logs`).Scan(&first); err != nil {
		return st, err
	}
	if first != nil {
		st.hasAny = true
		firstDay := tashkentDay(*first)
		// A year back is far longer than any streak this will ever see broken
		// by the two-miss rule; it only bounds the query.
		from := today.AddDate(-1, 0, 0)
		if firstDay.After(from) {
			from = firstDay
		}
		days, err := b.minutesByDay(ctx, from)
		if err != nil {
			return st, err
		}
		st.todayMinutes = days[today.Format(dateFmt)]
		st.streak, st.missedYesterday = growth.Streak(days, today, firstDay)
	}

	sp, err := b.openSprint(ctx)
	if err != nil {
		return st, err
	}
	st.sprint = sp
	return st, nil
}

func sprintLine(sp *sprintRow, today time.Time) string {
	if sp == nil {
		return ""
	}
	n, total := growth.SprintDay(sp.starts, sp.ends, today)
	when := fmt.Sprintf("kun %d/%d", n, total)
	if n == 0 {
		when = sp.starts.Format("02.01") + " dan"
	}
	return fmt.Sprintf("🎯 Sprint · %s — «%s»", when, escapeHTML(truncate(sp.goal, 80)))
}

func minimumMark(minutes int) string {
	if growth.DayMet(minutes) {
		return "minimum ✓"
	}
	return fmt.Sprintf("minimumgacha %d daq", growth.MinDailyMinutes-minutes)
}

// statusLines is the tail every growth card ends with.
func statusLines(st growthState, today time.Time) string {
	var sb strings.Builder
	if growth.IsRestDay(today) {
		sb.WriteString(fmt.Sprintf("Bugun: %s · dam kuni\n", growth.FormatMinutes(st.todayMinutes)))
	} else {
		sb.WriteString(fmt.Sprintf("Bugun: %s · %s\n", growth.FormatMinutes(st.todayMinutes), minimumMark(st.todayMinutes)))
	}
	if st.streak > 0 {
		sb.WriteString(fmt.Sprintf("🔥 Streak: %d kun\n", st.streak))
	}
	if line := sprintLine(st.sprint, today); line != "" {
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

/* -------------------------------- /qildim --------------------------------- */

func qildimHelp() string {
	return strings.Join([]string{
		"<b>Ingliz tili — yozish</b>",
		"",
		"<code>/qildim 45 w essay 2</code> — 45 daqiqa writing",
		"<code>/qildim 30 s part 1</code> · <code>/qildim 1s l cambridge 18</code>",
		"<code>/qildim kecha 25 anki</code> — kechagi kun uchun",
		"<code>/qildim bekor</code> — oxirgi yozuvni o'chirish",
		"",
		"Turlar: <b>w</b> writing · <b>s</b> speaking · <b>l</b> listening · <b>r</b> reading · <b>v</b> vocab · <b>g</b> grammar · <b>m</b> mock",
		"🎙 Ovozli xabar yuborsangiz — speaking sifatida o'zi yoziladi.",
	}, "\n")
}

func (b *Bot) logCommand(ctx context.Context, args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return qildimHelp()
	}
	if strings.EqualFold(args, "bekor") {
		return b.undoLastLog(ctx)
	}

	l, err := growth.ParseLog(args)
	if err != nil {
		return "❌ " + escapeHTML(err.Error()) + "\n\n" + qildimHelp()
	}

	now := time.Now().In(tashkent)
	day := dayStart(now).AddDate(0, 0, l.DayOffset)
	if _, err := b.pool.Exec(ctx, `
		INSERT INTO growth_logs (day, kind, minutes, note)
		VALUES ($1::date, $2, $3, $4)`,
		day.Format(dateFmt), l.Kind, l.Minutes, l.Note); err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}

	head := fmt.Sprintf("✅ +%s · %s", growth.FormatMinutes(l.Minutes), l.Kind)
	if l.DayOffset != 0 {
		head += " · <i>kecha uchun</i>"
	}
	if l.Note != "" {
		head += "\n<i>" + escapeHTML(truncate(l.Note, 100)) + "</i>"
	}

	st, err := b.growthNow(ctx, now)
	if err != nil {
		return head
	}
	return head + "\n\n" + statusLines(st, dayStart(now))
}

// undoLastLog removes the newest entry from the last day — the typo fix. Older
// rows are left alone: an undo that can reach last week is a delete button.
func (b *Bot) undoLastLog(ctx context.Context) string {
	var (
		kind    string
		minutes int
		day     time.Time
	)
	err := b.pool.QueryRow(ctx, `
		DELETE FROM growth_logs
		WHERE id = (
			SELECT id FROM growth_logs
			WHERE created_at > now() - interval '24 hours'
			ORDER BY created_at DESC LIMIT 1)
		RETURNING kind, minutes, day`).Scan(&kind, &minutes, &day)
	if err != nil {
		if isNoRows(err) {
			return "Oxirgi 24 soatda yozuv yo'q — o'chiradigan narsa yo'q."
		}
		return "❌ O'chirilmadi: " + escapeHTML(shortError(err))
	}
	return fmt.Sprintf("🗑 O'chirildi: %s · %s · %s",
		growth.FormatMinutes(minutes), kind, tashkentDay(day).Format("02.01"))
}

/* --------------------------------- voice ---------------------------------- */

// recordVoice keeps a voice message and counts it as speaking. The audio
// stays on Telegram; the file_id is enough to play it back at /yakun.
func (b *Bot) recordVoice(ctx context.Context, v *Voice, caption string) string {
	today := dayStart(time.Now())
	minutes := (v.Duration + 59) / 60
	if minutes < 1 {
		minutes = 1
	}

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	// The same voice forwarded twice is one recording, not two sessions.
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM growth_recordings WHERE file_unique_id = $1)`,
		v.FileUniqueID).Scan(&exists); err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}
	if exists {
		return "Bu yozuv allaqachon saqlangan."
	}

	var logID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO growth_logs (day, kind, minutes, note, source)
		VALUES ($1::date, 'speaking', $2, $3, 'voice')
		RETURNING id::text`, today.Format(dateFmt), minutes, caption).Scan(&logID); err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO growth_recordings (day, telegram_file_id, file_unique_id, duration_sec, caption, log_id)
		VALUES ($1::date, $2, $3, $4, $5, $6::uuid)`,
		today.Format(dateFmt), v.FileID, v.FileUniqueID, v.Duration, caption, logID); err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}

	head := fmt.Sprintf("🎙 Saqlandi · %d:%02d · speaking +%s",
		v.Duration/60, v.Duration%60, growth.FormatMinutes(minutes))

	st, err := b.growthNow(ctx, time.Now())
	if err != nil {
		return head
	}
	if st.sprint != nil {
		var n int
		if err := b.pool.QueryRow(ctx, `
			SELECT count(*) FROM growth_recordings
			WHERE day BETWEEN $1::date AND $2::date`,
			st.sprint.starts.Format(dateFmt), st.sprint.ends.Format(dateFmt)).Scan(&n); err == nil {
			head += fmt.Sprintf("\nBu sprintdagi %d-yozuv.", n)
		}
	}
	return head + "\n\n" + statusLines(st, today)
}

/* --------------------------------- /bugun --------------------------------- */

func (b *Bot) todayReport(ctx context.Context) string {
	now := time.Now().In(tashkent)
	today := dayStart(now)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📅 <b>Bugun</b> — %s, %s\n\n",
		today.Format("02.01"), growth.WeekdayName(today.Weekday())))

	rows, err := b.pool.Query(ctx, `
		SELECT kind, minutes, note, source FROM growth_logs
		WHERE day = $1::date ORDER BY created_at`, today.Format(dateFmt))
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}
	count := 0
	for rows.Next() {
		var (
			kind, note, source string
			minutes            int
		)
		if err := rows.Scan(&kind, &minutes, &note, &source); err != nil {
			rows.Close()
			return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
		}
		count++
		line := fmt.Sprintf("• %s %s", growth.FormatMinutes(minutes), kind)
		if source == "voice" {
			line += " 🎙"
		}
		if note != "" {
			line += " — <i>" + escapeHTML(truncate(note, 60)) + "</i>"
		}
		sb.WriteString(line + "\n")
	}
	rows.Close()
	if count == 0 {
		sb.WriteString("Hali hech narsa yozilmagan.\n")
	}

	if st, err := b.growthNow(ctx, now); err == nil {
		sb.WriteString("\n" + statusLines(st, today))
	}
	if line := b.bookTodayLine(ctx, today); line != "" {
		sb.WriteString(line + "\n")
	}
	if count == 0 {
		sb.WriteString("\n<code>/qildim 25 w</code> yoki ovozli xabar yuboring.")
	}
	return sb.String()
}

/* -------------------------------- /sprint --------------------------------- */

func (b *Bot) sprintCommand(ctx context.Context, args string) string {
	now := time.Now().In(tashkent)
	goal := strings.TrimSpace(args)

	sp, err := b.openSprint(ctx)
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}

	if goal == "" {
		if sp == nil {
			return strings.Join([]string{
				"Ochiq sprint yo'q.",
				"",
				"Natijani bitta jumlada yozib oching:",
				"<code>/sprint 5 ta Task 2 essay, 2 tasi tekshirilgan</code>",
				"",
				"Du–Ju ochilsa bugundan, dam olish kunlari keyingi dushanbadan boshlanadi. Shanba — natija kuni.",
			}, "\n")
		}
		return b.sprintReport(ctx, sp, now)
	}

	if sp != nil {
		// Opening a new sprint over an open one would skip the part that
		// matters most: looking at what the last one produced.
		return fmt.Sprintf("Ochiq sprint bor: «%s»\n\nAvval /yakun bilan yoping — natijasi va oldin/keyin yozuvlari chiqadi. Keyin yangisini oching.",
			escapeHTML(sp.goal))
	}

	if len([]rune(goal)) < 3 || len([]rune(goal)) > 300 {
		return "Natija jumlasi 3–300 belgi bo'lsin."
	}
	start, end := growth.SprintWindow(now)
	if _, err := b.pool.Exec(ctx, `
		INSERT INTO growth_sprints (goal, starts_on, ends_on)
		VALUES ($1, $2::date, $3::date)`, goal, start.Format(dateFmt), end.Format(dateFmt)); err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}
	_, total := growth.SprintDay(start, end, start)
	return fmt.Sprintf("🎯 <b>Sprint ochildi</b>\n\n«%s»\n\n%s – %s · %d ish kuni\nKunlik minimum: %d daq. Shanba — natija kuni, /yakun.",
		escapeHTML(goal), start.Format("02.01"), end.Format("02.01"), total, growth.MinDailyMinutes)
}

// sprintTotals is what a sprint produced, read from the date range.
type sprintTotals struct {
	byKind     map[string]int
	total      int
	daysMet    int
	workDays   int
	recordings int
}

func (b *Bot) sprintTotals(ctx context.Context, sp *sprintRow, upTo time.Time) (sprintTotals, error) {
	t := sprintTotals{byKind: map[string]int{}}
	end := sp.ends
	if upTo.Before(end) {
		end = upTo
	}

	rows, err := b.pool.Query(ctx, `
		SELECT kind, sum(minutes)::int FROM growth_logs
		WHERE day BETWEEN $1::date AND $2::date
		GROUP BY kind`, sp.starts.Format(dateFmt), end.Format(dateFmt))
	if err != nil {
		return t, err
	}
	for rows.Next() {
		var (
			k string
			m int
		)
		if err := rows.Scan(&k, &m); err != nil {
			rows.Close()
			return t, err
		}
		t.byKind[k] = m
		t.total += m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return t, err
	}

	days, err := b.minutesByDay(ctx, sp.starts)
	if err != nil {
		return t, err
	}
	for d := sp.starts; !d.After(end); d = d.AddDate(0, 0, 1) {
		if growth.IsRestDay(d) {
			continue
		}
		t.workDays++
		if growth.DayMet(days[d.Format(dateFmt)]) {
			t.daysMet++
		}
	}

	err = b.pool.QueryRow(ctx, `
		SELECT count(*) FROM growth_recordings
		WHERE day BETWEEN $1::date AND $2::date`,
		sp.starts.Format(dateFmt), sp.ends.Format(dateFmt)).Scan(&t.recordings)
	return t, err
}

func kindBreakdown(byKind map[string]int) string {
	var parts []string
	for _, k := range growth.Kinds {
		if m := byKind[k]; m > 0 {
			parts = append(parts, fmt.Sprintf("%s %s", k, growth.FormatMinutes(m)))
		}
	}
	return strings.Join(parts, " · ")
}

func (b *Bot) sprintReport(ctx context.Context, sp *sprintRow, now time.Time) string {
	today := dayStart(now)
	t, err := b.sprintTotals(ctx, sp, today)
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🎯 <b>Sprint</b> — «%s»\n", escapeHTML(sp.goal)))
	sb.WriteString(fmt.Sprintf("%s – %s\n\n", sp.starts.Format("02.01"), sp.ends.Format("02.01")))
	n, total := growth.SprintDay(sp.starts, sp.ends, today)
	sb.WriteString(fmt.Sprintf("Kun: %d/%d · minimum bajarilgan: %d/%d\n", n, total, t.daysMet, t.workDays))
	sb.WriteString(fmt.Sprintf("Jami: %s\n", growth.FormatMinutes(t.total)))
	if br := kindBreakdown(t.byKind); br != "" {
		sb.WriteString(br + "\n")
	}
	sb.WriteString(fmt.Sprintf("🎙 Yozuvlar: %d\n", t.recordings))
	if !today.Before(sp.ends) {
		sb.WriteString("\nBugun natija kuni — /yakun")
	}
	return sb.String()
}

/* --------------------------------- /yakun --------------------------------- */

type recording struct {
	fileID   string
	day      time.Time
	duration int
}

// firstAndLast returns the earliest and newest recordings in a range; both are
// nil when there is none, and they are the same row when there is one.
func (b *Bot) firstAndLast(ctx context.Context, from, to *time.Time) (first, last *recording, err error) {
	where := "TRUE"
	var args []any
	if from != nil && to != nil {
		where = "day BETWEEN $1::date AND $2::date"
		args = append(args, from.Format(dateFmt), to.Format(dateFmt))
	}
	pick := func(order string) (*recording, error) {
		var r recording
		err := b.pool.QueryRow(ctx, `
			SELECT telegram_file_id, day, duration_sec FROM growth_recordings
			WHERE `+where+` ORDER BY created_at `+order+` LIMIT 1`, args...).
			Scan(&r.fileID, &r.day, &r.duration)
		if err != nil {
			if isNoRows(err) {
				return nil, nil
			}
			return nil, err
		}
		r.day = tashkentDay(r.day)
		return &r, nil
	}
	if first, err = pick("ASC"); err != nil || first == nil {
		return nil, nil, err
	}
	last, err = pick("DESC")
	return first, last, err
}

// playBeforeAfter sends the two recordings back to back. That is the whole
// reward of a speaking sprint: the difference is heard, not read.
func (b *Bot) playBeforeAfter(ctx context.Context, chatID int64, first, last *recording) string {
	if first == nil {
		return "🎙 Bu davrda ovozli yozuv yo'q — keyingi sprintda kamida ikkitasini yuboring (boshida va oxirida)."
	}
	if first.fileID == last.fileID {
		b.sendVoiceLogged(ctx, chatID, first.fileID, "🎙 Yagona yozuv · "+first.day.Format("02.01"))
		return "🎙 Faqat bitta yozuv bor — solishtirish uchun oxirida yana bittasini yuboring."
	}
	b.sendVoiceLogged(ctx, chatID, first.fileID, "⏮ <b>Oldin</b> · "+first.day.Format("02.01"))
	b.sendVoiceLogged(ctx, chatID, last.fileID, "⏭ <b>Keyin</b> · "+last.day.Format("02.01"))
	return ""
}

func (b *Bot) sendVoiceLogged(ctx context.Context, chatID int64, fileID, caption string) {
	if err := b.tg.sendVoice(ctx, chatID, fileID, caption); err != nil {
		log.Printf("bot: sendVoice failed: %v", err)
	}
}

func (b *Bot) closeSprint(ctx context.Context, chatID int64, note string) {
	now := time.Now().In(tashkent)
	sp, err := b.openSprint(ctx)
	if err != nil {
		b.reply(ctx, chatID, "❌ O'qib bo'lmadi: "+escapeHTML(shortError(err)))
		return
	}
	if sp == nil {
		b.reply(ctx, chatID, "Ochiq sprint yo'q. Yangisi: <code>/sprint natija jumlasi</code>")
		return
	}

	t, err := b.sprintTotals(ctx, sp, dayStart(now))
	if err != nil {
		b.reply(ctx, chatID, "❌ O'qib bo'lmadi: "+escapeHTML(shortError(err)))
		return
	}
	if _, err := b.pool.Exec(ctx, `
		UPDATE growth_sprints SET closed_at = now(), result_note = $2 WHERE id = $1::uuid`,
		sp.id, strings.TrimSpace(note)); err != nil {
		b.reply(ctx, chatID, "❌ Yopilmadi: "+escapeHTML(shortError(err)))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🏁 <b>Sprint yopildi</b>\n\n«%s»\n%s – %s\n\n",
		escapeHTML(sp.goal), sp.starts.Format("02.01"), sp.ends.Format("02.01")))
	sb.WriteString(fmt.Sprintf("Minimum bajarilgan kunlar: <b>%d/%d</b>\n", t.daysMet, t.workDays))
	sb.WriteString(fmt.Sprintf("Jami: %s\n", growth.FormatMinutes(t.total)))
	if br := kindBreakdown(t.byKind); br != "" {
		sb.WriteString(br + "\n")
	}
	sb.WriteString(fmt.Sprintf("🎙 Yozuvlar: %d\n", t.recordings))
	if scores := b.scoresBetween(ctx, sp.starts, sp.ends); scores != "" {
		sb.WriteString("\n" + scores)
	}
	if n := strings.TrimSpace(note); n != "" {
		sb.WriteString("\n📝 " + escapeHTML(n) + "\n")
	}
	sb.WriteString("\nDam oling. Keyingisi: <code>/sprint natija jumlasi</code>")
	b.reply(ctx, chatID, sb.String())

	first, last, err := b.firstAndLast(ctx, &sp.starts, &sp.ends)
	if err != nil {
		log.Printf("bot: could not read recordings: %v", err)
		return
	}
	if msg := b.playBeforeAfter(ctx, chatID, first, last); msg != "" {
		b.reply(ctx, chatID, msg)
	}
}

// compareCommand is /solishtir: the open sprint's first and last recording, or
// the very first and the newest when no sprint is open — the long view.
func (b *Bot) compareCommand(ctx context.Context, chatID int64) {
	sp, err := b.openSprint(ctx)
	if err != nil {
		b.reply(ctx, chatID, "❌ O'qib bo'lmadi: "+escapeHTML(shortError(err)))
		return
	}
	var first, last *recording
	if sp != nil {
		first, last, err = b.firstAndLast(ctx, &sp.starts, &sp.ends)
	} else {
		first, last, err = b.firstAndLast(ctx, nil, nil)
	}
	if err != nil {
		b.reply(ctx, chatID, "❌ O'qib bo'lmadi: "+escapeHTML(shortError(err)))
		return
	}
	if msg := b.playBeforeAfter(ctx, chatID, first, last); msg != "" {
		b.reply(ctx, chatID, msg)
	}
}

/* ---------------------------------- /ball --------------------------------- */

func ballHelp() string {
	return strings.Join([]string{
		"<code>/ball 6.0 L6.5 R6 W5.5 S5.5 mock Cambridge 18</code>",
		"<code>/ball 6.5 rasmiy</code> · <code>/ball 6 tutor</code>",
		"Faqat umumiy ball ham yetadi: <code>/ball 6</code>",
	}, "\n")
}

func (b *Bot) scoreCommand(ctx context.Context, args string) string {
	if strings.TrimSpace(args) == "" {
		if list := b.recentScores(ctx, 5); list != "" {
			return list + "\n" + ballHelp()
		}
		return "Hali ball yo'q.\n\n" + ballHelp()
	}

	s, err := growth.ParseScore(args)
	if err != nil {
		return "❌ " + escapeHTML(err.Error()) + "\n\n" + ballHelp()
	}
	part := func(h int) any {
		if h == 0 {
			return nil
		}
		return float64(h) / 2
	}
	today := dayStart(time.Now())
	if _, err := b.pool.Exec(ctx, `
		INSERT INTO growth_scores (taken_on, kind, overall, listening, reading, writing, speaking, note)
		VALUES ($1::date, $2, $3, $4, $5, $6, $7, $8)`,
		today.Format(dateFmt), s.Kind, float64(s.OverallHalves)/2,
		part(s.Listening), part(s.Reading), part(s.Writing), part(s.Speaking), s.Note); err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}

	return "✅ Ball saqlandi\n\n" + b.recentScores(ctx, 5)
}

type scoreRow struct {
	day        time.Time
	kind       string
	overall    float64
	l, r, w, s *float64
	note       string
}

func (b *Bot) loadScores(ctx context.Context, where string, args ...any) ([]scoreRow, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT taken_on, kind, overall::float8, listening::float8, reading::float8,
		       writing::float8, speaking::float8, note
		FROM growth_scores `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []scoreRow
	for rows.Next() {
		var s scoreRow
		if err := rows.Scan(&s.day, &s.kind, &s.overall, &s.l, &s.r, &s.w, &s.s, &s.note); err != nil {
			return nil, err
		}
		s.day = tashkentDay(s.day)
		out = append(out, s)
	}
	return out, rows.Err()
}

func formatScore(s scoreRow) string {
	line := fmt.Sprintf("%s · <b>%.1f</b> %s", s.day.Format("02.01"), s.overall, growth.ScoreKindLabel(s.kind))
	var parts []string
	for _, p := range []struct {
		label string
		v     *float64
	}{{"L", s.l}, {"R", s.r}, {"W", s.w}, {"S", s.s}} {
		if p.v != nil {
			parts = append(parts, fmt.Sprintf("%s%.1f", p.label, *p.v))
		}
	}
	if len(parts) > 0 {
		line += " (" + strings.Join(parts, " ") + ")"
	}
	if s.note != "" {
		line += " — <i>" + escapeHTML(truncate(s.note, 40)) + "</i>"
	}
	return line
}

// recentScores lists the newest scores, each with its change from the one
// before it — the direction matters more than any single number.
func (b *Bot) recentScores(ctx context.Context, n int) string {
	scores, err := b.loadScores(ctx, `ORDER BY taken_on DESC, created_at DESC LIMIT $1`, n+1)
	if err != nil || len(scores) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("📊 <b>Ballar</b>\n")
	for i, s := range scores {
		if i == n {
			break
		}
		line := formatScore(s)
		if i+1 < len(scores) {
			d := s.overall - scores[i+1].overall
			switch {
			case d > 0:
				line += fmt.Sprintf(" ↑%.1f", d)
			case d < 0:
				line += fmt.Sprintf(" ↓%.1f", -d)
			}
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

func (b *Bot) scoresBetween(ctx context.Context, from, to time.Time) string {
	scores, err := b.loadScores(ctx, `WHERE taken_on BETWEEN $1::date AND $2::date ORDER BY taken_on, created_at`,
		from.Format(dateFmt), to.Format(dateFmt))
	if err != nil || len(scores) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("📊 Ballar:\n")
	for _, s := range scores {
		sb.WriteString(formatScore(s) + "\n")
	}
	return sb.String()
}

/* --------------------------------- nudge ---------------------------------- */

// scheduleGrowthNudge runs every minute from the scheduler. It does nothing
// until the evening, and then queues at most one row per day — and only while
// the day is still short, so writing the work down at 20:55 cancels the 21:00
// message by never creating it.
func (b *Bot) scheduleGrowthNudge(ctx context.Context) {
	now := time.Now().In(tashkent)
	// Cheap exit for most of the day, before any query.
	if now.Hour() < 19 || now.Hour() >= 23 || growth.IsRestDay(now) {
		return
	}

	st, err := b.growthNow(ctx, now)
	if err != nil {
		log.Printf("bot: growth nudge: %v", err)
		return
	}
	// Nothing ever logged: the habit has not started, and a reminder about a
	// habit that does not exist yet is noise.
	if !st.hasAny {
		return
	}
	if !growth.NudgeDue(now, st.todayMinutes, st.missedYesterday) {
		return
	}

	payload := map[string]any{
		"today_minutes":    st.todayMinutes,
		"missed_yesterday": st.missedYesterday,
		"streak":           st.streak,
	}
	if st.sprint != nil {
		payload["sprint_goal"] = st.sprint.goal
	}
	b.queueScheduled(ctx, "growth.nudge", payload, sevInfo,
		"growth-nudge:"+now.Format(dateFmt), time.Time{})
}

func renderGrowthNudge(p map[string]any) string {
	today := int(num(p, "today_minutes"))
	missed, _ := p["missed_yesterday"].(bool)
	streak := num(p, "streak")

	var sb strings.Builder
	switch {
	case missed:
		sb.WriteString("⚠️ <b>Kecha o'tib ketdi — bugun ikkinchisi bo'lmasin.</b>\n")
		if today > 0 {
			sb.WriteString(fmt.Sprintf("\nBugun %s. Yana %d daqiqa yetadi.\n",
				growth.FormatMinutes(today), growth.MinDailyMinutes-today))
		} else {
			sb.WriteString(fmt.Sprintf("\n%d daqiqa yetadi: 1 ta writing paragraf + 2 daqiqalik ovoz.\n", growth.MinDailyMinutes))
		}
	case today > 0:
		sb.WriteString(fmt.Sprintf("⏰ Bugun %s. Minimumgacha %d daqiqa qoldi.\n",
			growth.FormatMinutes(today), growth.MinDailyMinutes-today))
	default:
		sb.WriteString(fmt.Sprintf("⏰ <b>Bugun hali nol.</b>\n\nMinimum — %d daqiqa: 1 ta writing paragraf + 2 daqiqalik ovoz.\n", growth.MinDailyMinutes))
	}
	if streak > 0 {
		sb.WriteString(fmt.Sprintf("🔥 Streak: %d kun\n", streak))
	}
	if goal := str(p, "sprint_goal"); goal != "" {
		sb.WriteString("🎯 «" + escapeHTML(truncate(goal, 80)) + "»\n")
	}
	sb.WriteString("\n<code>/qildim 25 w</code> · 🎙 ovozli xabar")
	return sb.String()
}

/* --------------------------- daily and weekly ----------------------------- */

// growthDailySection is the morning summary's block. Left out entirely until
// something has been logged, and silently when a query fails — the summary's
// rule is that a section it cannot read is a section it does not print.
func (b *Bot) growthDailySection(ctx context.Context) string {
	now := time.Now().In(tashkent)
	st, err := b.growthNow(ctx, now)
	if err != nil {
		return ""
	}
	books := b.bookDailyLine(ctx)
	if !st.hasAny && st.sprint == nil && books == "" {
		return ""
	}

	today := dayStart(now)
	yesterday := today.AddDate(0, 0, -1)

	var sb strings.Builder
	sb.WriteString("\n📈 <b>O'sish</b>\n")
	if st.hasAny {
		days, err := b.minutesByDay(ctx, yesterday)
		if err != nil {
			return ""
		}
		y := days[yesterday.Format(dateFmt)]
		switch {
		case growth.IsRestDay(yesterday):
			sb.WriteString(fmt.Sprintf("Kecha: dam kuni · %s\n", growth.FormatMinutes(y)))
		case growth.DayMet(y):
			sb.WriteString(fmt.Sprintf("Kecha: %s ✓", growth.FormatMinutes(y)))
		default:
			sb.WriteString(fmt.Sprintf("Kecha: %s ✗", growth.FormatMinutes(y)))
		}
		if !growth.IsRestDay(yesterday) {
			if st.streak > 0 {
				sb.WriteString(fmt.Sprintf(" · 🔥 %d kun", st.streak))
			}
			sb.WriteString("\n")
		}
		if st.missedYesterday && !growth.IsRestDay(today) {
			sb.WriteString("⚠️ Bugun ikkinchi nol bo'lmasin.\n")
		}
	}
	if line := sprintLine(st.sprint, today); line != "" {
		sb.WriteString(line + "\n")
	}
	sb.WriteString(books)
	return sb.String()
}

// growthWeeklySection goes into the Sunday evening report: the week's English
// by kind, the days that counted, and the reading.
func (b *Bot) growthWeeklySection(ctx context.Context) string {
	today := dayStart(time.Now())
	from := today.AddDate(0, 0, -6)

	days, err := b.minutesByDay(ctx, from)
	if err != nil {
		return ""
	}
	books := b.bookWeekLine(ctx, from, today)
	total := 0
	for _, m := range days {
		total += m
	}
	if total == 0 && books == "" {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n📈 <b>O'sish — hafta</b>\n")
	if total > 0 {
		met, work := 0, 0
		var bars []int
		var labels strings.Builder
		for d := from; !d.After(today); d = d.AddDate(0, 0, 1) {
			m := days[d.Format(dateFmt)]
			bars = append(bars, m)
			labels.WriteString(growth.WeekdayShort(d.Weekday())[:1])
			if growth.IsRestDay(d) {
				continue
			}
			work++
			if growth.DayMet(m) {
				met++
			}
		}
		sb.WriteString(fmt.Sprintf("Ingliz: %s · minimum %d/%d kun\n", growth.FormatMinutes(total), met, work))
		sb.WriteString(fmt.Sprintf("<code>%s</code>\n<code>%s</code>\n", growth.Sparkline(bars), labels.String()))

		byKind := map[string]int{}
		rows, err := b.pool.Query(ctx, `
			SELECT kind, sum(minutes)::int FROM growth_logs
			WHERE day BETWEEN $1::date AND $2::date GROUP BY kind`,
			from.Format(dateFmt), today.Format(dateFmt))
		if err == nil {
			for rows.Next() {
				var (
					k string
					m int
				)
				if rows.Scan(&k, &m) == nil {
					byKind[k] = m
				}
			}
			rows.Close()
		}
		if br := kindBreakdown(byKind); br != "" {
			sb.WriteString(br + "\n")
		}
	}
	sb.WriteString(books)
	return sb.String()
}

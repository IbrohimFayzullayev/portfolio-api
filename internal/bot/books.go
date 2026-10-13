package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ibrohimcoder/portfolio-api/internal/growth"
)

// Books read for pleasure.
//
// Kept beside the English log but deliberately apart from it: a novel in Uzbek
// is rest, not practice, and must never quietly satisfy the daily English
// minimum. What it gets instead is a record — which book, which days, how long
// — because seeing "4 books this year, 31 hours" is its own small reward.
//
//	/kitob yangi O'tkan kunlar - Abdulla Qodiriy 420 bet
//	/kitob 40 120-bet
//	/kitob tugatdim
//	/kitoblar

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

type bookRow struct {
	id         string
	title      string
	author     string
	totalPages *int
	status     string
	startedOn  time.Time
	finishedOn *time.Time
}

// bookProgress is everything computed from a book's sessions.
type bookProgress struct {
	minutes  int
	days     int
	sessions int
	lastPage int
	lastDay  *time.Time
}

func bookLabel(b bookRow) string {
	s := "«" + escapeHTML(b.title) + "»"
	if b.author != "" {
		s += " — " + escapeHTML(b.author)
	}
	return s
}

func (p bookProgress) pageText(b bookRow) string {
	if p.lastPage == 0 {
		return ""
	}
	if b.totalPages != nil {
		return fmt.Sprintf("%d%% (%d/%d)", growth.Percent(p.lastPage, *b.totalPages), p.lastPage, *b.totalPages)
	}
	return fmt.Sprintf("%d-bet", p.lastPage)
}

func bookHelp() string {
	return strings.Join([]string{
		"<b>📚 Kitoblar</b>",
		"",
		"<code>/kitob yangi O'tkan kunlar - Abdulla Qodiriy 420 bet</code>",
		"<code>/kitob 40</code> — 40 daqiqa o'qidim",
		"<code>/kitob 40 120-bet</code> — ... va 120-betga yetdim",
		"<code>/kitob kecha 1s</code> — kechagi kun uchun",
		"<code>/kitob tugatdim</code> · <code>/kitob tashladim</code>",
		"<code>/kitob bekor</code> — oxirgi yozuvni o'chirish",
		"/kitoblar — statistika",
		"",
		"<i>Bir nechta kitob o'qilayotgan bo'lsa, nomidan bir so'z qo'shing: <code>/kitob ufq 30</code></i>",
	}, "\n")
}

/* --------------------------------- lookup --------------------------------- */

// readingBooks lists the books being read, most recently touched first — the
// first one is "the current book".
func (b *Bot) readingBooks(ctx context.Context) ([]bookRow, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT bk.id::text, bk.title, bk.author, bk.total_pages, bk.status, bk.started_on, bk.finished_on
		FROM books bk
		LEFT JOIN book_sessions s ON s.book_id = bk.id
		WHERE bk.status = 'reading'
		GROUP BY bk.id
		ORDER BY COALESCE(max(s.created_at), bk.created_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bookRow
	for rows.Next() {
		var bk bookRow
		if err := rows.Scan(&bk.id, &bk.title, &bk.author, &bk.totalPages, &bk.status, &bk.startedOn, &bk.finishedOn); err != nil {
			return nil, err
		}
		bk.startedOn = tashkentDay(bk.startedOn)
		out = append(out, bk)
	}
	return out, rows.Err()
}

// pickBook chooses among the books being read: the current one when the query
// is empty, otherwise the one whose title or author contains it.
func pickBook(books []bookRow, query string) (bookRow, string) {
	if len(books) == 0 {
		return bookRow{}, "Hozir o'qilayotgan kitob yo'q.\n\n<code>/kitob yangi Nomi - Muallif 320 bet</code>"
	}
	q := strings.ToLower(strings.TrimSpace(growth.NormalizeApostrophes(query)))
	if q == "" {
		return books[0], ""
	}
	var hits []bookRow
	for _, bk := range books {
		hay := strings.ToLower(growth.NormalizeApostrophes(bk.title + " " + bk.author))
		if strings.Contains(hay, q) {
			hits = append(hits, bk)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], ""
	case 0:
		var names []string
		for _, bk := range books {
			names = append(names, "• "+bookLabel(bk))
		}
		return bookRow{}, "«" + escapeHTML(query) + "» topilmadi. O'qilayotganlar:\n" + strings.Join(names, "\n")
	default:
		return bookRow{}, "«" + escapeHTML(query) + "» bir nechta kitobga mos keldi — aniqroq yozing."
	}
}

func (b *Bot) bookProgress(ctx context.Context, bookID string) (bookProgress, error) {
	var p bookProgress
	err := b.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(minutes), 0)::int, count(DISTINCT day)::int, count(*)::int,
		       COALESCE((SELECT page_reached FROM book_sessions
		                 WHERE book_id = $1::uuid AND page_reached IS NOT NULL
		                 ORDER BY day DESC, created_at DESC LIMIT 1), 0),
		       max(day)
		FROM book_sessions WHERE book_id = $1::uuid`, bookID).
		Scan(&p.minutes, &p.days, &p.sessions, &p.lastPage, &p.lastDay)
	return p, err
}

/* -------------------------------- /kitob ---------------------------------- */

func (b *Bot) bookCommand(ctx context.Context, args string) string {
	args = strings.TrimSpace(args)
	first, rest, _ := strings.Cut(args, " ")
	switch strings.ToLower(growth.NormalizeApostrophes(first)) {
	case "":
		return b.readingStatus(ctx)
	case "yangi", "boshladim", "+":
		return b.addBook(ctx, rest)
	case "tugatdim", "tugadi", "tugatildi":
		return b.finishBook(ctx, rest, "finished")
	case "tashladim", "qoldirdim":
		return b.finishBook(ctx, rest, "dropped")
	case "bekor":
		return b.undoBookSession(ctx)
	case "yordam", "help":
		return bookHelp()
	}
	return b.logReading(ctx, args)
}

func (b *Bot) addBook(ctx context.Context, args string) string {
	nb, err := growth.ParseNewBook(args)
	if err != nil {
		return "Kitob nomini yozing:\n<code>/kitob yangi O'tkan kunlar - Abdulla Qodiriy 420 bet</code>"
	}
	var pages any
	if nb.Pages > 0 {
		pages = nb.Pages
	}
	today := dayStart(time.Now())
	if _, err := b.pool.Exec(ctx, `
		INSERT INTO books (title, author, total_pages, started_on)
		VALUES ($1, $2, $3, $4::date)`, nb.Title, nb.Author, pages, today.Format(dateFmt)); err != nil {
		if strings.Contains(err.Error(), "idx_books_reading_title") {
			return "Bu kitob allaqachon o'qilmoqda. <code>/kitob</code>"
		}
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}

	text := fmt.Sprintf("📖 <b>Yangi kitob</b>\n\n%s", bookLabel(bookRow{title: nb.Title, author: nb.Author}))
	if nb.Pages > 0 {
		text += fmt.Sprintf("\n%d bet", nb.Pages)
	}
	return text + "\n\nO'qiganingizda: <code>/kitob 30</code> yoki <code>/kitob 30 45-bet</code>"
}

func (b *Bot) logReading(ctx context.Context, args string) string {
	l, err := growth.ParseBookLog(args)
	if err != nil {
		if growth.IsNoMinutes(err) {
			return "Necha daqiqa o'qidingiz?\n\n" + bookHelp()
		}
		return "❌ " + escapeHTML(err.Error())
	}

	books, err := b.readingBooks(ctx)
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}
	bk, problem := pickBook(books, l.Query)
	if problem != "" {
		return problem
	}
	if l.Page > 0 && bk.totalPages != nil && l.Page > *bk.totalPages {
		return fmt.Sprintf("Kitob %d bet — %d-bet bo'lishi mumkin emas.", *bk.totalPages, l.Page)
	}

	day := dayStart(time.Now()).AddDate(0, 0, l.DayOffset)
	var page any
	if l.Page > 0 {
		page = l.Page
	}
	if _, err := b.pool.Exec(ctx, `
		INSERT INTO book_sessions (book_id, day, minutes, page_reached)
		VALUES ($1::uuid, $2::date, $3, $4)`, bk.id, day.Format(dateFmt), l.Minutes, page); err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}

	p, err := b.bookProgress(ctx, bk.id)
	if err != nil {
		return "📚 Saqlandi."
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📚 +%s · %s", growth.FormatMinutes(l.Minutes), bookLabel(bk)))
	if l.DayOffset != 0 {
		sb.WriteString(" · <i>kecha uchun</i>")
	}
	sb.WriteString("\n\n")
	if pt := p.pageText(bk); pt != "" {
		sb.WriteString("📍 " + pt + "\n")
	}
	sb.WriteString(fmt.Sprintf("Shu kitobga: %s · %d kun\n", growth.FormatMinutes(p.minutes), p.days))
	if bk.totalPages != nil && p.lastPage >= *bk.totalPages {
		sb.WriteString("\nOxirgi betga yetdingiz — <code>/kitob tugatdim</code>")
	}
	return sb.String()
}

func (b *Bot) undoBookSession(ctx context.Context) string {
	var (
		title   string
		minutes int
	)
	err := b.pool.QueryRow(ctx, `
		WITH gone AS (
			DELETE FROM book_sessions
			WHERE id = (SELECT id FROM book_sessions
			            WHERE created_at > now() - interval '24 hours'
			            ORDER BY created_at DESC LIMIT 1)
			RETURNING book_id, minutes)
		SELECT bk.title, gone.minutes FROM gone JOIN books bk ON bk.id = gone.book_id`).
		Scan(&title, &minutes)
	if err != nil {
		if isNoRows(err) {
			return "Oxirgi 24 soatda kitob yozuvi yo'q."
		}
		return "❌ O'chirilmadi: " + escapeHTML(shortError(err))
	}
	return fmt.Sprintf("🗑 O'chirildi: %s · «%s»", growth.FormatMinutes(minutes), escapeHTML(title))
}

func (b *Bot) finishBook(ctx context.Context, query, status string) string {
	books, err := b.readingBooks(ctx)
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}
	bk, problem := pickBook(books, query)
	if problem != "" {
		return problem
	}

	today := dayStart(time.Now())
	if _, err := b.pool.Exec(ctx, `
		UPDATE books SET status = $2, finished_on = $3::date WHERE id = $1::uuid`,
		bk.id, status, today.Format(dateFmt)); err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}

	p, _ := b.bookProgress(ctx, bk.id)
	if status == "dropped" {
		return fmt.Sprintf("📕 Qoldirildi: %s\n%s · %d kun. Hammasini oxirigacha o'qish shart emas.",
			bookLabel(bk), growth.FormatMinutes(p.minutes), p.days)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🏁 <b>Kitob tugadi!</b>\n\n%s\n", bookLabel(bk)))
	if bk.totalPages != nil {
		sb.WriteString(fmt.Sprintf("%d bet\n", *bk.totalPages))
	}
	span := int(today.Sub(bk.startedOn).Hours()/24) + 1
	sb.WriteString(fmt.Sprintf("\n%s – %s · %d kun ichida\n", bk.startedOn.Format("02.01"), today.Format("02.01"), span))
	sb.WriteString(fmt.Sprintf("O'qilgan kunlar: %d · jami %s\n", p.days, growth.FormatMinutes(p.minutes)))
	if p.days > 0 {
		sb.WriteString(fmt.Sprintf("O'rtacha: %s / kun\n", growth.FormatMinutes(p.minutes/p.days)))
	}

	var n int
	if err := b.pool.QueryRow(ctx, `
		SELECT count(*) FROM books
		WHERE status = 'finished' AND finished_on >= date_trunc('year', $1::date)`,
		today.Format(dateFmt)).Scan(&n); err == nil {
		sb.WriteString(fmt.Sprintf("\n📚 %d-yildagi %d-kitob.", today.Year(), n))
	}
	return sb.String()
}

// readingStatus is bare /kitob: what is open, and where each one stands.
func (b *Bot) readingStatus(ctx context.Context) string {
	books, err := b.readingBooks(ctx)
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}
	if len(books) == 0 {
		return bookHelp()
	}
	var sb strings.Builder
	sb.WriteString("📖 <b>O'qilmoqda</b>\n")
	for _, bk := range books {
		p, err := b.bookProgress(ctx, bk.id)
		if err != nil {
			continue
		}
		sb.WriteString("\n" + bookLabel(bk) + "\n")
		line := fmt.Sprintf("%s · %d kun", growth.FormatMinutes(p.minutes), p.days)
		if pt := p.pageText(bk); pt != "" {
			line += " · " + pt
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n<code>/kitob 30</code> · /kitoblar")
	return sb.String()
}

/* ------------------------------- /kitoblar -------------------------------- */

// booksReport is the statistics view: this year by default, "/kitoblar 2025"
// for another, "/kitoblar hammasi" for everything.
func (b *Bot) booksReport(ctx context.Context, arg string) string {
	today := dayStart(time.Now())
	year := today.Year()
	from := time.Date(year, 1, 1, 0, 0, 0, 0, tashkent)
	to := time.Date(year, 12, 31, 0, 0, 0, 0, tashkent)
	label := fmt.Sprintf("%d", year)

	switch a := strings.ToLower(strings.TrimSpace(arg)); {
	case a == "hammasi" || a == "hamma":
		from, to, label = time.Date(2000, 1, 1, 0, 0, 0, 0, tashkent), today, "hammasi"
	case len(a) == 4:
		if y, err := time.Parse("2006", a); err == nil {
			year = y.Year()
			from = time.Date(year, 1, 1, 0, 0, 0, 0, tashkent)
			to = time.Date(year, 12, 31, 0, 0, 0, 0, tashkent)
			label = a
		}
	}

	var totalMin, days, sessions int
	if err := b.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(minutes), 0)::int, count(DISTINCT day)::int, count(*)::int
		FROM book_sessions WHERE day BETWEEN $1::date AND $2::date`,
		from.Format(dateFmt), to.Format(dateFmt)).Scan(&totalMin, &days, &sessions); err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📚 <b>Kitoblar — %s</b>\n\n", label))
	if sessions == 0 {
		sb.WriteString("Bu davrda o'qish yozilmagan.\n")
	} else {
		sb.WriteString(fmt.Sprintf("Jami: <b>%s</b> · %d kun o'qildi\n", growth.FormatMinutes(totalMin), days))
		if days > 0 {
			sb.WriteString(fmt.Sprintf("O'qilgan kunga o'rtacha: %s\n", growth.FormatMinutes(totalMin/days)))
		}
	}

	// This month and the last fourteen days only make sense for the year we
	// are in.
	if label != "hammasi" && year == today.Year() {
		monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, tashkent)
		var mMin, mDays int
		if err := b.pool.QueryRow(ctx, `
			SELECT COALESCE(sum(minutes), 0)::int, count(DISTINCT day)::int
			FROM book_sessions WHERE day >= $1::date`, monthStart.Format(dateFmt)).Scan(&mMin, &mDays); err == nil && mMin > 0 {
			sb.WriteString(fmt.Sprintf("%s: %s · %d kun\n", monthName(today.Month()), growth.FormatMinutes(mMin), mDays))
		}
		if bars := b.readingBars(ctx, today.AddDate(0, 0, -13), today); bars != "" {
			sb.WriteString("\nOxirgi 14 kun:\n" + bars)
		}
	}

	// Being read.
	if reading, err := b.readingBooks(ctx); err == nil && len(reading) > 0 {
		sb.WriteString("\n📖 <b>O'qilmoqda</b>\n")
		for _, bk := range reading {
			p, err := b.bookProgress(ctx, bk.id)
			if err != nil {
				continue
			}
			line := fmt.Sprintf("• %s — %s · %d kun", bookLabel(bk), growth.FormatMinutes(p.minutes), p.days)
			if pt := p.pageText(bk); pt != "" {
				line += " · " + pt
			}
			sb.WriteString(line + "\n")
		}
	}

	// Finished in the period, newest first.
	rows, err := b.pool.Query(ctx, `
		SELECT bk.id::text, bk.title, bk.author, bk.total_pages, bk.status, bk.started_on, bk.finished_on
		FROM books bk
		WHERE bk.status = 'finished' AND bk.finished_on BETWEEN $1::date AND $2::date
		ORDER BY bk.finished_on DESC, bk.created_at DESC`, from.Format(dateFmt), to.Format(dateFmt))
	if err == nil {
		var finished []bookRow
		for rows.Next() {
			var bk bookRow
			if rows.Scan(&bk.id, &bk.title, &bk.author, &bk.totalPages, &bk.status, &bk.startedOn, &bk.finishedOn) == nil {
				bk.startedOn = tashkentDay(bk.startedOn)
				finished = append(finished, bk)
			}
		}
		rows.Close()
		if len(finished) > 0 {
			sb.WriteString(fmt.Sprintf("\n🏁 <b>Tugatilgan: %d ta</b>\n", len(finished)))
			for _, bk := range finished {
				p, _ := b.bookProgress(ctx, bk.id)
				span := int(tashkentDay(*bk.finishedOn).Sub(bk.startedOn).Hours()/24) + 1
				sb.WriteString(fmt.Sprintf("• %s — %s · %d kunda · %s\n",
					bookLabel(bk), tashkentDay(*bk.finishedOn).Format("02.01"), span, growth.FormatMinutes(p.minutes)))
			}
		}
	}

	if top := b.favouriteWeekday(ctx, from, to); top != "" {
		sb.WriteString("\n" + top)
	}
	return sb.String()
}

// readingBars draws minutes per day as a sparkline with weekday initials.
func (b *Bot) readingBars(ctx context.Context, from, to time.Time) string {
	rows, err := b.pool.Query(ctx, `
		SELECT day, sum(minutes)::int FROM book_sessions
		WHERE day BETWEEN $1::date AND $2::date GROUP BY day`,
		from.Format(dateFmt), to.Format(dateFmt))
	if err != nil {
		return ""
	}
	byDay := map[string]int{}
	for rows.Next() {
		var (
			d time.Time
			m int
		)
		if rows.Scan(&d, &m) == nil {
			byDay[d.Format(dateFmt)] = m
		}
	}
	rows.Close()

	var bars []int
	var labels strings.Builder
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		bars = append(bars, byDay[d.Format(dateFmt)])
		labels.WriteString(growth.WeekdayShort(d.Weekday())[:1])
	}
	return fmt.Sprintf("<code>%s</code>\n<code>%s</code>\n", growth.Sparkline(bars), labels.String())
}

// favouriteWeekday names the weekday with the most reading — a small, true
// fact about the habit, shown once there is enough to say it.
func (b *Bot) favouriteWeekday(ctx context.Context, from, to time.Time) string {
	var (
		dow     int
		minutes int
		days    int
	)
	err := b.pool.QueryRow(ctx, `
		SELECT extract(dow FROM day)::int AS dow, sum(minutes)::int,
		       (SELECT count(DISTINCT day) FROM book_sessions WHERE day BETWEEN $1::date AND $2::date)::int
		FROM book_sessions WHERE day BETWEEN $1::date AND $2::date
		GROUP BY dow ORDER BY sum(minutes) DESC LIMIT 1`,
		from.Format(dateFmt), to.Format(dateFmt)).Scan(&dow, &minutes, &days)
	if err != nil || days < 7 {
		return ""
	}
	return fmt.Sprintf("Eng ko'p o'qiladigan kun: <b>%s</b>", growth.WeekdayName(time.Weekday(dow)))
}

/* ------------------------- lines for other reports ------------------------ */

// bookTodayLine is the /bugun line.
func (b *Bot) bookTodayLine(ctx context.Context, today time.Time) string {
	var minutes int
	var title *string
	err := b.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(s.minutes), 0)::int,
		       (SELECT bk.title FROM book_sessions s2 JOIN books bk ON bk.id = s2.book_id
		        WHERE s2.day = $1::date ORDER BY s2.created_at DESC LIMIT 1)
		FROM book_sessions s WHERE s.day = $1::date`, today.Format(dateFmt)).Scan(&minutes, &title)
	if err != nil || minutes == 0 || title == nil {
		return ""
	}
	return fmt.Sprintf("📚 Kitob: %s · «%s»", growth.FormatMinutes(minutes), escapeHTML(*title))
}

// bookDailyLine is the morning summary's reading line: yesterday's minutes and
// where the current book stands. Empty when there are no books at all.
func (b *Bot) bookDailyLine(ctx context.Context) string {
	books, err := b.readingBooks(ctx)
	if err != nil {
		return ""
	}
	yesterday := dayStart(time.Now()).AddDate(0, 0, -1)
	var minutes int
	if err := b.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(minutes), 0)::int FROM book_sessions WHERE day = $1::date`,
		yesterday.Format(dateFmt)).Scan(&minutes); err != nil {
		return ""
	}
	if len(books) == 0 && minutes == 0 {
		return ""
	}

	line := "📚 Kecha: "
	if minutes > 0 {
		line += growth.FormatMinutes(minutes)
	} else {
		line += "o'qilmagan"
	}
	if len(books) > 0 {
		bk := books[0]
		line += " · «" + escapeHTML(truncate(bk.title, 40)) + "»"
		if p, err := b.bookProgress(ctx, bk.id); err == nil && bk.totalPages != nil && p.lastPage > 0 {
			line += fmt.Sprintf(" %d%%", growth.Percent(p.lastPage, *bk.totalPages))
		}
	}
	return line + "\n"
}

// bookWeekLine is the weekly report's reading line.
func (b *Bot) bookWeekLine(ctx context.Context, from, to time.Time) string {
	var minutes, days int
	if err := b.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(minutes), 0)::int, count(DISTINCT day)::int
		FROM book_sessions WHERE day BETWEEN $1::date AND $2::date`,
		from.Format(dateFmt), to.Format(dateFmt)).Scan(&minutes, &days); err != nil || minutes == 0 {
		return ""
	}
	return fmt.Sprintf("📚 Kitob: %s · %d kun — /kitoblar\n", growth.FormatMinutes(minutes), days)
}

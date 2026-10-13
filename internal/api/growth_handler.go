package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/growth"
)

// English practice and books, for the dashboard and the Telegram Mini App.
//
// The rows are written by the bot (internal/bot/growth.go, books.go); here
// they are only read, by admin and viewer tokens alike. Raw SQL for the same
// reason as finance_handler.go: the bot owns the tables. Dates are
// "YYYY-MM-DD" calendar days in Asia/Tashkent.

/* --------------------------------- English -------------------------------- */

type growthDay struct {
	Date    string         `json:"date"`
	Minutes int            `json:"minutes"`
	ByKind  map[string]int `json:"by_kind"`
}

type growthSprint struct {
	ID          string         `json:"id"`
	Goal        string         `json:"goal"`
	StartsOn    string         `json:"starts_on"`
	EndsOn      string         `json:"ends_on"`
	Open        bool           `json:"open"`
	Outcome     *string        `json:"outcome"` // done | partial | missed | null
	ResultNote  string         `json:"result_note"`
	Minutes     int            `json:"minutes"`
	ActiveDays  int            `json:"active_days"`
	WorkingDays int            `json:"working_days"`
	Recordings  int            `json:"recordings"`
	ByKind      map[string]int `json:"by_kind"`
}

type growthScore struct {
	TakenOn   string   `json:"taken_on"`
	Kind      string   `json:"kind"`
	Overall   float64  `json:"overall"`
	Listening *float64 `json:"listening"`
	Reading   *float64 `json:"reading"`
	Writing   *float64 `json:"writing"`
	Speaking  *float64 `json:"speaking"`
	Note      string   `json:"note"`
}

type growthSummary struct {
	From        string         `json:"from"`
	To          string         `json:"to"`
	Minutes     int            `json:"minutes"`
	ActiveDays  int            `json:"active_days"`
	WorkingDays int            `json:"working_days"`
	Recordings  int            `json:"recordings"`
	ByKind      map[string]int `json:"by_kind"`
	ByDay       []growthDay    `json:"by_day"`
	// The newest sprints first; the open one, if any, is among them.
	Sprints []growthSprint `json:"sprints"`
	// Every score, oldest first — the line on the chart.
	Scores []growthScore `json:"scores"`
}

// growthRange reads from/to, defaulting to the last four weeks.
func growthRange(r *http.Request) (dateRange, error) {
	today := todayTashkent()
	q := r.URL.Query()
	if q.Get("from") == "" && q.Get("to") == "" {
		return dateRange{from: today.AddDate(0, 0, -27), to: today}, nil
	}
	return parseRange(r, "from", "to")
}

func (s *Server) handleGrowthSummary(w http.ResponseWriter, r *http.Request) {
	rng, err := growthRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.growthSummary(r.Context(), rng)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the growth log")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// minutesByDayKind reads one range as day → kind → minutes.
func (s *Server) minutesByDayKind(ctx context.Context, from, to time.Time) (map[string]map[string]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT day, kind, sum(minutes)::int FROM growth_logs
		WHERE day BETWEEN $1::date AND $2::date
		GROUP BY day, kind`, from.Format(dateLayout), to.Format(dateLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var (
			d    time.Time
			kind string
			m    int
		)
		if err := rows.Scan(&d, &kind, &m); err != nil {
			return nil, err
		}
		key := d.Format(dateLayout)
		if out[key] == nil {
			out[key] = map[string]int{}
		}
		out[key][kind] += m
	}
	return out, rows.Err()
}

func totals(days map[string]map[string]int) (perDay map[string]int, byKind map[string]int, total int) {
	perDay, byKind = map[string]int{}, map[string]int{}
	for day, kinds := range days {
		for k, m := range kinds {
			perDay[day] += m
			byKind[k] += m
			total += m
		}
	}
	return perDay, byKind, total
}

func (s *Server) growthSummary(ctx context.Context, rng dateRange) (*growthSummary, error) {
	out := &growthSummary{
		From:    rng.from.Format(dateLayout),
		To:      rng.to.Format(dateLayout),
		ByDay:   []growthDay{},
		Sprints: []growthSprint{},
		Scores:  []growthScore{},
	}

	days, err := s.minutesByDayKind(ctx, rng.from, rng.to)
	if err != nil {
		return nil, err
	}
	perDay, byKind, total := totals(days)
	out.ByKind, out.Minutes = byKind, total
	out.ActiveDays, out.WorkingDays = growth.ActiveDays(perDay, rng.from, rng.to)
	for d := rng.from; !d.After(rng.to); d = d.AddDate(0, 0, 1) {
		key := d.Format(dateLayout)
		kinds := days[key]
		if kinds == nil {
			kinds = map[string]int{}
		}
		out.ByDay = append(out.ByDay, growthDay{Date: key, Minutes: perDay[key], ByKind: kinds})
	}

	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM growth_recordings WHERE day BETWEEN $1::date AND $2::date`,
		rng.from.Format(dateLayout), rng.to.Format(dateLayout)).Scan(&out.Recordings); err != nil {
		return nil, err
	}

	if out.Sprints, err = s.growthSprints(ctx, 12); err != nil {
		return nil, err
	}
	if out.Scores, err = s.growthScores(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// growthSprints returns the newest sprints with what each produced, counted
// from its own date range up to today.
func (s *Server) growthSprints(ctx context.Context, limit int) ([]growthSprint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, goal, starts_on, ends_on, closed_at IS NULL, outcome, result_note
		FROM growth_sprints ORDER BY starts_on DESC, created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	type raw struct {
		sp           growthSprint
		starts, ends time.Time
	}
	var list []raw
	for rows.Next() {
		var x raw
		if err := rows.Scan(&x.sp.ID, &x.sp.Goal, &x.starts, &x.ends, &x.sp.Open, &x.sp.Outcome, &x.sp.ResultNote); err != nil {
			rows.Close()
			return nil, err
		}
		x.sp.StartsOn, x.sp.EndsOn = x.starts.Format(dateLayout), x.ends.Format(dateLayout)
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	today := todayTashkent()
	out := []growthSprint{}
	for _, x := range list {
		end := x.ends
		if today.Before(end) {
			end = today
		}
		x.sp.ByKind = map[string]int{}
		if !end.Before(x.starts) {
			days, err := s.minutesByDayKind(ctx, x.starts, end)
			if err != nil {
				return nil, err
			}
			perDay, byKind, total := totals(days)
			x.sp.ByKind, x.sp.Minutes = byKind, total
			x.sp.ActiveDays, x.sp.WorkingDays = growth.ActiveDays(perDay, x.starts, end)
		}
		if err := s.pool.QueryRow(ctx, `
			SELECT count(*) FROM growth_recordings WHERE day BETWEEN $1::date AND $2::date`,
			x.starts.Format(dateLayout), x.ends.Format(dateLayout)).Scan(&x.sp.Recordings); err != nil {
			return nil, err
		}
		out = append(out, x.sp)
	}
	return out, nil
}

func (s *Server) growthScores(ctx context.Context) ([]growthScore, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT taken_on, kind, overall::float8, listening::float8, reading::float8,
		       writing::float8, speaking::float8, note
		FROM growth_scores ORDER BY taken_on, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []growthScore{}
	for rows.Next() {
		var (
			sc growthScore
			d  time.Time
		)
		if err := rows.Scan(&d, &sc.Kind, &sc.Overall, &sc.Listening, &sc.Reading, &sc.Writing, &sc.Speaking, &sc.Note); err != nil {
			return nil, err
		}
		sc.TakenOn = d.Format(dateLayout)
		out = append(out, sc)
	}
	return out, rows.Err()
}

/* ---------------------------------- books --------------------------------- */

type bookJSON struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Author     string  `json:"author"`
	TotalPages *int    `json:"total_pages"`
	Status     string  `json:"status"`
	StartedOn  string  `json:"started_on"`
	FinishedOn *string `json:"finished_on"`
	Minutes    int     `json:"minutes"`
	Days       int     `json:"days"`
	Sessions   int     `json:"sessions"`
	LastPage   int     `json:"last_page"`
	Percent    int     `json:"percent"`
}

type monthMinutes struct {
	Month   string `json:"month"` // "2026-10"
	Minutes int    `json:"minutes"`
	Days    int    `json:"days"`
}

type dayMinutes struct {
	Date    string `json:"date"`
	Minutes int    `json:"minutes"`
}

type booksSummary struct {
	Year     int            `json:"year"`
	Minutes  int            `json:"minutes"`
	Days     int            `json:"days"`
	Sessions int            `json:"sessions"`
	ByMonth  []monthMinutes `json:"by_month"`
	// The last 30 days, whatever the year — the recent rhythm.
	Recent   []dayMinutes `json:"recent"`
	Reading  []bookJSON   `json:"reading"`
	Finished []bookJSON   `json:"finished"`
	Dropped  []bookJSON   `json:"dropped"`
}

const bookColumns = `
	bk.id::text, bk.title, bk.author, bk.total_pages, bk.status, bk.started_on, bk.finished_on,
	COALESCE(sum(s.minutes), 0)::int, count(DISTINCT s.day)::int, count(s.id)::int,
	COALESCE((SELECT page_reached FROM book_sessions p
	          WHERE p.book_id = bk.id AND p.page_reached IS NOT NULL
	          ORDER BY p.day DESC, p.created_at DESC LIMIT 1), 0)`

func (s *Server) loadBooks(ctx context.Context, where string, args ...any) ([]bookJSON, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+bookColumns+`
		FROM books bk LEFT JOIN book_sessions s ON s.book_id = bk.id
		WHERE `+where+`
		GROUP BY bk.id
		ORDER BY COALESCE(bk.finished_on, max(s.day), bk.started_on) DESC, bk.created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []bookJSON{}
	for rows.Next() {
		var (
			b        bookJSON
			started  time.Time
			finished *time.Time
		)
		if err := rows.Scan(&b.ID, &b.Title, &b.Author, &b.TotalPages, &b.Status, &started, &finished,
			&b.Minutes, &b.Days, &b.Sessions, &b.LastPage); err != nil {
			return nil, err
		}
		b.StartedOn = started.Format(dateLayout)
		if finished != nil {
			f := finished.Format(dateLayout)
			b.FinishedOn = &f
		}
		if b.TotalPages != nil {
			b.Percent = growth.Percent(b.LastPage, *b.TotalPages)
			if b.Status == "finished" {
				b.Percent = 100
			}
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Server) handleBooksSummary(w http.ResponseWriter, r *http.Request) {
	year := todayTashkent().Year()
	if v := r.URL.Query().Get("year"); v != "" {
		y, err := strconv.Atoi(v)
		if err != nil || y < 2000 || y > 2100 {
			writeError(w, http.StatusBadRequest, "year must be a four-digit year")
			return
		}
		year = y
	}
	out, err := s.booksSummary(r.Context(), year)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read books")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) booksSummary(ctx context.Context, year int) (*booksSummary, error) {
	from := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC)
	fromS, toS := from.Format(dateLayout), to.Format(dateLayout)
	out := &booksSummary{Year: year, ByMonth: []monthMinutes{}, Recent: []dayMinutes{}}

	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(minutes), 0)::int, count(DISTINCT day)::int, count(*)::int
		FROM book_sessions WHERE day BETWEEN $1::date AND $2::date`, fromS, toS).
		Scan(&out.Minutes, &out.Days, &out.Sessions); err != nil {
		return nil, err
	}

	byMonth := map[string]monthMinutes{}
	rows, err := s.pool.Query(ctx, `
		SELECT to_char(day, 'YYYY-MM'), sum(minutes)::int, count(DISTINCT day)::int
		FROM book_sessions WHERE day BETWEEN $1::date AND $2::date
		GROUP BY 1`, fromS, toS)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m monthMinutes
		if err := rows.Scan(&m.Month, &m.Minutes, &m.Days); err != nil {
			rows.Close()
			return nil, err
		}
		byMonth[m.Month] = m
	}
	rows.Close()
	for mo := 1; mo <= 12; mo++ {
		key := fmt.Sprintf("%04d-%02d", year, mo)
		m := byMonth[key]
		m.Month = key
		out.ByMonth = append(out.ByMonth, m)
	}

	today := todayTashkent()
	recentFrom := today.AddDate(0, 0, -29)
	perDay := map[string]int{}
	rows, err = s.pool.Query(ctx, `
		SELECT day, sum(minutes)::int FROM book_sessions
		WHERE day BETWEEN $1::date AND $2::date GROUP BY day`,
		recentFrom.Format(dateLayout), today.Format(dateLayout))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			d time.Time
			m int
		)
		if err := rows.Scan(&d, &m); err != nil {
			rows.Close()
			return nil, err
		}
		perDay[d.Format(dateLayout)] = m
	}
	rows.Close()
	for d := recentFrom; !d.After(today); d = d.AddDate(0, 0, 1) {
		key := d.Format(dateLayout)
		out.Recent = append(out.Recent, dayMinutes{Date: key, Minutes: perDay[key]})
	}

	if out.Reading, err = s.loadBooks(ctx, `bk.status = 'reading'`); err != nil {
		return nil, err
	}
	if out.Finished, err = s.loadBooks(ctx,
		`bk.status = 'finished' AND bk.finished_on BETWEEN $1::date AND $2::date`, fromS, toS); err != nil {
		return nil, err
	}
	if out.Dropped, err = s.loadBooks(ctx,
		`bk.status = 'dropped' AND bk.finished_on BETWEEN $1::date AND $2::date`, fromS, toS); err != nil {
		return nil, err
	}
	return out, nil
}

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // the runtime image has no zone database
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ibrohimcoder/portfolio-api/internal/finance"
)

// The money log, for the dashboard and the Telegram Mini App.
//
// The rows are written by the bot (internal/bot/finance.go) one chat message
// at a time; here they are read in bulk and, from the dashboard only,
// corrected. Reads are open to admin and viewer tokens alike (adminOrViewer);
// writes sit behind authMiddleware.
//
// Raw SQL, like system_handler.go: the bot owns the table and reads it the
// same way, and none of these queries is reused elsewhere.
//
// Money is always minor units (hundredths) per currency: {"UZS": 3500000}
// is 35 000 so'm. Dates are "YYYY-MM-DD" in Asia/Tashkent, never instants.

var tashkent = mustLoadLocation("Asia/Tashkent")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.FixedZone("UTC+5", 5*60*60)
	}
	return loc
}

// The longest range a summary is computed for. A daily series longer than
// two years is not a chart anyone reads.
const maxSummaryDays = 731

func todayTashkent() time.Time {
	now := time.Now().In(tashkent)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

/* --------------------------------- JSON ----------------------------------- */

type financeEntryJSON struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	AmountMinor int64     `json:"amount_minor"`
	Currency    string    `json:"currency"`
	Category    string    `json:"category"`
	Person      string    `json:"person"`
	Note        string    `json:"note"`
	RawText     string    `json:"raw_text"`
	SpentOn     string    `json:"spent_on"`
	Source      string    `json:"source"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const entryColumns = `id::text, kind, amount_minor, currency, category, person, note,
	raw_text, spent_on, source, created_at, updated_at`

func scanEntry(row pgx.Row) (financeEntryJSON, error) {
	var e financeEntryJSON
	var spentOn time.Time
	err := row.Scan(&e.ID, &e.Kind, &e.AmountMinor, &e.Currency, &e.Category, &e.Person,
		&e.Note, &e.RawText, &spentOn, &e.Source, &e.CreatedAt, &e.UpdatedAt)
	e.SpentOn = spentOn.Format(dateLayout)
	return e, err
}

type amountsJSON = finance.Amounts

func addTo(m map[string]finance.Amounts, key, currency string, minor int64) {
	if m[key] == nil {
		m[key] = finance.Amounts{}
	}
	m[key].Add(currency, minor)
}

/* --------------------------------- /meta ---------------------------------- */

func (s *Server) handleFinanceMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"kinds":      finance.Kinds,
		"categories": finance.Categories,
		"currencies": []string{"UZS", "USD"},
		"today":      todayTashkent().Format(dateLayout),
	})
}

/* -------------------------------- /summary -------------------------------- */

type dateRange struct{ from, to time.Time } // both inclusive

func (d dateRange) days() int { return int(d.to.Sub(d.from).Hours()/24) + 1 }

// args gives the range as SQL parameters: from inclusive, to exclusive.
func (d dateRange) args() (string, string) {
	return d.from.Format(dateLayout), d.to.AddDate(0, 0, 1).Format(dateLayout)
}

// parseRange reads from/to. Missing values default to this month so far.
func parseRange(r *http.Request, fromKey, toKey string) (dateRange, error) {
	today := todayTashkent()
	rng := dateRange{
		from: time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC),
		to:   today,
	}
	if v := r.URL.Query().Get(fromKey); v != "" {
		t, err := time.Parse(dateLayout, v)
		if err != nil {
			return rng, fmt.Errorf("%s must be YYYY-MM-DD", fromKey)
		}
		rng.from = t
	}
	if v := r.URL.Query().Get(toKey); v != "" {
		t, err := time.Parse(dateLayout, v)
		if err != nil {
			return rng, fmt.Errorf("%s must be YYYY-MM-DD", toKey)
		}
		rng.to = t
	}
	if rng.to.Before(rng.from) {
		return rng, fmt.Errorf("%s is before %s", toKey, fromKey)
	}
	if rng.days() > maxSummaryDays {
		return rng, fmt.Errorf("range is longer than %d days", maxSummaryDays)
	}
	return rng, nil
}

type kindTotal struct {
	Amounts amountsJSON `json:"amounts"`
	Count   int64       `json:"count"`
}

type categoryTotal struct {
	Category string      `json:"category"`
	Amounts  amountsJSON `json:"amounts"`
	Count    int64       `json:"count"`
}

type dayTotal struct {
	Date    string      `json:"date"`
	Amounts amountsJSON `json:"amounts"`
}

type monthTotal struct {
	Month   string      `json:"month"` // "2026-10"
	Amounts amountsJSON `json:"amounts"`
}

type personTotal struct {
	Person  string      `json:"person"`
	Amounts amountsJSON `json:"amounts"`
	Count   int64       `json:"count"`
}

type financeSummary struct {
	From string `json:"from"`
	To   string `json:"to"`
	Days int    `json:"days"`

	// Expense + gift: money that left and is not coming back.
	Spent      amountsJSON          `json:"spent"`
	ByKind     map[string]kindTotal `json:"by_kind"`
	ByCategory []categoryTotal      `json:"by_category"`
	ByDay      []dayTotal           `json:"by_day"`
	// Twelve calendar months ending with the month of "to".
	ByMonth       []monthTotal         `json:"by_month"`
	GiftsByPerson []personTotal        `json:"gifts_by_person"`
	Largest       []financeEntryJSON   `json:"largest"`
	Compare       *financeCompare      `json:"compare"`
	Debts         financeDebtsResponse `json:"debts"`
}

type financeCompare struct {
	From  string      `json:"from"`
	To    string      `json:"to"`
	Spent amountsJSON `json:"spent"`
}

func (s *Server) handleFinanceSummary(w http.ResponseWriter, r *http.Request) {
	rng, err := parseRange(r, "from", "to")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// What to compare against: given explicitly (the page knows that "this
	// month so far" compares with the same days of last month), or else the
	// window of the same length just before.
	cmp := dateRange{from: rng.from.AddDate(0, 0, -rng.days()), to: rng.from.AddDate(0, 0, -1)}
	if r.URL.Query().Get("compare_from") != "" {
		if cmp, err = parseRange(r, "compare_from", "compare_to"); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	out, err := s.financeSummary(r.Context(), rng, cmp)
	if err != nil {
		s.serverError(w, r, "finance summary", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) financeSummary(ctx context.Context, rng, cmp dateRange) (*financeSummary, error) {
	from, to := rng.args()
	out := &financeSummary{
		From:          rng.from.Format(dateLayout),
		To:            rng.to.Format(dateLayout),
		Days:          rng.days(),
		Spent:         finance.Amounts{},
		ByKind:        map[string]kindTotal{},
		ByCategory:    []categoryTotal{},
		ByDay:         []dayTotal{},
		ByMonth:       []monthTotal{},
		GiftsByPerson: []personTotal{},
		Largest:       []financeEntryJSON{},
	}

	// Per kind.
	rows, err := s.pool.Query(ctx, `
		SELECT kind, currency, count(*), sum(amount_minor)::bigint
		FROM finance_entries
		WHERE spent_on >= $1::date AND spent_on < $2::date
		GROUP BY kind, currency`, from, to)
	if err != nil {
		return nil, err
	}
	byKind := map[string]finance.Amounts{}
	counts := map[string]int64{}
	for rows.Next() {
		var kind, cur string
		var n, sum int64
		if err := rows.Scan(&kind, &cur, &n, &sum); err != nil {
			rows.Close()
			return nil, err
		}
		addTo(byKind, kind, cur, sum)
		counts[kind] += n
		if finance.IsSpending(kind) {
			out.Spent.Add(cur, sum)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for kind, amounts := range byKind {
		out.ByKind[kind] = kindTotal{Amounts: amounts, Count: counts[kind]}
	}

	// Per category, spending only.
	rows, err = s.pool.Query(ctx, `
		SELECT category, currency, count(*), sum(amount_minor)::bigint
		FROM finance_entries
		WHERE kind IN ('expense', 'gift')
		  AND spent_on >= $1::date AND spent_on < $2::date
		GROUP BY category, currency`, from, to)
	if err != nil {
		return nil, err
	}
	byCat := map[string]finance.Amounts{}
	catCounts := map[string]int64{}
	for rows.Next() {
		var cat, cur string
		var n, sum int64
		if err := rows.Scan(&cat, &cur, &n, &sum); err != nil {
			rows.Close()
			return nil, err
		}
		addTo(byCat, cat, cur, sum)
		catCounts[cat] += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for cat, amounts := range byCat {
		out.ByCategory = append(out.ByCategory, categoryTotal{cat, amounts, catCounts[cat]})
	}
	sort.SliceStable(out.ByCategory, func(i, j int) bool {
		a, b := out.ByCategory[i].Amounts, out.ByCategory[j].Amounts
		if a["UZS"] != b["UZS"] {
			return a["UZS"] > b["UZS"]
		}
		if a["USD"] != b["USD"] {
			return a["USD"] > b["USD"]
		}
		return out.ByCategory[i].Category < out.ByCategory[j].Category
	})

	// Per day, every day present so a chart has no gaps.
	rows, err = s.pool.Query(ctx, `
		SELECT spent_on, currency, sum(amount_minor)::bigint
		FROM finance_entries
		WHERE kind IN ('expense', 'gift')
		  AND spent_on >= $1::date AND spent_on < $2::date
		GROUP BY spent_on, currency`, from, to)
	if err != nil {
		return nil, err
	}
	byDay := map[string]finance.Amounts{}
	for rows.Next() {
		var day time.Time
		var cur string
		var sum int64
		if err := rows.Scan(&day, &cur, &sum); err != nil {
			rows.Close()
			return nil, err
		}
		addTo(byDay, day.Format(dateLayout), cur, sum)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for d := rng.from; !d.After(rng.to); d = d.AddDate(0, 0, 1) {
		key := d.Format(dateLayout)
		amounts := byDay[key]
		if amounts == nil {
			amounts = finance.Amounts{}
		}
		out.ByDay = append(out.ByDay, dayTotal{Date: key, Amounts: amounts})
	}

	// Twelve months up to the month of "to".
	monthEnd := time.Date(rng.to.Year(), rng.to.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	monthStart := monthEnd.AddDate(0, -12, 0)
	rows, err = s.pool.Query(ctx, `
		SELECT to_char(spent_on, 'YYYY-MM'), currency, sum(amount_minor)::bigint
		FROM finance_entries
		WHERE kind IN ('expense', 'gift')
		  AND spent_on >= $1::date AND spent_on < $2::date
		GROUP BY 1, currency`, monthStart.Format(dateLayout), monthEnd.Format(dateLayout))
	if err != nil {
		return nil, err
	}
	byMonth := map[string]finance.Amounts{}
	for rows.Next() {
		var month, cur string
		var sum int64
		if err := rows.Scan(&month, &cur, &sum); err != nil {
			rows.Close()
			return nil, err
		}
		addTo(byMonth, month, cur, sum)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for m := monthStart; m.Before(monthEnd); m = m.AddDate(0, 1, 0) {
		key := m.Format("2006-01")
		amounts := byMonth[key]
		if amounts == nil {
			amounts = finance.Amounts{}
		}
		out.ByMonth = append(out.ByMonth, monthTotal{Month: key, Amounts: amounts})
	}

	// Given away, per person.
	rows, err = s.pool.Query(ctx, `
		SELECT (array_agg(person ORDER BY created_at DESC))[1], currency,
		       count(*), sum(amount_minor)::bigint
		FROM finance_entries
		WHERE kind = 'gift'
		  AND spent_on >= $1::date AND spent_on < $2::date
		GROUP BY lower(person), currency
		ORDER BY 4 DESC
		LIMIT 50`, from, to)
	if err != nil {
		return nil, err
	}
	gifts := map[string]*personTotal{}
	var giftOrder []string
	for rows.Next() {
		var person, cur string
		var n, sum int64
		if err := rows.Scan(&person, &cur, &n, &sum); err != nil {
			rows.Close()
			return nil, err
		}
		key := strings.ToLower(person)
		p, ok := gifts[key]
		if !ok {
			p = &personTotal{Person: person, Amounts: finance.Amounts{}}
			gifts[key] = p
			giftOrder = append(giftOrder, key)
		}
		p.Amounts.Add(cur, sum)
		p.Count += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, key := range giftOrder {
		out.GiftsByPerson = append(out.GiftsByPerson, *gifts[key])
	}

	// The largest single spends.
	rows, err = s.pool.Query(ctx, `
		SELECT `+entryColumns+`
		FROM finance_entries
		WHERE kind IN ('expense', 'gift')
		  AND spent_on >= $1::date AND spent_on < $2::date
		ORDER BY (currency = 'UZS') DESC, amount_minor DESC
		LIMIT 5`, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out.Largest = append(out.Largest, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The comparison window: one number is enough.
	cFrom, cTo := cmp.args()
	compare := &financeCompare{
		From:  cmp.from.Format(dateLayout),
		To:    cmp.to.Format(dateLayout),
		Spent: finance.Amounts{},
	}
	rows, err = s.pool.Query(ctx, `
		SELECT currency, sum(amount_minor)::bigint
		FROM finance_entries
		WHERE kind IN ('expense', 'gift')
		  AND spent_on >= $1::date AND spent_on < $2::date
		GROUP BY currency`, cFrom, cTo)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var cur string
		var sum int64
		if err := rows.Scan(&cur, &sum); err != nil {
			rows.Close()
			return nil, err
		}
		compare.Spent.Add(cur, sum)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.Compare = compare

	// Debts are not about the range — they are where things stand now.
	debts, err := s.financeDebts(ctx, "")
	if err != nil {
		return nil, err
	}
	out.Debts = *debts

	return out, nil
}

/* -------------------------------- /entries -------------------------------- */

type financeEntriesResponse struct {
	Entries []financeEntryJSON `json:"entries"`
	Total   int64              `json:"total"`
}

// handleListFinanceEntries filters by any of: from, to, kind, category,
// currency, person (case-insensitive, exact), q (in note, person or the
// original message). Newest first.
func (s *Server) handleListFinanceEntries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	for _, key := range []string{"from", "to"} {
		v := q.Get(key)
		if v == "" {
			continue
		}
		t, err := time.Parse(dateLayout, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, key+" must be YYYY-MM-DD")
			return
		}
		if key == "from" {
			where = append(where, "spent_on >= "+arg(t.Format(dateLayout))+"::date")
		} else {
			where = append(where, "spent_on <= "+arg(t.Format(dateLayout))+"::date")
		}
	}
	if v := q.Get("kind"); v != "" {
		if _, ok := finance.KindOf(v); !ok {
			writeError(w, http.StatusBadRequest, "unknown kind")
			return
		}
		where = append(where, "kind = "+arg(v))
	}
	if v := q.Get("category"); v != "" {
		where = append(where, "category = "+arg(v))
	}
	if v := q.Get("currency"); v != "" {
		where = append(where, "currency = "+arg(strings.ToUpper(v)))
	}
	if v := strings.TrimSpace(q.Get("person")); v != "" {
		where = append(where, "lower(person) = lower("+arg(v)+")")
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		p := arg("%" + escapeLike(v) + "%")
		where = append(where, "(note ILIKE "+p+" OR person ILIKE "+p+" OR raw_text ILIKE "+p+")")
	}

	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeError(w, http.StatusBadRequest, "limit must be 1..500")
			return
		}
		limit = n
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "offset must be 0 or more")
			return
		}
		offset = n
	}

	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}

	out := financeEntriesResponse{Entries: []financeEntryJSON{}}
	if err := s.pool.QueryRow(r.Context(),
		`SELECT count(*) FROM finance_entries `+clause, args...).Scan(&out.Total); err != nil {
		s.serverError(w, r, "count finance entries", err)
		return
	}

	rows, err := s.pool.Query(r.Context(), `
		SELECT `+entryColumns+`
		FROM finance_entries `+clause+`
		ORDER BY spent_on DESC, created_at DESC
		LIMIT `+strconv.Itoa(limit)+` OFFSET `+strconv.Itoa(offset), args...)
	if err != nil {
		s.serverError(w, r, "list finance entries", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			s.serverError(w, r, "scan finance entry", err)
			return
		}
		out.Entries = append(out.Entries, e)
	}
	if err := rows.Err(); err != nil {
		s.serverError(w, r, "list finance entries", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

/* --------------------------------- /debts --------------------------------- */

type debtJSON struct {
	Person      string `json:"person"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"` // positive: they owe me
	Since       string `json:"since"`
}

type financeDebtsResponse struct {
	Balances []debtJSON  `json:"balances"`
	OwedToMe amountsJSON `json:"owed_to_me"`
	IOwe     amountsJSON `json:"i_owe"`
	People   int         `json:"people"`
}

func (s *Server) handleFinanceDebts(w http.ResponseWriter, r *http.Request) {
	out, err := s.financeDebts(r.Context(), strings.TrimSpace(r.URL.Query().Get("person")))
	if err != nil {
		s.serverError(w, r, "finance debts", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) financeDebts(ctx context.Context, person string) (*financeDebtsResponse, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT person, currency, kind, amount_minor, spent_on
		FROM finance_entries
		WHERE kind IN ('lent', 'repaid_in', 'borrowed', 'repaid_out')
		  AND person <> ''
		  AND ($1 = '' OR lower(person) = lower($1))
		ORDER BY spent_on, created_at`, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var all []finance.Row
	for rows.Next() {
		var row finance.Row
		if err := rows.Scan(&row.Person, &row.Currency, &row.Kind, &row.Minor, &row.Day); err != nil {
			return nil, err
		}
		all = append(all, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	balances := finance.Fold(all)
	owed, owe, people := finance.Totals(balances)
	out := &financeDebtsResponse{
		Balances: make([]debtJSON, 0, len(balances)),
		OwedToMe: owed,
		IOwe:     owe,
		People:   people,
	}
	for _, b := range balances {
		out.Balances = append(out.Balances, debtJSON{
			Person: b.Person, Currency: b.Currency, AmountMinor: b.Minor,
			Since: b.Since.Format(dateLayout),
		})
	}
	return out, nil
}

/* ------------------------------ admin writes ------------------------------ */

type financeEntryInput struct {
	Kind        string `json:"kind"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Category    string `json:"category"`
	Person      string `json:"person"`
	Note        string `json:"note"`
	SpentOn     string `json:"spent_on"`
}

const (
	financeMaxPerson = 100
	financeMaxNote   = 500
	// A billion so'm, in tiyin — a typo guard, not a business rule.
	financeMaxAmount = 1_000_000_000_00
)

// normalize cleans the input and applies the same rules the bot does:
// debt entries live in "qarz", everything else never does.
func (in *financeEntryInput) normalize() {
	in.Kind = strings.TrimSpace(in.Kind)
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	if in.Currency == "" {
		in.Currency = "UZS"
	}
	in.Category = strings.TrimSpace(in.Category)
	in.Person = strings.TrimSpace(in.Person)
	in.Note = strings.TrimSpace(in.Note)
	in.SpentOn = strings.TrimSpace(in.SpentOn)

	switch {
	case finance.IsDebt(in.Kind):
		in.Category = "qarz"
	case in.Category == "" || in.Category == "qarz":
		in.Category = "boshqa"
	}
}

func (in *financeEntryInput) validate(today time.Time) error {
	if _, ok := finance.KindOf(in.Kind); !ok {
		return errors.New("kind is not one of the known kinds")
	}
	if in.AmountMinor <= 0 || in.AmountMinor > financeMaxAmount {
		return errors.New("amount_minor must be positive and at most 1e11")
	}
	if !finance.IsCurrency(in.Currency) {
		return errors.New("currency must be UZS or USD")
	}
	if !finance.IsCategory(in.Category) {
		return errors.New("category is not one of the known categories")
	}
	if finance.IsDebt(in.Kind) && in.Person == "" {
		return errors.New("person is required for debt entries")
	}
	if utf8.RuneCountInString(in.Person) > financeMaxPerson {
		return errors.New("person is too long")
	}
	if utf8.RuneCountInString(in.Note) > financeMaxNote {
		return errors.New("note is too long")
	}
	day, err := time.Parse(dateLayout, in.SpentOn)
	if err != nil {
		return errors.New("spent_on must be YYYY-MM-DD")
	}
	if day.After(today.AddDate(0, 0, 1)) {
		return errors.New("spent_on is in the future")
	}
	return nil
}

func (s *Server) handleCreateFinanceEntry(w http.ResponseWriter, r *http.Request) {
	var in financeEntryInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.normalize()
	if err := in.validate(todayTashkent()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	e, err := scanEntry(s.pool.QueryRow(r.Context(), `
		INSERT INTO finance_entries
		    (kind, amount_minor, currency, category, person, note, spent_on, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7::date, 'admin')
		RETURNING `+entryColumns,
		in.Kind, in.AmountMinor, in.Currency, in.Category, in.Person, in.Note, in.SpentOn))
	if err != nil {
		s.serverError(w, r, "create finance entry", err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (s *Server) handleUpdateFinanceEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r)
	if !ok {
		return
	}
	var in financeEntryInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.normalize()
	if err := in.validate(todayTashkent()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// raw_text and source are left alone: they record where the row came
	// from, and an edit does not change that.
	e, err := scanEntry(s.pool.QueryRow(r.Context(), `
		UPDATE finance_entries
		SET kind = $2, amount_minor = $3, currency = $4, category = $5,
		    person = $6, note = $7, spent_on = $8::date, updated_at = now()
		WHERE id = $1::uuid
		RETURNING `+entryColumns,
		id, in.Kind, in.AmountMinor, in.Currency, in.Category, in.Person, in.Note, in.SpentOn))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "entry not found")
		return
	}
	if err != nil {
		s.serverError(w, r, "update finance entry", err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) handleDeleteFinanceEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r)
	if !ok {
		return
	}
	tag, err := s.pool.Exec(r.Context(), `DELETE FROM finance_entries WHERE id = $1::uuid`, id)
	if err != nil {
		s.serverError(w, r, "delete finance entry", err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "entry not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseUUIDParam validates {id} before it reaches SQL, so a malformed id is a
// 400 rather than a cast error from PostgreSQL.
func parseUUIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if len(id) != 36 || strings.Trim(strings.ToLower(id), "0123456789abcdef-") != "" {
		writeError(w, http.StatusBadRequest, "invalid id")
		return "", false
	}
	return id, true
}

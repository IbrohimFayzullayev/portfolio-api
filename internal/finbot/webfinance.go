package finbot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/ibrohimcoder/portfolio-api/internal/finance"
)

// /api/finance/*: the same JSON as the portfolio API's /finance routes
// (internal/api/finance_handler.go, finance_balance.go), for one user.
// Money is minor units per currency; dates are "YYYY-MM-DD" in Tashkent.
// Every statement filters on user_id = $1.

const (
	dateLayout     = "2006-01-02"
	maxSummaryDays = 731
	maxPerson      = 100
	maxNote        = 500
	// A billion so'm, in tiyin: a typo guard, not a business rule.
	maxAmount = 1_000_000_000_00
)

func todayTashkent(now time.Time) time.Time {
	t := now.In(tashkent)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

type entryJSON struct {
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

func scanEntryJSON(row pgx.Row) (entryJSON, error) {
	var e entryJSON
	var spentOn time.Time
	err := row.Scan(&e.ID, &e.Kind, &e.AmountMinor, &e.Currency, &e.Category, &e.Person,
		&e.Note, &e.RawText, &spentOn, &e.Source, &e.CreatedAt, &e.UpdatedAt)
	e.SpentOn = spentOn.Format(dateLayout)
	return e, err
}

func addTo(m map[string]finance.Amounts, key, currency string, minor int64) {
	if m[key] == nil {
		m[key] = finance.Amounts{}
	}
	m[key].Add(currency, minor)
}

/* --------------------------------- /meta ---------------------------------- */

func (b *Bot) handleMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"kinds":             finance.Kinds,
		"categories":        finance.Categories,
		"income_categories": finance.IncomeCategories,
		"currencies":        []string{"UZS", "USD"},
		"today":             todayTashkent(b.now()).Format(dateLayout),
	})
}

/* -------------------------------- /summary -------------------------------- */

type dateRange struct{ from, to time.Time } // both inclusive

func (d dateRange) days() int { return int(d.to.Sub(d.from).Hours()/24) + 1 }

func (d dateRange) args() (string, string) {
	return d.from.Format(dateLayout), d.to.AddDate(0, 0, 1).Format(dateLayout)
}

func parseRange(r *http.Request, fromKey, toKey string, today time.Time) (dateRange, error) {
	rng := dateRange{from: time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC), to: today}
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
	Amounts finance.Amounts `json:"amounts"`
	Count   int64           `json:"count"`
}

type categoryTotal struct {
	Category string          `json:"category"`
	Amounts  finance.Amounts `json:"amounts"`
	Count    int64           `json:"count"`
}

type dayTotal struct {
	Date    string          `json:"date"`
	Amounts finance.Amounts `json:"amounts"`
}

type monthTotal struct {
	Month   string          `json:"month"`
	Amounts finance.Amounts `json:"amounts"`
	Income  finance.Amounts `json:"income"`
}

type personTotal struct {
	Person  string          `json:"person"`
	Amounts finance.Amounts `json:"amounts"`
	Count   int64           `json:"count"`
}

type summaryJSON struct {
	From          string               `json:"from"`
	To            string               `json:"to"`
	Days          int                  `json:"days"`
	Spent         finance.Amounts      `json:"spent"`
	Income        finance.Amounts      `json:"income"`
	Net           finance.Amounts      `json:"net"`
	Balance       balanceResponse      `json:"balance"`
	BalanceSeries []balancePoint       `json:"balance_series"`
	ByKind        map[string]kindTotal `json:"by_kind"`
	ByCategory    []categoryTotal      `json:"by_category"`
	ByDay         []dayTotal           `json:"by_day"`
	ByMonth       []monthTotal         `json:"by_month"`
	GiftsByPerson []personTotal        `json:"gifts_by_person"`
	Largest       []entryJSON          `json:"largest"`
	Compare       *compareJSON         `json:"compare"`
	Debts         debtsResponse        `json:"debts"`
}

type compareJSON struct {
	From  string          `json:"from"`
	To    string          `json:"to"`
	Spent finance.Amounts `json:"spent"`
}

func (b *Bot) handleSummary(w http.ResponseWriter, r *http.Request) {
	today := todayTashkent(b.now())
	rng, err := parseRange(r, "from", "to", today)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cmp := dateRange{from: rng.from.AddDate(0, 0, -rng.days()), to: rng.from.AddDate(0, 0, -1)}
	if r.URL.Query().Get("compare_from") != "" {
		if cmp, err = parseRange(r, "compare_from", "compare_to", today); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	out, err := b.summary(r.Context(), currentUser(r).ID, rng, cmp)
	if err != nil {
		serverError(w, "summary", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// sumRows runs a grouping query and hands each row to fn.
func (b *Bot) sumRows(ctx context.Context, sql string, args []any, fn func(pgx.Rows) error) error {
	rows, err := b.pool.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (b *Bot) summary(ctx context.Context, uid int64, rng, cmp dateRange) (*summaryJSON, error) {
	from, to := rng.args()
	out := &summaryJSON{
		From: rng.from.Format(dateLayout), To: rng.to.Format(dateLayout), Days: rng.days(),
		Spent: finance.Amounts{}, Income: finance.Amounts{}, Net: finance.Amounts{},
		ByKind: map[string]kindTotal{}, ByCategory: []categoryTotal{}, ByDay: []dayTotal{},
		ByMonth: []monthTotal{}, GiftsByPerson: []personTotal{}, Largest: []entryJSON{},
	}

	byKind := map[string]finance.Amounts{}
	counts := map[string]int64{}
	if err := b.sumRows(ctx, `
		SELECT kind, currency, count(*), sum(amount_minor)::bigint
		FROM entries
		WHERE user_id = $1 AND spent_on >= $2::date AND spent_on < $3::date
		GROUP BY kind, currency`, []any{uid, from, to}, func(rows pgx.Rows) error {
		var kind, cur string
		var n, sum int64
		if err := rows.Scan(&kind, &cur, &n, &sum); err != nil {
			return err
		}
		addTo(byKind, kind, cur, sum)
		counts[kind] += n
		if finance.IsSpending(kind) {
			out.Spent.Add(cur, sum)
			out.Net.Add(cur, -sum)
		}
		if finance.IsIncome(kind) {
			out.Income.Add(cur, sum)
			out.Net.Add(cur, sum)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for kind, amounts := range byKind {
		out.ByKind[kind] = kindTotal{Amounts: amounts, Count: counts[kind]}
	}

	byCat := map[string]finance.Amounts{}
	catCounts := map[string]int64{}
	if err := b.sumRows(ctx, `
		SELECT category, currency, count(*), sum(amount_minor)::bigint
		FROM entries
		WHERE user_id = $1 AND kind IN ('expense', 'gift')
		  AND spent_on >= $2::date AND spent_on < $3::date
		GROUP BY category, currency`, []any{uid, from, to}, func(rows pgx.Rows) error {
		var cat, cur string
		var n, sum int64
		if err := rows.Scan(&cat, &cur, &n, &sum); err != nil {
			return err
		}
		addTo(byCat, cat, cur, sum)
		catCounts[cat] += n
		return nil
	}); err != nil {
		return nil, err
	}
	for cat, amounts := range byCat {
		out.ByCategory = append(out.ByCategory, categoryTotal{cat, amounts, catCounts[cat]})
	}
	sort.SliceStable(out.ByCategory, func(i, j int) bool {
		a, c := out.ByCategory[i].Amounts, out.ByCategory[j].Amounts
		if a["UZS"] != c["UZS"] {
			return a["UZS"] > c["UZS"]
		}
		if a["USD"] != c["USD"] {
			return a["USD"] > c["USD"]
		}
		return out.ByCategory[i].Category < out.ByCategory[j].Category
	})

	byDay := map[string]finance.Amounts{}
	if err := b.sumRows(ctx, `
		SELECT spent_on, currency, sum(amount_minor)::bigint
		FROM entries
		WHERE user_id = $1 AND kind IN ('expense', 'gift')
		  AND spent_on >= $2::date AND spent_on < $3::date
		GROUP BY spent_on, currency`, []any{uid, from, to}, func(rows pgx.Rows) error {
		var day time.Time
		var cur string
		var sum int64
		if err := rows.Scan(&day, &cur, &sum); err != nil {
			return err
		}
		addTo(byDay, day.Format(dateLayout), cur, sum)
		return nil
	}); err != nil {
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

	monthEnd := time.Date(rng.to.Year(), rng.to.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	monthStart := monthEnd.AddDate(0, -12, 0)
	byMonth := map[string]finance.Amounts{}
	incomeByMonth := map[string]finance.Amounts{}
	if err := b.sumRows(ctx, `
		SELECT to_char(spent_on, 'YYYY-MM'), currency, kind = 'income', sum(amount_minor)::bigint
		FROM entries
		WHERE user_id = $1 AND kind IN ('expense', 'gift', 'income')
		  AND spent_on >= $2::date AND spent_on < $3::date
		GROUP BY 1, 2, 3`, []any{uid, monthStart.Format(dateLayout), monthEnd.Format(dateLayout)},
		func(rows pgx.Rows) error {
			var month, cur string
			var isIncome bool
			var sum int64
			if err := rows.Scan(&month, &cur, &isIncome, &sum); err != nil {
				return err
			}
			if isIncome {
				addTo(incomeByMonth, month, cur, sum)
			} else {
				addTo(byMonth, month, cur, sum)
			}
			return nil
		}); err != nil {
		return nil, err
	}
	for m := monthStart; m.Before(monthEnd); m = m.AddDate(0, 1, 0) {
		key := m.Format("2006-01")
		amounts, income := byMonth[key], incomeByMonth[key]
		if amounts == nil {
			amounts = finance.Amounts{}
		}
		if income == nil {
			income = finance.Amounts{}
		}
		out.ByMonth = append(out.ByMonth, monthTotal{Month: key, Amounts: amounts, Income: income})
	}

	gifts := map[string]*personTotal{}
	var giftOrder []string
	if err := b.sumRows(ctx, `
		SELECT (array_agg(person ORDER BY created_at DESC))[1], currency, count(*), sum(amount_minor)::bigint
		FROM entries
		WHERE user_id = $1 AND kind = 'gift'
		  AND spent_on >= $2::date AND spent_on < $3::date
		GROUP BY lower(person), currency
		ORDER BY 4 DESC
		LIMIT 50`, []any{uid, from, to}, func(rows pgx.Rows) error {
		var person, cur string
		var n, sum int64
		if err := rows.Scan(&person, &cur, &n, &sum); err != nil {
			return err
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
		return nil
	}); err != nil {
		return nil, err
	}
	for _, key := range giftOrder {
		out.GiftsByPerson = append(out.GiftsByPerson, *gifts[key])
	}

	if err := b.sumRows(ctx, `
		SELECT `+entryColumns+`
		FROM entries
		WHERE user_id = $1 AND kind IN ('expense', 'gift')
		  AND spent_on >= $2::date AND spent_on < $3::date
		ORDER BY (currency = 'UZS') DESC, amount_minor DESC
		LIMIT 5`, []any{uid, from, to}, func(rows pgx.Rows) error {
		e, err := scanEntryJSON(rows)
		if err != nil {
			return err
		}
		out.Largest = append(out.Largest, e)
		return nil
	}); err != nil {
		return nil, err
	}

	cFrom, cTo := cmp.args()
	compare := &compareJSON{From: cmp.from.Format(dateLayout), To: cmp.to.Format(dateLayout), Spent: finance.Amounts{}}
	if err := b.sumRows(ctx, `
		SELECT currency, sum(amount_minor)::bigint
		FROM entries
		WHERE user_id = $1 AND kind IN ('expense', 'gift')
		  AND spent_on >= $2::date AND spent_on < $3::date
		GROUP BY currency`, []any{uid, cFrom, cTo}, func(rows pgx.Rows) error {
		var cur string
		var sum int64
		if err := rows.Scan(&cur, &sum); err != nil {
			return err
		}
		compare.Spent.Add(cur, sum)
		return nil
	}); err != nil {
		return nil, err
	}
	out.Compare = compare

	debts, err := b.debts(ctx, uid, "")
	if err != nil {
		return nil, err
	}
	out.Debts = *debts

	balance, series, err := b.balanceWithSeries(ctx, uid, rng, debts)
	if err != nil {
		return nil, err
	}
	out.Balance, out.BalanceSeries = *balance, series
	return out, nil
}

/* -------------------------------- /entries -------------------------------- */

type entriesResponse struct {
	Entries []entryJSON `json:"entries"`
	Total   int64       `json:"total"`
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// handleListEntries filters by any of: from, to, kind, category, currency,
// person (case-insensitive, exact), q (in note, person or the original
// message). Newest first.
func (b *Bot) handleListEntries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	args := []any{currentUser(r).ID}
	where := []string{"user_id = $1"}
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
	clause := "WHERE " + strings.Join(where, " AND ")

	out := entriesResponse{Entries: []entryJSON{}}
	if err := b.pool.QueryRow(r.Context(), `SELECT count(*) FROM entries `+clause, args...).Scan(&out.Total); err != nil {
		serverError(w, "count entries", err)
		return
	}
	if err := b.sumRows(r.Context(), `
		SELECT `+entryColumns+` FROM entries `+clause+`
		ORDER BY spent_on DESC, created_at DESC
		LIMIT `+strconv.Itoa(limit)+` OFFSET `+strconv.Itoa(offset), args, func(rows pgx.Rows) error {
		e, err := scanEntryJSON(rows)
		if err != nil {
			return err
		}
		out.Entries = append(out.Entries, e)
		return nil
	}); err != nil {
		serverError(w, "list entries", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type entryInput struct {
	Kind        string `json:"kind"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Category    string `json:"category"`
	Person      string `json:"person"`
	Note        string `json:"note"`
	SpentOn     string `json:"spent_on"`
}

// normalize applies the bot's rules: debt rows live in "qarz", everything
// else never does; a category that does not fit falls back to the default.
func (in *entryInput) normalize() {
	in.Kind = strings.TrimSpace(in.Kind)
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	if in.Currency == "" {
		in.Currency = "UZS"
	}
	in.Category = strings.TrimSpace(in.Category)
	in.Person = strings.TrimSpace(in.Person)
	in.Note = strings.TrimSpace(in.Note)
	in.SpentOn = strings.TrimSpace(in.SpentOn)
	known := in.Category == "" || finance.IsCategory(in.Category)
	if known && !finance.CategoryFits(in.Kind, in.Category) {
		in.Category = finance.DefaultCategory(in.Kind)
	}
}

func (in *entryInput) validate(today time.Time) error {
	if _, ok := finance.KindOf(in.Kind); !ok {
		return errors.New("kind is not one of the known kinds")
	}
	if in.AmountMinor <= 0 || in.AmountMinor > maxAmount {
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
	if utf8.RuneCountInString(in.Person) > maxPerson {
		return errors.New("person is too long")
	}
	if utf8.RuneCountInString(in.Note) > maxNote {
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

func (b *Bot) readEntryInput(w http.ResponseWriter, r *http.Request) (entryInput, bool) {
	var in entryInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return in, false
	}
	in.normalize()
	if err := in.validate(todayTashkent(b.now())); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return in, false
	}
	return in, true
}

func (b *Bot) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	in, ok := b.readEntryInput(w, r)
	if !ok {
		return
	}
	e, err := scanEntryJSON(b.pool.QueryRow(r.Context(), `
		INSERT INTO entries (user_id, kind, amount_minor, currency, category, person, note, spent_on, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::date, 'web')
		RETURNING `+entryColumns,
		currentUser(r).ID, in.Kind, in.AmountMinor, in.Currency, in.Category, in.Person, in.Note, in.SpentOn))
	if err != nil {
		serverError(w, "create entry", err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (b *Bot) handleUpdateEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	in, ok := b.readEntryInput(w, r)
	if !ok {
		return
	}
	e, err := scanEntryJSON(b.pool.QueryRow(r.Context(), `
		UPDATE entries
		SET kind = $3, amount_minor = $4, currency = $5, category = $6,
		    person = $7, note = $8, spent_on = $9::date, updated_at = now()
		WHERE id = $2::uuid AND user_id = $1
		RETURNING `+entryColumns,
		currentUser(r).ID, id, in.Kind, in.AmountMinor, in.Currency, in.Category, in.Person, in.Note, in.SpentOn))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "entry not found")
		return
	}
	if err != nil {
		serverError(w, "update entry", err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (b *Bot) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	tag, err := b.pool.Exec(r.Context(), `DELETE FROM entries WHERE id = $2::uuid AND user_id = $1`,
		currentUser(r).ID, id)
	if err != nil {
		serverError(w, "delete entry", err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "entry not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

/* --------------------------------- /debts --------------------------------- */

type debtJSON struct {
	Person      string `json:"person"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
	Since       string `json:"since"`
}

type debtsResponse struct {
	Balances []debtJSON      `json:"balances"`
	OwedToMe finance.Amounts `json:"owed_to_me"`
	IOwe     finance.Amounts `json:"i_owe"`
	People   int             `json:"people"`
}

func (b *Bot) handleDebts(w http.ResponseWriter, r *http.Request) {
	out, err := b.debts(r.Context(), currentUser(r).ID, strings.TrimSpace(r.URL.Query().Get("person")))
	if err != nil {
		serverError(w, "debts", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (b *Bot) debts(ctx context.Context, uid int64, person string) (*debtsResponse, error) {
	balances, err := debtBalances(ctx, b.pool, uid, person)
	if err != nil {
		return nil, err
	}
	owed, owe, people := finance.Totals(balances)
	out := &debtsResponse{Balances: make([]debtJSON, 0, len(balances)), OwedToMe: owed, IOwe: owe, People: people}
	for _, d := range balances {
		out.Balances = append(out.Balances, debtJSON{
			Person: d.Person, Currency: d.Currency, AmountMinor: d.Minor, Since: d.Since.Format(dateLayout),
		})
	}
	return out, nil
}

/* -------------------------------- balance --------------------------------- */

type balanceJSON struct {
	Currency    string              `json:"currency"`
	AmountMinor int64               `json:"amount_minor"`
	Checkpoint  *finance.Checkpoint `json:"checkpoint"`
	In          int64               `json:"in"`
	Out         int64               `json:"out"`
}

type balanceResponse struct {
	Balances []balanceJSON   `json:"balances"`
	InHand   finance.Amounts `json:"in_hand"`
	NetWorth finance.Amounts `json:"net_worth"`
}

type balancePoint struct {
	Date        string `json:"date"`
	AmountMinor *int64 `json:"amount_minor"`
}

func (b *Bot) loadCheckpoints(ctx context.Context, uid int64, currency string) ([]finance.Checkpoint, error) {
	var out []finance.Checkpoint
	err := b.sumRows(ctx, `
		SELECT id::text, currency, amount_minor, taken_at, computed_minor, note
		FROM checkpoints
		WHERE user_id = $1 AND ($2 = '' OR currency = $2)
		ORDER BY taken_at, created_at`, []any{uid, currency}, func(rows pgx.Rows) error {
		var cp finance.Checkpoint
		if err := rows.Scan(&cp.ID, &cp.Currency, &cp.Minor, &cp.TakenAt, &cp.Computed, &cp.Note); err != nil {
			return err
		}
		out = append(out, cp)
		return nil
	})
	return out, err
}

func (b *Bot) balanceWithSeries(ctx context.Context, uid int64, rng dateRange, debts *debtsResponse) (*balanceResponse, []balancePoint, error) {
	out := &balanceResponse{Balances: []balanceJSON{}, InHand: finance.Amounts{}, NetWorth: finance.Amounts{}}
	series := []balancePoint{}

	cps, err := b.loadCheckpoints(ctx, uid, "")
	if err != nil {
		return nil, nil, err
	}
	if len(cps) == 0 {
		return out, series, nil
	}
	earliest := cps[0].TakenAt
	for _, cp := range cps {
		if cp.TakenAt.Before(earliest) {
			earliest = cp.TakenAt
		}
	}
	flows, err := loadFlows(ctx, b.pool, uid, "", earliest)
	if err != nil {
		return nil, nil, err
	}
	latest := map[string]finance.Checkpoint{}
	for _, cp := range cps {
		latest[cp.Currency] = cp
	}
	for _, cur := range []string{"UZS", "USD"} {
		cp, ok := latest[cur]
		if !ok {
			continue
		}
		minor, in, outflow := finance.Since(cp, flows, tashkent)
		cpCopy := cp
		out.Balances = append(out.Balances, balanceJSON{Currency: cur, AmountMinor: minor, Checkpoint: &cpCopy, In: in, Out: outflow})
		out.InHand.Add(cur, minor)
		out.NetWorth.Add(cur, minor)
	}
	for cur, v := range debts.OwedToMe {
		out.NetWorth.Add(cur, v)
	}
	for cur, v := range debts.IOwe {
		out.NetWorth.Add(cur, -v)
	}
	for _, p := range finance.Series(cps, flows, "UZS", rng.from, rng.to, tashkent) {
		bp := balancePoint{Date: p.Day.Format(dateLayout)}
		if p.Known {
			v := p.Minor
			bp.AmountMinor = &v
		}
		series = append(series, bp)
	}
	return out, series, nil
}

func (b *Bot) handleListCheckpoints(w http.ResponseWriter, r *http.Request) {
	cps, err := b.loadCheckpoints(r.Context(), currentUser(r).ID, "")
	if err != nil {
		serverError(w, "list checkpoints", err)
		return
	}
	out := make([]finance.Checkpoint, 0, len(cps))
	for i := len(cps) - 1; i >= 0 && len(out) < 50; i-- {
		out = append(out, cps[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"checkpoints": out})
}

func (b *Bot) handleCreateCheckpoint(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Currency    string `json:"currency"`
		AmountMinor int64  `json:"amount_minor"`
		Date        string `json:"date"`
		Note        string `json:"note"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	if in.Currency == "" {
		in.Currency = "UZS"
	}
	in.Note = strings.TrimSpace(in.Note)
	if !finance.IsCurrency(in.Currency) {
		writeError(w, http.StatusBadRequest, "currency must be UZS or USD")
		return
	}
	if finance.Abs(in.AmountMinor) > maxAmount*10 {
		writeError(w, http.StatusBadRequest, "amount_minor is out of range")
		return
	}
	if utf8.RuneCountInString(in.Note) > maxNote {
		writeError(w, http.StatusBadRequest, "note is too long")
		return
	}
	takenAt := b.now()
	if in.Date != "" {
		d, err := time.ParseInLocation(dateLayout, in.Date, tashkent)
		if err != nil {
			writeError(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
			return
		}
		if d.After(takenAt) {
			writeError(w, http.StatusBadRequest, "date is in the future")
			return
		}
		takenAt = d
	}
	uid := currentUser(r).ID
	computed, err := b.computedAt(r.Context(), uid, in.Currency, takenAt)
	if err != nil {
		serverError(w, "compute balance", err)
		return
	}
	var cp finance.Checkpoint
	err = b.pool.QueryRow(r.Context(), `
		INSERT INTO checkpoints (user_id, currency, amount_minor, taken_at, computed_minor, note, source)
		VALUES ($1, $2, $3, $4, $5, $6, 'web')
		RETURNING id::text, currency, amount_minor, taken_at, computed_minor, note`,
		uid, in.Currency, in.AmountMinor, takenAt, computed, in.Note,
	).Scan(&cp.ID, &cp.Currency, &cp.Minor, &cp.TakenAt, &cp.Computed, &cp.Note)
	if err != nil {
		serverError(w, "create checkpoint", err)
		return
	}
	writeJSON(w, http.StatusCreated, cp)
}

// computedAt is what the rows say the balance was at a moment: the latest
// checkpoint before it plus the rows between. Nil when there is none.
func (b *Bot) computedAt(ctx context.Context, uid int64, currency string, at time.Time) (*int64, error) {
	cps, err := b.loadCheckpoints(ctx, uid, currency)
	if err != nil {
		return nil, err
	}
	var prev *finance.Checkpoint
	for i := range cps {
		if cps[i].TakenAt.Before(at) {
			prev = &cps[i]
		}
	}
	if prev == nil {
		return nil, nil
	}
	flows, err := loadFlows(ctx, b.pool, uid, currency, prev.TakenAt)
	if err != nil {
		return nil, err
	}
	next := finance.Checkpoint{Currency: currency, TakenAt: at}
	var between []finance.Flow
	for _, f := range flows {
		if next.Covers(f, tashkent) {
			between = append(between, f)
		}
	}
	v, _, _ := finance.Since(*prev, between, tashkent)
	return &v, nil
}

func (b *Bot) handleDeleteCheckpoint(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	tag, err := b.pool.Exec(r.Context(), `DELETE FROM checkpoints WHERE id = $2::uuid AND user_id = $1`,
		currentUser(r).ID, id)
	if err != nil {
		serverError(w, "delete checkpoint", err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "checkpoint not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package finbot

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ibrohimcoder/portfolio-api/internal/finance"
	"github.com/ibrohimcoder/portfolio-api/internal/financetext"
)

// The admin's view of the finance bot, for the portfolio dashboard.
//
// Only the portfolio API calls these routes: it checks the dashboard session
// and forwards the request over the Docker network with the shared key
// (FINBOT_ADMIN_KEY). The finance database is still read by this process
// alone. Caddy routes only /api/* and /healthz here, so /internal is never
// reachable from outside; the key is a second lock, not the only one.
//
// What it shows:
//   - everyone who pressed /start, with status and how they use the bot:
//     entries, active days, corrections, messages the bot could not read;
//   - with AdminSeesMoney, also amounts, and each user's full report through
//     the same /finance JSON the Mini App uses - read-only.

const adminKeyHeader = "X-Finbot-Admin-Key"

func (b *Bot) mountAdmin(mux *http.ServeMux) {
	if b.cfg.AdminKey == "" {
		return
	}
	admin := func(h http.HandlerFunc) http.Handler { return b.requireAdminKey(h) }
	mux.Handle("GET /internal/admin/users", admin(b.handleAdminUsers))
	mux.Handle("GET /internal/admin/users/{id}", admin(b.handleAdminUser))
	mux.Handle("POST /internal/admin/users/{id}/status", admin(b.handleAdminStatus))

	// One user's money, read-only: the Mini App's handlers with that user put
	// in the context, so the admin sees exactly what they see.
	money := func(h http.HandlerFunc) http.Handler { return admin(b.asUser(h)) }
	mux.Handle("GET /internal/admin/users/{id}/finance/meta", money(b.handleMeta))
	mux.Handle("GET /internal/admin/users/{id}/finance/summary", money(b.handleSummary))
	mux.Handle("GET /internal/admin/users/{id}/finance/entries", money(b.handleListEntries))
	mux.Handle("GET /internal/admin/users/{id}/finance/debts", money(b.handleDebts))
	mux.Handle("GET /internal/admin/users/{id}/finance/checkpoints", money(b.handleListCheckpoints))
}

func (b *Bot) requireAdminKey(next http.HandlerFunc) http.Handler {
	want := []byte(b.cfg.AdminKey)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get(adminKeyHeader))
		if len(got) == 0 || subtle.ConstantTimeCompare(got, want) != 1 {
			writeError(w, http.StatusUnauthorized, "admin key required")
			return
		}
		next(w, r)
	})
}

func (b *Bot) userByID(ctx context.Context, raw string) (*user, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, pgx.ErrNoRows
	}
	return scanUser(b.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

// asUser puts the user named in the path into the context, for the money
// handlers - only when the admin may see money at all.
func (b *Bot) asUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !b.cfg.AdminSeesMoney {
			writeError(w, http.StatusForbidden, "amounts are hidden from the admin (FINBOT_ADMIN_SEES_MONEY=false)")
			return
		}
		u, err := b.userByID(r.Context(), r.PathValue("id"))
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		if err != nil {
			serverError(w, "admin user lookup", err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	}
}

/* ---------------------------------- users --------------------------------- */

type adminMoneyJSON struct {
	// This calendar month, Tashkent.
	MonthSpent  finance.Amounts `json:"month_spent"`
	MonthIncome finance.Amounts `json:"month_income"`
	// Nil before the first /balans.
	InHand finance.Amounts `json:"in_hand"`
	// Counted minus computed at the latest recount, so'm: money that moved
	// without being written down. Large and negative = forgets expenses.
	LastGapMinor *int64 `json:"last_gap_minor"`
}

type adminUserJSON struct {
	ID           int64      `json:"id"`
	TelegramID   int64      `json:"telegram_id"`
	FirstName    string     `json:"first_name"`
	Username     string     `json:"username"`
	Status       string     `json:"status"`
	JoinedVia    string     `json:"joined_via"`
	DailySummary bool       `json:"daily_summary"`
	CreatedAt    time.Time  `json:"created_at"`
	ApprovedAt   *time.Time `json:"approved_at"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	LastEntryAt  *time.Time `json:"last_entry_at"`

	Entries      int64 `json:"entries"`
	Entries7d    int64 `json:"entries_7d"`
	Entries30d   int64 `json:"entries_30d"`
	ActiveDays30 int64 `json:"active_days_30"`
	// Rows changed after they were written: the parser guessed wrong, or
	// the person changed their mind.
	Corrected   int64 `json:"corrected"`
	Failures30d int64 `json:"failures_30d"`
	Checkpoints int64 `json:"checkpoints"`

	Money *adminMoneyJSON `json:"money"`
}

type adminCountsJSON struct {
	Total    int `json:"total"`
	Active   int `json:"active"`
	Pending  int `json:"pending"`
	Blocked  int `json:"blocked"`
	Active7d int `json:"active_7d"` // active and wrote something in 7 days
}

const adminUserQuery = `
	SELECT u.id, u.telegram_id, u.first_name, u.username, u.status, u.joined_via,
	       u.daily_summary, u.created_at, u.approved_at, u.last_seen_at,
	       max(e.created_at),
	       count(e.id),
	       count(e.id) FILTER (WHERE e.created_at > now() - interval '7 days'),
	       count(e.id) FILTER (WHERE e.created_at > now() - interval '30 days'),
	       count(DISTINCT (e.created_at AT TIME ZONE 'Asia/Tashkent')::date)
	             FILTER (WHERE e.created_at > now() - interval '30 days'),
	       count(e.id) FILTER (WHERE e.updated_at > e.created_at + interval '2 seconds'),
	       (SELECT count(*) FROM parse_failures f
	         WHERE f.user_id = u.id AND f.created_at > now() - interval '30 days'),
	       (SELECT count(*) FROM checkpoints c WHERE c.user_id = u.id)
	FROM users u
	LEFT JOIN entries e ON e.user_id = u.id`

func scanAdminUser(row pgx.Row) (adminUserJSON, error) {
	var a adminUserJSON
	err := row.Scan(&a.ID, &a.TelegramID, &a.FirstName, &a.Username, &a.Status, &a.JoinedVia,
		&a.DailySummary, &a.CreatedAt, &a.ApprovedAt, &a.LastSeenAt, &a.LastEntryAt,
		&a.Entries, &a.Entries7d, &a.Entries30d, &a.ActiveDays30, &a.Corrected,
		&a.Failures30d, &a.Checkpoints)
	return a, err
}

func (b *Bot) adminMoney(ctx context.Context, a adminUserJSON) (*adminMoneyJSON, error) {
	if !b.cfg.AdminSeesMoney {
		return nil, nil
	}
	today := financetext.DayStart(b.now())
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
	u := &user{ID: a.ID}
	m := &adminMoneyJSON{MonthSpent: finance.Amounts{}, MonthIncome: finance.Amounts{}}

	spent, _, err := b.spentBetween(ctx, u, monthStart, today.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	income, err := b.incomeBetween(ctx, u, monthStart, today.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	for cur, v := range spent {
		m.MonthSpent.Add(cur, v)
	}
	for cur, v := range income {
		m.MonthIncome.Add(cur, v)
	}
	states, err := b.balances(ctx, u)
	if err != nil {
		return nil, err
	}
	if len(states) > 0 {
		m.InHand = finance.Amounts{}
		for _, st := range states {
			m.InHand.Add(st.currency, st.minor)
		}
	}
	var gap int64
	err = b.pool.QueryRow(ctx, `
		SELECT amount_minor - computed_minor FROM checkpoints
		WHERE user_id = $1 AND currency = 'UZS' AND computed_minor IS NOT NULL
		ORDER BY taken_at DESC, created_at DESC LIMIT 1`, a.ID).Scan(&gap)
	switch {
	case err == nil:
		m.LastGapMinor = &gap
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	return m, nil
}

func (b *Bot) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := b.pool.Query(ctx, adminUserQuery+`
		GROUP BY u.id
		ORDER BY (u.status = 'pending') DESC, u.created_at DESC`)
	if err != nil {
		serverError(w, "admin users", err)
		return
	}
	var users []adminUserJSON
	for rows.Next() {
		a, err := scanAdminUser(rows)
		if err != nil {
			rows.Close()
			serverError(w, "admin users scan", err)
			return
		}
		users = append(users, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		serverError(w, "admin users", err)
		return
	}

	counts := adminCountsJSON{Total: len(users)}
	for i := range users {
		switch users[i].Status {
		case statusActive:
			counts.Active++
			if users[i].Entries7d > 0 {
				counts.Active7d++
			}
		case statusPending:
			counts.Pending++
		case statusBlocked:
			counts.Blocked++
		}
		if users[i].Money, err = b.adminMoney(ctx, users[i]); err != nil {
			serverError(w, "admin money", err)
			return
		}
	}
	if users == nil {
		users = []adminUserJSON{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"users":         users,
		"counts":        counts,
		"money_visible": b.cfg.AdminSeesMoney,
		"bot_username":  b.username,
	})
}

type failureJSON struct {
	Text      string    `json:"text"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

func (b *Bot) handleAdminUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	a, err := scanAdminUser(b.pool.QueryRow(ctx, adminUserQuery+`
		WHERE u.id = $1
		GROUP BY u.id`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		serverError(w, "admin user", err)
		return
	}
	if a.Money, err = b.adminMoney(ctx, a); err != nil {
		serverError(w, "admin money", err)
		return
	}

	// What the bot could not read. Only with money visible: the text of a
	// failed message is the message itself ("Aliga 500 berdim").
	failures := []failureJSON{}
	if b.cfg.AdminSeesMoney {
		if err := b.sumRows(ctx, `
			SELECT text, reason, created_at FROM parse_failures
			WHERE user_id = $1 ORDER BY created_at DESC LIMIT 30`, []any{id}, func(rows pgx.Rows) error {
			var f failureJSON
			if err := rows.Scan(&f.Text, &f.Reason, &f.CreatedAt); err != nil {
				return err
			}
			failures = append(failures, f)
			return nil
		}); err != nil {
			serverError(w, "admin failures", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":          a,
		"failures":      failures,
		"money_visible": b.cfg.AdminSeesMoney,
	})
}

// handleAdminStatus is the dashboard's Allow / Block button. The owner is
// told exactly as when the admin presses the bot's own buttons.
func (b *Bot) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Status != statusActive && in.Status != statusBlocked {
		writeError(w, http.StatusBadRequest, "status must be active or blocked")
		return
	}
	u, err := b.userByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		serverError(w, "admin status lookup", err)
		return
	}
	notice, _ := b.setStatus(r.Context(), u.TelegramID, in.Status)
	writeJSON(w, http.StatusOK, map[string]string{"status": in.Status, "notice": notice})
}

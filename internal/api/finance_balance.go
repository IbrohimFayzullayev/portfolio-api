package api

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ibrohimcoder/portfolio-api/internal/finance"
)

// The balance: money in hand, from checkpoints plus the rows after them.
// The arithmetic lives in internal/finance/balance.go and is shared with the
// bot; this file only reads the rows and writes checkpoints.

type balanceJSON struct {
	Currency    string              `json:"currency"`
	AmountMinor int64               `json:"amount_minor"`
	Checkpoint  *finance.Checkpoint `json:"checkpoint"`
	// Since the checkpoint: what came in, what went out.
	In  int64 `json:"in"`
	Out int64 `json:"out"`
}

type financeBalanceResponse struct {
	// One per currency that has ever been counted; empty before the first
	// checkpoint, which the page treats as "set an opening balance".
	Balances []balanceJSON   `json:"balances"`
	InHand   finance.Amounts `json:"in_hand"`
	// In hand + owed to me - what I owe: the honest total.
	NetWorth finance.Amounts `json:"net_worth"`
}

type balancePoint struct {
	Date string `json:"date"`
	// Nil before the first checkpoint: there is no balance to draw.
	AmountMinor *int64 `json:"amount_minor"`
}

func (s *Server) loadCheckpoints(ctx context.Context, currency string) ([]finance.Checkpoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, currency, amount_minor, taken_at, computed_minor, note
		FROM finance_checkpoints
		WHERE ($1 = '' OR currency = $1)
		ORDER BY taken_at, created_at`, currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []finance.Checkpoint
	for rows.Next() {
		var cp finance.Checkpoint
		if err := rows.Scan(&cp.ID, &cp.Currency, &cp.Minor, &cp.TakenAt, &cp.Computed, &cp.Note); err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}

// loadFlows reads every row from a day on. Covers() decides precisely which
// of them count after a given checkpoint.
func (s *Server) loadFlows(ctx context.Context, from time.Time) ([]finance.Flow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT currency, kind, amount_minor, spent_on, created_at
		FROM finance_entries
		WHERE spent_on >= $1::date`, from.In(tashkent).Format(dateLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []finance.Flow
	for rows.Next() {
		var f finance.Flow
		if err := rows.Scan(&f.Currency, &f.Kind, &f.Minor, &f.Day, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// financeBalance gives the balance now and, for the range, the so'm balance
// at the end of each day.
func (s *Server) financeBalance(ctx context.Context, rng dateRange) (*financeBalanceResponse, []balancePoint, error) {
	out := &financeBalanceResponse{
		Balances: []balanceJSON{},
		InHand:   finance.Amounts{},
		NetWorth: finance.Amounts{},
	}
	series := []balancePoint{}

	cps, err := s.loadCheckpoints(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	if len(cps) == 0 {
		return out, series, nil
	}

	// Rows from the earliest day either question needs.
	earliest := cps[0].TakenAt
	for _, cp := range cps {
		if cp.TakenAt.Before(earliest) {
			earliest = cp.TakenAt
		}
	}
	flows, err := s.loadFlows(ctx, earliest)
	if err != nil {
		return nil, nil, err
	}

	latest := map[string]finance.Checkpoint{}
	for _, cp := range cps { // in TakenAt order: the last one wins
		latest[cp.Currency] = cp
	}
	for _, cur := range []string{"UZS", "USD"} {
		cp, ok := latest[cur]
		if !ok {
			continue
		}
		minor, in, outflow := finance.Since(cp, flows, tashkent)
		cpCopy := cp
		out.Balances = append(out.Balances, balanceJSON{
			Currency: cur, AmountMinor: minor, Checkpoint: &cpCopy, In: in, Out: outflow,
		})
		out.InHand.Add(cur, minor)
		out.NetWorth.Add(cur, minor)
	}

	debts, err := s.financeDebts(ctx, "")
	if err != nil {
		return nil, nil, err
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

/* ------------------------------ checkpoints ------------------------------- */

func (s *Server) handleListFinanceCheckpoints(w http.ResponseWriter, r *http.Request) {
	cps, err := s.loadCheckpoints(r.Context(), "")
	if err != nil {
		s.serverError(w, r, "list checkpoints", err)
		return
	}
	// Newest first, and only the recent ones: this is a history to glance
	// at, not an archive.
	out := make([]finance.Checkpoint, 0, len(cps))
	for i := len(cps) - 1; i >= 0 && len(out) < 50; i-- {
		out = append(out, cps[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"checkpoints": out})
}

type checkpointInput struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
	// Optional "YYYY-MM-DD": the balance at the START of that day, for an
	// opening balance entered after the fact. Empty means now.
	Date string `json:"date"`
	Note string `json:"note"`
}

func (s *Server) handleCreateFinanceCheckpoint(w http.ResponseWriter, r *http.Request) {
	var in checkpointInput
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
	if finance.Abs(in.AmountMinor) > financeMaxAmount*10 {
		writeError(w, http.StatusBadRequest, "amount_minor is out of range")
		return
	}
	if utf8.RuneCountInString(in.Note) > financeMaxNote {
		writeError(w, http.StatusBadRequest, "note is too long")
		return
	}

	takenAt := time.Now()
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

	computed, err := s.computedAt(r.Context(), in.Currency, takenAt)
	if err != nil {
		s.serverError(w, r, "compute balance for checkpoint", err)
		return
	}

	var cp finance.Checkpoint
	err = s.pool.QueryRow(r.Context(), `
		INSERT INTO finance_checkpoints (currency, amount_minor, taken_at, computed_minor, note, source)
		VALUES ($1, $2, $3, $4, $5, 'admin')
		RETURNING id::text, currency, amount_minor, taken_at, computed_minor, note`,
		in.Currency, in.AmountMinor, takenAt, computed, in.Note,
	).Scan(&cp.ID, &cp.Currency, &cp.Minor, &cp.TakenAt, &cp.Computed, &cp.Note)
	if err != nil {
		s.serverError(w, r, "create checkpoint", err)
		return
	}
	writeJSON(w, http.StatusCreated, cp)
}

// computedAt is what the rows say the balance was at a moment: the latest
// checkpoint before it plus the rows between them. Nil when there is none.
func (s *Server) computedAt(ctx context.Context, currency string, at time.Time) (*int64, error) {
	cps, err := s.loadCheckpoints(ctx, currency)
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
	flows, err := s.loadFlows(ctx, prev.TakenAt)
	if err != nil {
		return nil, err
	}
	// The rows after prev that the new checkpoint would already include.
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

func (s *Server) handleDeleteFinanceCheckpoint(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r)
	if !ok {
		return
	}
	tag, err := s.pool.Exec(r.Context(), `DELETE FROM finance_checkpoints WHERE id = $1::uuid`, id)
	if err != nil {
		s.serverError(w, r, "delete checkpoint", err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "checkpoint not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

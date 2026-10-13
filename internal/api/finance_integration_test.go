package api

import (
	"context"
	"net/http"
	"testing"
)

// The money endpoints against a real database: the same rows the bot would
// write, read back as the dashboard and the Mini App see them.

func setupFinance(t *testing.T) (*Server, string) {
	t.Helper()
	s := setupServer(t)
	if _, err := s.pool.Exec(context.Background(), `TRUNCATE finance_entries`); err != nil {
		t.Fatalf("truncate finance_entries: %v", err)
	}
	s.cfg.TelegramBotToken = testBotToken
	s.cfg.TelegramAllowedIDs = []int64{42}
	return s, registerAdmin(t, s)
}

func createEntry(t *testing.T, s *Server, token string, body map[string]any) financeEntryJSON {
	t.Helper()
	rec := do(t, s, http.MethodPost, "/api/v1/finance/entries", token, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %v: %d %s", body, rec.Code, rec.Body.String())
	}
	var e financeEntryJSON
	decode(t, rec, &e)
	return e
}

func TestIntegration_FinanceCRUD(t *testing.T) {
	s, admin := setupFinance(t)

	e := createEntry(t, s, admin, map[string]any{
		"kind": "expense", "amount_minor": 3_500_000, "currency": "UZS",
		"category": "ovqat", "person": "", "note": "tushlik", "spent_on": "2026-10-05",
	})
	if e.ID == "" || e.Source != "admin" || e.SpentOn != "2026-10-05" {
		t.Fatalf("created entry: %+v", e)
	}

	// Edit: change kind to a loan; the category follows.
	rec := do(t, s, http.MethodPut, "/api/v1/finance/entries/"+e.ID, admin, map[string]any{
		"kind": "lent", "amount_minor": 50_000_000, "currency": "UZS",
		"category": "ovqat", "person": "Ali", "note": "Aliga qarz", "spent_on": "2026-10-01",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	var updated financeEntryJSON
	decode(t, rec, &updated)
	if updated.Kind != "lent" || updated.Category != "qarz" || updated.Person != "Ali" ||
		updated.SpentOn != "2026-10-01" || updated.Source != "admin" {
		t.Errorf("updated entry: %+v", updated)
	}

	// Filtered list finds it by person, case-insensitively.
	rec = do(t, s, http.MethodGet, "/api/v1/finance/entries?person=ali", admin, nil)
	var list financeEntriesResponse
	decode(t, rec, &list)
	if rec.Code != http.StatusOK || list.Total != 1 || len(list.Entries) != 1 {
		t.Fatalf("list by person: %d %+v", rec.Code, list)
	}

	// Validation errors are 400s, not 500s.
	rec = do(t, s, http.MethodPut, "/api/v1/finance/entries/"+e.ID, admin, map[string]any{
		"kind": "lent", "amount_minor": 1, "currency": "UZS",
		"category": "", "person": "", "note": "", "spent_on": "2026-10-01",
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("debt without person: %d", rec.Code)
	}

	if rec := do(t, s, http.MethodDelete, "/api/v1/finance/entries/"+e.ID, admin, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := do(t, s, http.MethodDelete, "/api/v1/finance/entries/"+e.ID, admin, nil); rec.Code != http.StatusNotFound {
		t.Errorf("second delete: %d, want 404", rec.Code)
	}
}

func TestIntegration_FinanceSummaryAndDebts(t *testing.T) {
	s, admin := setupFinance(t)

	for _, body := range []map[string]any{
		{"kind": "expense", "amount_minor": 3_500_000, "currency": "UZS", "category": "ovqat", "person": "", "note": "tushlik", "spent_on": "2026-10-02"},
		{"kind": "expense", "amount_minor": 1_800_000, "currency": "UZS", "category": "transport", "person": "", "note": "taksi", "spent_on": "2026-10-02"},
		{"kind": "expense", "amount_minor": 1_250, "currency": "USD", "category": "ish", "person": "", "note": "vps", "spent_on": "2026-10-03"},
		{"kind": "gift", "amount_minor": 10_000_000, "currency": "UZS", "category": "marosim", "person": "Sardor", "note": "to'yga", "spent_on": "2026-10-04"},
		{"kind": "lent", "amount_minor": 50_000_000, "currency": "UZS", "category": "", "person": "Ali", "note": "", "spent_on": "2026-10-01"},
		{"kind": "repaid_in", "amount_minor": 20_000_000, "currency": "UZS", "category": "", "person": "ali", "note": "", "spent_on": "2026-10-05"},
		{"kind": "borrowed", "amount_minor": 30_000, "currency": "USD", "category": "", "person": "Jasur", "note": "", "spent_on": "2026-10-02"},
		// Outside the range: only the comparison window sees it.
		{"kind": "expense", "amount_minor": 9_000_000, "currency": "UZS", "category": "ovqat", "person": "", "note": "restoran", "spent_on": "2026-09-03"},
	} {
		createEntry(t, s, admin, body)
	}

	rec := do(t, s, http.MethodGet,
		"/api/v1/finance/summary?from=2026-10-01&to=2026-10-07&compare_from=2026-09-01&compare_to=2026-09-07",
		admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body.String())
	}
	var sum financeSummary
	decode(t, rec, &sum)

	if sum.Spent["UZS"] != 15_300_000 || sum.Spent["USD"] != 1_250 {
		t.Errorf("spent = %v", sum.Spent)
	}
	if sum.Days != 7 || len(sum.ByDay) != 7 || sum.ByDay[1].Date != "2026-10-02" ||
		sum.ByDay[1].Amounts["UZS"] != 5_300_000 {
		t.Errorf("by_day = %+v", sum.ByDay)
	}
	if len(sum.ByMonth) != 12 || sum.ByMonth[11].Month != "2026-10" || sum.ByMonth[10].Amounts["UZS"] != 9_000_000 {
		t.Errorf("by_month = %+v", sum.ByMonth)
	}
	if len(sum.ByCategory) == 0 || sum.ByCategory[0].Category != "marosim" {
		t.Errorf("by_category = %+v", sum.ByCategory)
	}
	if len(sum.GiftsByPerson) != 1 || sum.GiftsByPerson[0].Person != "Sardor" {
		t.Errorf("gifts_by_person = %+v", sum.GiftsByPerson)
	}
	if sum.Compare == nil || sum.Compare.Spent["UZS"] != 9_000_000 {
		t.Errorf("compare = %+v", sum.Compare)
	}
	if sum.ByKind["lent"].Amounts["UZS"] != 50_000_000 || sum.ByKind["expense"].Count != 3 {
		t.Errorf("by_kind = %+v", sum.ByKind)
	}

	// Ali owes 300 000; I owe Jasur $300.
	if sum.Debts.OwedToMe["UZS"] != 30_000_000 || sum.Debts.IOwe["USD"] != 30_000 || sum.Debts.People != 2 {
		t.Errorf("debts = %+v", sum.Debts)
	}
	for _, b := range sum.Debts.Balances {
		if b.Person == "ali" && b.Since != "2026-10-01" {
			t.Errorf("Ali's debt since %s, want 2026-10-01", b.Since)
		}
	}

	// The Mini App's token reads the same summary.
	viewer := viewerToken(t, s, 42)
	if rec := do(t, s, http.MethodGet, "/api/v1/finance/summary", viewer, nil); rec.Code != http.StatusOK {
		t.Errorf("viewer summary: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, s, http.MethodGet, "/api/v1/finance/debts?person=Ali", viewer, nil); rec.Code != http.StatusOK {
		t.Errorf("viewer debts: %d", rec.Code)
	}
	if rec := do(t, s, http.MethodGet, "/api/v1/finance/entries?q=taks", viewer, nil); rec.Code != http.StatusOK {
		t.Errorf("viewer entries: %d", rec.Code)
	}

	// Bad ranges are the caller's fault.
	for _, q := range []string{"from=2026-10-07&to=2026-10-01", "from=13.10.2026", "from=2020-01-01&to=2026-01-01"} {
		if rec := do(t, s, http.MethodGet, "/api/v1/finance/summary?"+q, admin, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("summary?%s: %d, want 400", q, rec.Code)
		}
	}
}

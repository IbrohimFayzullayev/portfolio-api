package finance

import (
	"strings"
	"testing"
	"time"
)

func day(m time.Month, d int) time.Time {
	return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC)
}

func TestFold(t *testing.T) {
	rows := []Row{
		// Ali: lent 500k, got 200k back — owes 300k since the loan.
		{"Ali", "UZS", KindLent, 500_000_00, day(10, 1)},
		{"ali", "UZS", KindRepaidIn, 200_000_00, day(10, 5)},

		// Vali: fully repaid, then lent again — the clock restarts.
		{"Vali", "UZS", KindLent, 100_000_00, day(9, 1)},
		{"Vali", "UZS", KindRepaidIn, 100_000_00, day(9, 10)},
		{"Vali", "UZS", KindLent, 50_000_00, day(10, 7)},

		// Sardor: I borrowed dollars and paid part back.
		{"Sardor", "USD", KindBorrowed, 300_00, day(10, 2)},
		{"Sardor", "USD", KindRepaidOut, 100_00, day(10, 9)},

		// Jasur: settled — must not appear.
		{"Jasur", "UZS", KindBorrowed, 70_000_00, day(9, 1)},
		{"Jasur", "UZS", KindRepaidOut, 70_000_00, day(9, 2)},
	}

	got := Fold(rows)
	want := map[string]struct {
		minor int64
		since time.Time
	}{
		"ali":    {300_000_00, day(10, 1)},
		"vali":   {50_000_00, day(10, 7)},
		"sardor": {-200_00, day(10, 2)},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d balances, want %d: %+v", len(got), len(want), got)
	}
	for _, g := range got {
		w, ok := want[strings.ToLower(g.Person)]
		if !ok {
			t.Errorf("unexpected balance for %s", g.Person)
			continue
		}
		if g.Minor != w.minor || !g.Since.Equal(w.since) {
			t.Errorf("%s: %d since %s, want %d since %s", g.Person, g.Minor,
				g.Since.Format("02.01"), w.minor, w.since.Format("02.01"))
		}
	}

	owed, owe, people := Totals(got)
	if owed["UZS"] != 350_000_00 || owe["USD"] != 200_00 || people != 3 {
		t.Errorf("totals: owed=%v owe=%v people=%d", owed, owe, people)
	}
}

func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range Kinds {
		if seen[k.Code] {
			t.Errorf("duplicate kind code %q", k.Code)
		}
		seen[k.Code] = true
		if KindByCode(k.Code) != k.Key {
			t.Errorf("KindByCode(%q) = %q", k.Code, KindByCode(k.Code))
		}
	}
	for _, c := range Categories {
		// Callback data is "fc:<key>:<uuid>", 64 bytes at most.
		if len("fc:"+c.Key+":")+36 > 64 {
			t.Errorf("category key %q too long for callback data", c.Key)
		}
		for _, r := range c.Key {
			if r > 127 {
				t.Errorf("category key %q is not ASCII", c.Key)
			}
		}
	}
	if CategoryByKey("nonsense").Key != "boshqa" {
		t.Error("unknown category should fall back to boshqa")
	}
	if !IsDebt(KindLent) || IsDebt(KindExpense) || !IsSpending(KindGift) || IsSpending(KindLent) {
		t.Error("kind flags wrong")
	}
}

package bot

import (
	"strings"
	"testing"
	"time"
)

func day(m time.Month, d int) time.Time {
	return time.Date(2026, m, d, 0, 0, 0, 0, tashkent)
}

func TestFoldDebts(t *testing.T) {
	rows := []debtRow{
		// Ali: lent 500k, got 200k back — owes 300k since the loan.
		{"Ali", "UZS", kindLent, 500_000_00, day(10, 1)},
		{"ali", "UZS", kindRepaidIn, 200_000_00, day(10, 5)},

		// Vali: fully repaid, then lent again — the clock restarts.
		{"Vali", "UZS", kindLent, 100_000_00, day(9, 1)},
		{"Vali", "UZS", kindRepaidIn, 100_000_00, day(9, 10)},
		{"Vali", "UZS", kindLent, 50_000_00, day(10, 7)},

		// Sardor: I borrowed dollars and paid part back.
		{"Sardor", "USD", kindBorrowed, 300_00, day(10, 2)},
		{"Sardor", "USD", kindRepaidOut, 100_00, day(10, 9)},

		// Jasur: settled — must not appear.
		{"Jasur", "UZS", kindBorrowed, 70_000_00, day(9, 1)},
		{"Jasur", "UZS", kindRepaidOut, 70_000_00, day(9, 2)},
	}

	got := foldDebts(rows)
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
		w, ok := want[strings.ToLower(g.person)]
		if !ok {
			t.Errorf("unexpected balance for %s", g.person)
			continue
		}
		if g.minor != w.minor || !g.since.Equal(w.since) {
			t.Errorf("%s: %d since %s, want %d since %s", g.person, g.minor,
				g.since.Format("02.01"), w.minor, w.since.Format("02.01"))
		}
	}

	owed, owe, people := debtTotals(got)
	if owed["UZS"] != 350_000_00 || owe["USD"] != 200_00 || people != 3 {
		t.Errorf("totals: owed=%v owe=%v people=%d", owed, owe, people)
	}
}

func TestFinancePeriod(t *testing.T) {
	today := day(10, 13)

	p := financePeriod("", today)
	if !p.from.Equal(day(10, 1)) || !p.to.Equal(day(10, 14)) || p.compare == nil {
		t.Fatalf("default period = %s..%s compare=%v", p.from, p.to, p.compare)
	}
	if !p.compare.from.Equal(day(9, 1)) || !p.compare.to.Equal(day(9, 14)) {
		t.Errorf("compare = %s..%s", p.compare.from, p.compare.to)
	}

	if p := financePeriod("o'tgan", today); !p.from.Equal(day(9, 1)) || !p.to.Equal(day(10, 1)) {
		t.Errorf("o'tgan = %s..%s", p.from, p.to)
	}
	if p := financePeriod("hafta", today); p.days() != 7 {
		t.Errorf("hafta spans %d days", p.days())
	}
	if p := financePeriod("sentabr", today); !p.from.Equal(day(9, 1)) || !p.to.Equal(day(10, 1)) {
		t.Errorf("sentabr = %s..%s", p.from, p.to)
	}
	// Iyun and Iyul share three letters; the full word must decide.
	if p := financePeriod("iyul", today); p.from.Month() != time.July {
		t.Errorf("iyul = %s", p.from)
	}
	// A month still ahead this year means last year's.
	if p := financePeriod("dekabr", today); p.from.Year() != 2025 {
		t.Errorf("dekabr = %s", p.from)
	}

	// 31 March compares against February, which has no 31st.
	mar := time.Date(2026, 3, 31, 0, 0, 0, 0, tashkent)
	p = financePeriod("oy", mar)
	if !p.compare.to.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, tashkent)) {
		t.Errorf("March compare ends %s", p.compare.to)
	}
}

func TestSplitEntries(t *testing.T) {
	today := day(10, 13)
	tests := []struct {
		in   string
		want int
	}{
		{"tushlik 35k", 1},
		{"tushlik 35k\ntaksi 18k\n\nkofe 20k", 3},
		{"tushlik 35k, taksi 18k", 2},
		{"tushlik 35k; taksi 18k; kofe 20k", 3},
		{"kofe, pishiriq 45k", 1}, // "kofe" alone has no amount
		{"telefon 1,5 mln", 1},    // decimal comma, not a separator
	}
	for _, tc := range tests {
		if got := splitEntries(tc.in, today); len(got) != tc.want {
			t.Errorf("splitEntries(%q) = %q, want %d parts", tc.in, got, tc.want)
		}
	}
}

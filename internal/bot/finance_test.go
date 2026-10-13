package bot

import (
	"testing"
	"time"
)

func day(m time.Month, d int) time.Time {
	return time.Date(2026, m, d, 0, 0, 0, 0, tashkent)
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

func TestNetLine(t *testing.T) {
	got := netLine(money{"UZS": 10_000_000_00}, money{"UZS": 2_630_000_00})
	if want := "+7 370 000 so'm (73%)"; got != want {
		t.Errorf("netLine = %q, want %q", got, want)
	}
	// Spending more than came in shows a minus, and a negative share.
	got = netLine(money{"UZS": 1_000_000_00}, money{"UZS": 1_500_000_00})
	if want := "-500 000 so'm (-50%)"; got != want {
		t.Errorf("netLine = %q, want %q", got, want)
	}
}

func TestMonthProjection(t *testing.T) {
	// 11 whole days gone, 2.2 mln spent: 200k a day for the 20 days left.
	end, ok := monthProjection(day(10, 12), money{"UZS": 10_000_000_00}, money{"UZS": 2_200_000_00})
	if !ok || end != 3_800_000_00 {
		t.Errorf("projection = %d, %v", end, ok)
	}
	// Too early in the month to say anything.
	if _, ok := monthProjection(day(10, 3), money{"UZS": 1}, money{"UZS": 1}); ok {
		t.Error("projection on the 3rd")
	}
	// Without income there is nothing to project against.
	if _, ok := monthProjection(day(10, 20), money{}, money{"UZS": 1}); ok {
		t.Error("projection without income")
	}
}

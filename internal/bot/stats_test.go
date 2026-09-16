package bot

import (
	"strings"
	"testing"
	"time"
)

func TestPeriodFor(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, tashkent)

	tests := []struct {
		arg      string
		label    string
		wantDays float64
	}{
		{"kun", "kun", 1},
		{"hafta", "hafta", 7},
		{"", "hafta", 7}, // the default
		{"nonsense", "hafta", 7},
		{"oy", "oy", 30},
	}

	for _, tc := range tests {
		p := periodFor(tc.arg, now)
		if p.label != tc.label {
			t.Errorf("periodFor(%q).label = %q, want %q", tc.arg, p.label, tc.label)
		}
		if days := p.to.Sub(p.from).Hours() / 24; days < tc.wantDays-1.5 || days > tc.wantDays+1.5 {
			t.Errorf("periodFor(%q) spans %.1f days, want about %.0f", tc.arg, days, tc.wantDays)
		}
	}
}

// The comparison window must be the same length and sit immediately before,
// or the percentage compares two different things.
func TestPeriodPrevious(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, tashkent)
	p := periodFor("hafta", now)
	prev := p.previous()

	if !prev.to.Equal(p.from) {
		t.Errorf("previous ends at %s, current starts at %s", prev.to, p.from)
	}
	if prev.to.Sub(prev.from) != p.to.Sub(p.from) {
		t.Error("windows have different lengths")
	}
}

func TestDelta(t *testing.T) {
	tests := []struct {
		name     string
		current  int64
		previous int64
		want     string
	}{
		{"growth", 120, 100, "+20%"},
		{"decline", 80, 100, "-20%"},
		{"flat", 100, 100, "≈"},
		{"no baseline means no claim", 120, 0, ""},
		{"negative baseline is ignored", 120, -5, ""},
	}

	for _, tc := range tests {
		got := delta(tc.current, tc.previous)
		if tc.want == "" {
			if got != "" {
				t.Errorf("%s: delta = %q, want empty", tc.name, got)
			}
			continue
		}
		// The minus sign comes from the formatter, so compare loosely.
		want := strings.ReplaceAll(tc.want, "-", "")
		if !strings.Contains(strings.ReplaceAll(got, "-", ""), want) {
			t.Errorf("%s: delta(%d, %d) = %q, want it to contain %q",
				tc.name, tc.current, tc.previous, got, tc.want)
		}
	}
}

func TestUntilNextWeekly(t *testing.T) {
	// Wednesday afternoon → the coming Sunday at 20:00.
	wednesday := time.Date(2026, 9, 16, 15, 0, 0, 0, tashkent)
	at := wednesday.Add(untilNextWeekly(wednesday))
	if at.Weekday() != weeklyWeekday || at.Hour() != weeklyHour {
		t.Errorf("got %s, want Sunday 20:00", at.Format("Mon 02.01 15:04"))
	}

	// Sunday just after the hour → next week, not a second send tonight.
	sundayLate := time.Date(2026, 9, 20, 20, 30, 0, 0, tashkent)
	at = sundayLate.Add(untilNextWeekly(sundayLate))
	if at.Day() != 27 {
		t.Errorf("got %s, want the following Sunday", at.Format("Mon 02.01 15:04"))
	}

	// Sunday morning → this evening.
	sundayMorning := time.Date(2026, 9, 20, 9, 0, 0, 0, tashkent)
	at = sundayMorning.Add(untilNextWeekly(sundayMorning))
	if at.Day() != 20 || at.Hour() != weeklyHour {
		t.Errorf("got %s, want the same evening", at.Format("Mon 02.01 15:04"))
	}
}

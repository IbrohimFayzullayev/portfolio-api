package bot

import (
	"testing"
	"time"
)

// The scheduler's only interesting decisions are about time, and getting them
// wrong is expensive in a specific way: a reminder is either right or actively
// misleading. These are the cases that decide which.

func TestParseEventTime(t *testing.T) {
	tests := []struct {
		in          string
		wantHour    int
		wantMinute  int
		wantParsed  bool
		description string
	}{
		{"19:00", 19, 0, true, "the common case"},
		{"19.00", 19, 0, true, "written with a dot"},
		{"soat 19:30 da", 19, 30, true, "wrapped in words"},
		{"9:05", 9, 5, true, "single-digit hour"},
		{"", 0, 0, false, "empty field"},
		{"kechqurun", 0, 0, false, "a word, not a clock"},
		{"25:00", 0, 0, false, "not a real hour"},
		{"19:75", 0, 0, false, "not a real minute"},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			h, m, ok := parseEventTime(tc.in)
			if ok != tc.wantParsed {
				t.Fatalf("parsed = %v, want %v", ok, tc.wantParsed)
			}
			if ok && (h != tc.wantHour || m != tc.wantMinute) {
				t.Errorf("got %02d:%02d, want %02d:%02d", h, m, tc.wantHour, tc.wantMinute)
			}
		})
	}
}

func TestEventMomentFallsBackWithoutAClock(t *testing.T) {
	date := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC) // as a DATE column arrives

	at, exact := eventMoment(date, "19:00")
	if !exact {
		t.Fatal("a readable time was not treated as exact")
	}
	if at.Hour() != 19 || at.Day() != 20 || at.Location() != tashkent {
		t.Errorf("got %s, want 20.09 19:00 Tashkent", at)
	}

	at, exact = eventMoment(date, "kechqurun")
	if exact {
		t.Fatal("an unreadable time was reported as exact")
	}
	if at.Day() != 20 {
		t.Errorf("fallback moved the day: %s", at)
	}

	// The day before at 09:00 — the morning batch, not the middle of the night.
	morning := morningBefore(at)
	if morning.Day() != 19 || morning.Hour() != quietReleaseHour {
		t.Errorf("morningBefore = %s, want 19.09 09:00", morning)
	}
}

func TestParseWhen(t *testing.T) {
	// A Tuesday afternoon.
	now := time.Date(2026, 9, 15, 14, 0, 0, 0, tashkent)

	tests := []struct {
		name     string
		fields   []string
		wantErr  bool
		wantDay  int
		wantHour int
		wantRest string
	}{
		{"date and time", []string{"20.09", "10:30", "post", "chiqsin"}, false, 20, 10, "post chiqsin"},
		{"date only means morning", []string{"20.09", "oylik", "hisobot"}, false, 20, quietReleaseHour, "oylik hisobot"},
		{"time later today", []string{"18:30", "kitob"}, false, 15, 18, "kitob"},
		{"time already gone means tomorrow", []string{"09:00", "kitob"}, false, 16, 9, "kitob"},
		{"explicit year", []string{"05.01.2027", "yangi", "yil"}, false, 5, quietReleaseHour, "yangi yil"},
		{"no moment at all", []string{"shunchaki", "matn"}, true, 0, 0, ""},
		{"impossible clock", []string{"99:99", "matn"}, true, 0, 0, ""},
		{"impossible month", []string{"20.19", "matn"}, true, 0, 0, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			at, rest, err := parseWhen(now, tc.fields)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %s", at)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if at.Day() != tc.wantDay || at.Hour() != tc.wantHour {
				t.Errorf("got %s, want day %d hour %d", at.Format("02.01 15:04"), tc.wantDay, tc.wantHour)
			}
			if !at.After(now) {
				t.Errorf("%s is not in the future", at)
			}
			if got := joinFields(rest); got != tc.wantRest {
				t.Errorf("rest = %q, want %q", got, tc.wantRest)
			}
		})
	}
}

// A date in the past this year is read as next year rather than as a mistake:
// typing 05.01 in December means the January that is coming.
func TestParseWhenRollsOverTheYear(t *testing.T) {
	december := time.Date(2026, 12, 20, 10, 0, 0, 0, tashkent)

	at, _, err := parseWhen(december, []string{"05.01", "yangi", "reja"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if at.Year() != 2027 || at.Month() != time.January {
		t.Errorf("got %s, want January 2027", at)
	}
}

func joinFields(fields []string) string {
	out := ""
	for i, f := range fields {
		if i > 0 {
			out += " "
		}
		out += f
	}
	return out
}

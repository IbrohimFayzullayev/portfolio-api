package growth

import (
	"testing"
	"time"
)

var tz = time.FixedZone("Asia/Tashkent", 5*3600)

// day builds a date in October 2026. 12 Oct is a Monday, 18 Oct a Sunday.
func day(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, tz) }

func TestParseLog(t *testing.T) {
	tests := []struct {
		in      string
		kind    string
		minutes int
		note    string
		offset  int
		wantErr bool
	}{
		{"45 writing", "writing", 45, "", 0, false},
		{"writing 45", "writing", 45, "", 0, false},
		{"45 w essay 2", "writing", 45, "essay 2", 0, false},
		{"30 s part 1", "speaking", 30, "part 1", 0, false},
		{"20 o'qish", "reading", 20, "", 0, false},
		{"20 o‘qish", "reading", 20, "", 0, false},
		{"1s l cambridge 18", "listening", 60, "cambridge 18", 0, false},
		{"1.5soat mock", "mock", 90, "", 0, false},
		{"kecha 25 anki", "vocab", 25, "", -1, false},
		{"40daq grammatika", "grammar", 40, "", 0, false},
		{"15", "other", 15, "", 0, false},
		{"writing", "writing", 0, "", 0, true},
		{"0 w", "writing", 0, "", 0, true},
		{"900 w", "writing", 900, "", 0, true},
		{"2.5 w", "writing", 0, "", 0, true}, // a fraction of a minute is a typo
	}
	for _, tc := range tests {
		got, err := ParseLog(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("%q: err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if tc.wantErr {
			continue
		}
		if got.Kind != tc.kind || got.Minutes != tc.minutes || got.Note != tc.note || got.DayOffset != tc.offset {
			t.Errorf("%q: got %+v", tc.in, got)
		}
	}
}

func TestActiveDays(t *testing.T) {
	m := map[string]int{}
	for d, v := range map[int]int{12: 30, 13: 5, 15: 40, 18: 60} { // 18 = Sunday
		m[DayKey(day(d))] = v
	}
	active, working := ActiveDays(m, day(12), day(18))
	if active != 3 || working != 6 {
		t.Fatalf("ActiveDays = %d/%d, want 3/6 (Sunday neither counts nor is counted)", active, working)
	}
}

func TestSprintWindow(t *testing.T) {
	tests := []struct {
		name       string
		now        time.Time
		start, end int
	}{
		{"monday starts today", day(12), 12, 17},
		{"tuesday starts today", day(13).Add(13 * time.Hour), 13, 17},
		{"friday is a two-day sprint", day(16), 16, 17},
		{"saturday waits for monday", day(17), 19, 24},
		{"sunday waits for monday", day(18), 19, 24},
	}
	for _, tc := range tests {
		s, e := SprintWindow(tc.now)
		if !s.Equal(day(tc.start)) || !e.Equal(day(tc.end)) {
			t.Errorf("%s: got %s – %s", tc.name, s.Format("02"), e.Format("02"))
		}
	}
}

func TestSprintDay(t *testing.T) {
	// 19 (Mon) – 24 (Sat): six working days.
	tests := []struct{ today, n, total int }{
		{18, 0, 6},
		{19, 1, 6},
		{21, 3, 6},
		{24, 6, 6},
		{26, 6, 6},
	}
	for _, tc := range tests {
		n, total := SprintDay(day(19), day(24), day(tc.today))
		if n != tc.n || total != tc.total {
			t.Errorf("today %d: %d/%d, want %d/%d", tc.today, n, total, tc.n, tc.total)
		}
	}
}

func TestNudgeDue(t *testing.T) {
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, tz) }
	tests := []struct {
		name  string
		now   time.Time
		today int
		want  bool
	}{
		{"20:59 too early", at(14, 20, 59), 0, false},
		{"21:00 due", at(14, 21, 0), 0, true},
		{"anything logged cancels it", at(14, 21, 30), 5, false},
		{"23:00 is quiet hours", at(14, 23, 0), 0, false},
		{"sunday never", at(18, 21, 30), 0, false},
	}
	for _, tc := range tests {
		if got := NudgeDue(tc.now, tc.today); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}

func TestFormatMinutesAndSparkline(t *testing.T) {
	for in, want := range map[int]string{5: "5 daq", 60: "1 s", 80: "1 s 20 daq", 185: "3 s 5 daq"} {
		if got := FormatMinutes(in); got != want {
			t.Errorf("FormatMinutes(%d) = %q, want %q", in, got, want)
		}
	}
	if got := Sparkline([]int{0, 10, 40, 0}); got != "▁▄█▁" {
		t.Errorf("Sparkline = %q", got)
	}
	if got := Sparkline([]int{0, 0}); got != "▁▁" {
		t.Errorf("empty Sparkline = %q", got)
	}
}

func TestParseScore(t *testing.T) {
	s, err := ParseScore("6.0 L6.5 R6 W5,5 s:5.5 mock Cambridge 18")
	if err != nil {
		t.Fatal(err)
	}
	if s.OverallHalves != 12 || s.Listening != 13 || s.Reading != 12 || s.Writing != 11 || s.Speaking != 11 {
		t.Fatalf("got %+v", s)
	}
	if s.Kind != "mock" || s.Note != "Cambridge 18" {
		t.Fatalf("kind/note: %+v", s)
	}

	s, err = ParseScore("6.5 rasmiy")
	if err != nil || s.Kind != "official" || s.OverallHalves != 13 {
		t.Fatalf("rasmiy: %+v %v", s, err)
	}

	for _, bad := range []string{"", "mock", "6.3", "9.5", "6 L6.2"} {
		if _, err := ParseScore(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
	if FormatBand(13) != "6.5" || FormatBand(14) != "7.0" {
		t.Error("FormatBand")
	}
}

func TestParseNewBook(t *testing.T) {
	tests := []struct {
		in, title, author string
		pages             int
	}{
		{"O'tkan kunlar - Abdulla Qodiriy 420 bet", "O'tkan kunlar", "Abdulla Qodiriy", 420},
		{"Kecha va kunduz — Cho‘lpon", "Kecha va kunduz", "Cho'lpon", 0},
		{"«Alkimyogar» 192b", "Alkimyogar", "", 192},
		{"Ufq - Said Ahmad 640-bet", "Ufq", "Said Ahmad", 640},
		{"Ikki eshik orasi", "Ikki eshik orasi", "", 0},
		{"Jinoyat va jazo - Fyodor Dostoyevskiy 600 sahifa", "Jinoyat va jazo", "Fyodor Dostoyevskiy", 600},
	}
	for _, tc := range tests {
		b, err := ParseNewBook(tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if b.Title != tc.title || b.Author != tc.author || b.Pages != tc.pages {
			t.Errorf("%q: got %+v", tc.in, b)
		}
	}
	if _, err := ParseNewBook("  "); err == nil {
		t.Error("empty title accepted")
	}
}

func TestParseBookLog(t *testing.T) {
	tests := []struct {
		in      string
		minutes int
		page    int
		query   string
		offset  int
		wantErr bool
	}{
		{"40", 40, 0, "", 0, false},
		{"40 120-bet", 40, 120, "", 0, false},
		{"40 120 bet", 40, 120, "", 0, false},
		{"1s b85", 60, 85, "", 0, false},
		{"kecha 1.5soat Kecha va kunduz 85b", 90, 85, "Kecha va kunduz", -1, false},
		{"Ufq 30", 30, 0, "Ufq", 0, false},
		{"120 bet", 0, 120, "", 0, true},
		{"ufq", 0, 0, "ufq", 0, true},
	}
	for _, tc := range tests {
		got, err := ParseBookLog(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("%q: err %v", tc.in, err)
			continue
		}
		if tc.wantErr {
			continue
		}
		if got.Minutes != tc.minutes || got.Page != tc.page || got.Query != tc.query || got.DayOffset != tc.offset {
			t.Errorf("%q: got %+v", tc.in, got)
		}
	}
	if Percent(160, 320) != 50 || Percent(400, 320) != 100 || Percent(10, 0) != 0 {
		t.Error("Percent")
	}
}

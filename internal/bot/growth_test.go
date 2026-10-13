package bot

import (
	"strings"
	"testing"
)

func TestRenderGrowthNudge(t *testing.T) {
	b := &Bot{}
	render := func(p map[string]any) string {
		return b.renderNotification(notification{kind: "growth.nudge", payload: p})
	}

	if got := render(map[string]any{"today_minutes": float64(0)}); !strings.Contains(got, "hali nol") {
		t.Errorf("zero day: %q", got)
	}
	if got := render(map[string]any{"today_minutes": float64(10)}); !strings.Contains(got, "15 daqiqa qoldi") {
		t.Errorf("partial day: %q", got)
	}
	got := render(map[string]any{
		"today_minutes": float64(0), "missed_yesterday": true,
		"streak": float64(4), "sprint_goal": "5 ta <essay>",
	})
	for _, want := range []string{"ikkinchisi bo'lmasin", "Streak: 4", "5 ta &lt;essay&gt;"} {
		if !strings.Contains(got, want) {
			t.Errorf("missed day: missing %q in %q", want, got)
		}
	}
}

func TestPickBook(t *testing.T) {
	books := []bookRow{
		{id: "1", title: "Ufq", author: "Said Ahmad"},
		{id: "2", title: "O‘tkan kunlar", author: "Abdulla Qodiriy"},
	}
	if bk, msg := pickBook(books, ""); bk.id != "1" || msg != "" {
		t.Errorf("current: %v %q", bk.id, msg)
	}
	if bk, _ := pickBook(books, "o'tkan"); bk.id != "2" {
		t.Error("apostrophe-insensitive match failed")
	}
	if bk, _ := pickBook(books, "qodiriy"); bk.id != "2" {
		t.Error("author match failed")
	}
	if _, msg := pickBook(books, "harry"); !strings.Contains(msg, "topilmadi") {
		t.Errorf("unknown: %q", msg)
	}
	if _, msg := pickBook(nil, ""); !strings.Contains(msg, "yo'q") {
		t.Errorf("none: %q", msg)
	}
}

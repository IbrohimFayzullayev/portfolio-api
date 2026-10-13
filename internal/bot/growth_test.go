package bot

import (
	"strings"
	"testing"
)

func TestRenderGrowthNudge(t *testing.T) {
	b := &Bot{}
	got := b.renderNotification(notification{kind: "growth.nudge", payload: map[string]any{
		"sprint_goal": "5 ta <essay>", "sprint_day": float64(3),
		"sprint_total": float64(6), "sprint_minutes": float64(95),
	}})
	for _, want := range []string{"hali tegilmadi", "5 ta &lt;essay&gt;", "Kun 3/6", "1 s 35 daq"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	for _, unwanted := range []string{"Streak", "minimum"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("reminder still talks about %q: %q", unwanted, got)
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

func TestSplitTextAndTabURL(t *testing.T) {
	text := strings.Repeat("| a | b |\n", 500)
	chunks := splitText(text, 1000)
	if strings.Join(chunks, "") != text {
		t.Fatal("splitText lost text")
	}
	for _, c := range chunks {
		if len(c) > 1000 || !strings.HasSuffix(c, "\n") {
			t.Fatalf("chunk breaks a line or is too long: %d", len(c))
		}
	}

	b := &Bot{cfg: Config{AdminSiteURL: "https://admin.example.com/"}}
	if got := b.tabURL("notes", "reja"); got != "https://admin.example.com/tg?doc=reja&tab=notes" {
		t.Errorf("tabURL = %q", got)
	}
	if (&Bot{cfg: Config{AdminSiteURL: "http://localhost:3001"}}).tabKeyboard("growth", "x") != nil {
		t.Error("a non-https Mini App URL must not produce a button")
	}
}

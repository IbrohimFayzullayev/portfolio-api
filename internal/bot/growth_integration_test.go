package bot

// Runs the growth and book commands against a real PostgreSQL, with Telegram
// replaced by a stub that records what would have been sent. Skipped unless
// TEST_DATABASE_URL points at a throwaway database (make test-integration).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/database"
	"github.com/ibrohimcoder/portfolio-api/internal/growth"
)

type sentCall struct {
	method string
	body   map[string]any
}

func setupGrowthBot(t *testing.T) (*Bot, func() []sentCall) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test (run `make test-integration`)")
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := database.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		TRUNCATE growth_logs, growth_recordings, growth_sprints, growth_scores,
		         books, book_sessions, notifications RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	var (
		mu    sync.Mutex
		calls []sentCall
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		parts := strings.Split(r.URL.Path, "/")
		mu.Lock()
		calls = append(calls, sentCall{method: parts[len(parts)-1], body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(srv.Close)

	b := New(Config{Token: "t", AllowedUserIDs: []int64{42}}, pool)
	b.tg.baseURL = srv.URL
	return b, func() []sentCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]sentCall(nil), calls...)
	}
}

func mustContain(t *testing.T, label, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s: missing %q in:\n%s", label, w, got)
		}
	}
}

func TestIntegration_Growth(t *testing.T) {
	b, calls := setupGrowthBot(t)
	ctx := context.Background()
	today := dayStart(time.Now())

	// Backfill a few days before today: they are outside the sprint (which
	// starts today) and must not leak into its numbers.
	for i := 1; i <= 4; i++ {
		d := today.AddDate(0, 0, -i)
		if _, err := b.pool.Exec(ctx,
			`INSERT INTO growth_logs (day, kind, minutes) VALUES ($1::date, 'writing', 30)`,
			d.Format(dateFmt)); err != nil {
			t.Fatal(err)
		}
	}

	out := b.sprintCommand(ctx, "5 ta Task 2 essay, 2 tasi tekshirilgan")
	mustContain(t, "sprint open", out, "Sprint ochildi", "5 ta Task 2 essay")
	mustContain(t, "second sprint refused", b.sprintCommand(ctx, "boshqa"), "Ochiq sprint bor")

	out = b.logCommand(ctx, "10 w essay 1")
	mustContain(t, "qildim", out, "+10 daq · writing", "essay 1", "Bugun: 10 daq", "🎯 Sprint", "1/1 kun ishlandi")
	if strings.Contains(out, "Streak") || strings.Contains(out, "minimum") {
		t.Errorf("card still shows streak/minimum:\n%s", out)
	}

	out = b.logCommand(ctx, "20 s")
	mustContain(t, "qildim 2", out, "Bugun: 30 daq", "Sprintda: 30 daq")

	out = b.logCommand(ctx, "bekor")
	mustContain(t, "undo", out, "O'chirildi: 20 daq · speaking")

	// Two voice messages: first and last of the sprint.
	mustContain(t, "voice", b.recordVoice(ctx, &Voice{FileID: "F1", FileUniqueID: "U1", Duration: 125}, ""),
		"2:05", "speaking +3 daq", "1-yozuv")
	mustContain(t, "voice dup", b.recordVoice(ctx, &Voice{FileID: "F1", FileUniqueID: "U1", Duration: 125}, ""),
		"allaqachon")
	b.recordVoice(ctx, &Voice{FileID: "F2", FileUniqueID: "U2", Duration: 61}, "part 2")

	out = b.todayReport(ctx)
	mustContain(t, "bugun", out, "10 daq writing", "🎙", "Bugun: 15 daq", "Sprintda: 15 daq")

	st, err := b.growthNow(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if st.todayMinutes != 10+3+2 {
		t.Errorf("todayMinutes = %d", st.todayMinutes)
	}
	if st.sprintMinutes != 15 || st.activeDays != 1 {
		t.Errorf("sprint progress = %d min, %d days (backfilled days leaked in?)", st.sprintMinutes, st.activeDays)
	}

	mustContain(t, "ball", b.scoreCommand(ctx, "5.5 L6 R6 W5 S5 mock baza"), "5.5", "L6.0", "W5.0")
	mustContain(t, "ball delta", b.scoreCommand(ctx, "6"), "↑0.5")
	if _, err := growth.ParseScore("6.3"); err == nil {
		t.Error("6.3 accepted")
	}

	// Nudge: the payload renders, and the dedupe key holds it to one per day.
	for i := 0; i < 2; i++ {
		b.queueScheduled(ctx, "growth.nudge", map[string]any{"sprint_goal": "x"},
			sevInfo, "growth-nudge:test", time.Time{})
	}
	var n int
	if err := b.pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'growth.nudge'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("nudge rows = %d (%v)", n, err)
	}

	if s := b.growthDailySection(ctx); !strings.Contains(s, "O'sish") {
		t.Errorf("daily section empty: %q", s)
	}
	if s := b.growthWeeklySection(ctx); !strings.Contains(s, "Ingliz:") {
		t.Errorf("weekly section: %q", s)
	}

	b.closeSprint(ctx, 42, "yaxshi hafta")
	var voices []string
	var summary string
	for _, c := range calls() {
		switch c.method {
		case "sendVoice":
			voices = append(voices, c.body["voice"].(string))
		case "sendMessage":
			if txt, _ := c.body["text"].(string); strings.Contains(txt, "Sprint yopildi") {
				summary = txt
			}
		}
	}
	mustContain(t, "yakun", summary, "Ishlangan kunlar: 1/", "Yozuvlar: 2", "yaxshi hafta", "Ballar")

	// The closing question comes last, with three answers.
	var question map[string]any
	for _, c := range calls() {
		if txt, _ := c.body["text"].(string); strings.Contains(txt, "Natijaga erishdingizmi") {
			question = c.body
		}
	}
	if question == nil || question["reply_markup"] == nil {
		t.Fatal("no outcome question with buttons")
	}
	var sprintID string
	if err := b.pool.QueryRow(ctx, `SELECT id::text FROM growth_sprints`).Scan(&sprintID); err != nil {
		t.Fatal(err)
	}
	notice, redraw := b.sprintOutcome(ctx, "partial:"+sprintID)
	if notice != growth.Outcomes["partial"] || redraw == nil {
		t.Errorf("outcome notice = %q", notice)
	}
	var outcome string
	if err := b.pool.QueryRow(ctx, `SELECT outcome FROM growth_sprints`).Scan(&outcome); err != nil || outcome != "partial" {
		t.Errorf("outcome = %q (%v)", outcome, err)
	}
	if len(voices) != 2 || voices[0] != "F1" || voices[1] != "F2" {
		t.Errorf("voices = %v, want [F1 F2]", voices)
	}
	mustContain(t, "after close", b.sprintCommand(ctx, ""), "Ochiq sprint yo'q")
}

func TestIntegration_Books(t *testing.T) {
	b, _ := setupGrowthBot(t)
	ctx := context.Background()

	mustContain(t, "empty", b.bookCommand(ctx, ""), "/kitob yangi")
	mustContain(t, "no book", b.bookCommand(ctx, "30"), "o'qilayotgan kitob yo'q")

	mustContain(t, "add", b.bookCommand(ctx, "yangi O'tkan kunlar - Abdulla Qodiriy 400 bet"),
		"Yangi kitob", "O'tkan kunlar", "Abdulla Qodiriy", "400 bet")
	mustContain(t, "dup", b.bookCommand(ctx, "yangi o'tkan kunlar"), "allaqachon")
	b.bookCommand(ctx, "yangi Ufq - Said Ahmad")

	// The newest book is current until another one is read.
	mustContain(t, "log current", b.bookCommand(ctx, "40 b30"), "+40 daq", "Ufq", "30-bet")
	mustContain(t, "log by name", b.bookCommand(ctx, "o'tkan 45 200-bet"), "O'tkan kunlar", "50% (200/400)")
	mustContain(t, "log yesterday", b.bookCommand(ctx, "kecha 1s"), "kecha uchun", "O'tkan kunlar", "1 s 45 daq · 2 kun")
	mustContain(t, "page too far", b.bookCommand(ctx, "10 500-bet"), "bo'lishi mumkin emas")
	mustContain(t, "unknown", b.bookCommand(ctx, "harry 10"), "topilmadi", "Ufq")

	mustContain(t, "status", b.bookCommand(ctx, ""), "O'qilmoqda", "O'tkan kunlar", "Ufq")
	mustContain(t, "undo", b.bookCommand(ctx, "bekor"), "O'chirildi: 1 s")

	mustContain(t, "finish", b.bookCommand(ctx, "tugatdim o'tkan"), "Kitob tugadi", "400 bet", "-kitob")
	mustContain(t, "drop", b.bookCommand(ctx, "tashladim"), "Qoldirildi", "Ufq")

	out := b.booksReport(ctx, "")
	mustContain(t, "report", out, "Kitoblar", "Jami:", "Tugatilgan: 1 ta", "O'tkan kunlar", "Oxirgi 14 kun")
	mustContain(t, "report all", b.booksReport(ctx, "hammasi"), "hammasi")

	if line := b.bookTodayLine(ctx, dayStart(time.Now())); !strings.Contains(line, "Kitob:") {
		t.Errorf("today line: %q", line)
	}
	// The book never counts as English.
	var n int
	if err := b.pool.QueryRow(ctx, `SELECT count(*) FROM growth_logs`).Scan(&n); err != nil || n != 0 {
		t.Errorf("growth_logs rows = %d", n)
	}
}

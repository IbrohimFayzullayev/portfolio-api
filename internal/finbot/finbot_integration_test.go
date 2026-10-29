package finbot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	finbotmigrations "github.com/ibrohimcoder/portfolio-api/db/finbot"
	"github.com/ibrohimcoder/portfolio-api/internal/database"
	"github.com/ibrohimcoder/portfolio-api/internal/tgauth"
)

// Integration tests: a real PostgreSQL (TEST_DATABASE_URL, skipped without
// it) and a fake Telegram that records every call. Each run uses a database
// of its own, so it cannot collide with the API's or the ops bot's tests
// running beside it.

const (
	testToken  = "123456:TEST-TOKEN"
	adminID    = int64(1000)
	aliID      = int64(2001)
	valiID     = int64(2002)
	strangerID = int64(2003)
	samiID     = int64(2004)
)

type tgCall struct {
	Method string
	Body   map[string]any
}

type fakeTelegram struct {
	mu     sync.Mutex
	calls  []tgCall
	nextID int64
	srv    *httptest.Server
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	f := &fakeTelegram{nextID: 500}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.calls = append(f.calls, tgCall{method, body})
		f.nextID++
		id := f.nextID
		f.mu.Unlock()

		var result any = true
		switch method {
		case "getMe":
			result = map[string]any{"id": 1, "is_bot": true, "username": "hisob_test_bot"}
		case "sendMessage":
			result = map[string]any{"message_id": id, "chat": map[string]any{"id": body["chat_id"]}}
		case "getUpdates":
			result = []any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// sent returns the messages sent to one chat since the last call, oldest first.
func (f *fakeTelegram) take(chatID int64) []tgCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var mine, rest []tgCall
	for _, c := range f.calls {
		if id, ok := c.Body["chat_id"].(float64); ok && int64(id) == chatID {
			mine = append(mine, c)
		} else {
			rest = append(rest, c)
		}
	}
	f.calls = rest
	return mine
}

func (f *fakeTelegram) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

func textOf(calls []tgCall) string {
	var parts []string
	for _, c := range calls {
		if s, ok := c.Body["text"].(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n---\n")
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("finbot_test_%d", os.Getpid())
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := database.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.MigrateFS(ctx, pool, finbotmigrations.FS, "migrations"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close(context.Background())
	})
	return pool
}

type harness struct {
	t    *testing.T
	b    *Bot
	tg   *fakeTelegram
	pool *pgxpool.Pool
	now  time.Time
	msg  int64
}

func newHarness(t *testing.T) *harness {
	pool := testPool(t)
	tg := newFakeTelegram(t)
	b := New(Config{Token: testToken, AdminID: adminID, WebAppURL: "https://hisob.example.test/pul"}, pool)
	b.tg.baseURL = tg.srv.URL
	b.username = "hisob_test_bot"
	// The real clock: checkpoints are stamped by the database's now(), and
	// a fixed fake date would drift away from it the day after it was written.
	h := &harness{t: t, b: b, tg: tg, pool: pool, now: time.Now()}
	b.now = func() time.Time { return h.now }
	return h
}

func (h *harness) say(from int64, text string) string {
	return h.sayAt(from, text, h.now, 0)
}

func (h *harness) sayAt(from int64, text string, at time.Time, replyTo int64) string {
	h.msg++
	m := &Message{
		MessageID: h.msg,
		From:      &User{ID: from, FirstName: "U" + strconv.FormatInt(from, 10)},
		Chat:      &Chat{ID: from, Type: "private"},
		Text:      text,
		Date:      at.Unix(),
	}
	if replyTo != 0 {
		m.ReplyToMessage = &Message{MessageID: replyTo}
	}
	h.b.handle(context.Background(), Update{UpdateID: h.msg, Message: m})
	return textOf(h.tg.take(from))
}

func (h *harness) press(from int64, data string) []tgCall {
	h.b.handle(context.Background(), Update{CallbackQuery: &CallbackQuery{
		ID: "cb", From: &User{ID: from}, Data: data,
		Message: &Message{MessageID: 99, Chat: &Chat{ID: from, Type: "private"}},
	}})
	return h.tg.take(from)
}

func (h *harness) mustContain(got string, want ...string) {
	h.t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			h.t.Errorf("reply does not contain %q:\n%s", w, got)
		}
	}
}

func (h *harness) count(sql string, args ...any) int64 {
	h.t.Helper()
	var n int64
	if err := h.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) activate(tgID int64) {
	h.t.Helper()
	if _, err := h.b.activate(context.Background(), &User{ID: tgID, FirstName: "U"}, "test"); err != nil {
		h.t.Fatal(err)
	}
}

func TestIntegration_AccessByApproval(t *testing.T) {
	h := newHarness(t)

	got := h.say(strangerID, "/start")
	h.mustContain(got, "So'rovingiz yuborildi")
	adminMsgs := h.tg.take(adminID)
	h.mustContain(textOf(adminMsgs), "Yangi so'rov", strconv.FormatInt(strangerID, 10))
	if len(adminMsgs) != 1 || adminMsgs[0].Body["reply_markup"] == nil {
		t.Fatalf("admin should get one request with buttons, got %+v", adminMsgs)
	}

	// Pending: no entry is written.
	h.mustContain(h.say(strangerID, "tushlik 35k"), "ko'rib chiqilmoqda")
	if n := h.count(`SELECT count(*) FROM entries`); n != 0 {
		t.Fatalf("pending user wrote %d entries", n)
	}
	// A second /start does not ping the admin again.
	h.say(strangerID, "/start")
	if msgs := h.tg.take(adminID); len(msgs) != 0 {
		t.Fatalf("admin pinged twice: %s", textOf(msgs))
	}

	// Only the admin's buttons count.
	h.press(aliID, "adm:ok:"+strconv.FormatInt(strangerID, 10))
	if s := h.count(`SELECT count(*) FROM users WHERE telegram_id = $1 AND status = 'active'`, strangerID); s != 0 {
		t.Fatal("a non-admin approved someone")
	}
	h.press(adminID, "adm:ok:"+strconv.FormatInt(strangerID, 10))
	h.mustContain(textOf(h.tg.take(strangerID)), "Xush kelibsiz")
	h.mustContain(h.say(strangerID, "tushlik 35k"), "35 000 so'm", "Ovqat")

	// Blocked: silence.
	h.press(adminID, "adm:no:"+strconv.FormatInt(strangerID, 10))
	h.tg.reset()
	if got := h.say(strangerID, "taksi 20k"); got != "" {
		t.Fatalf("blocked user got a reply: %s", got)
	}
}

var linkRe = regexp.MustCompile(`start=([a-z0-9]+)`)

func TestIntegration_InviteLink(t *testing.T) {
	h := newHarness(t)
	h.mustContain(h.say(adminID, "/start"), "Xush kelibsiz", "/taklif")

	// Only the admin may make links.
	h.mustContain(h.say(aliID, "/taklif"), "Assalomu")
	reply := h.say(adminID, "/taklif Vali uchun")
	m := linkRe.FindStringSubmatch(reply)
	if m == nil {
		t.Fatalf("no link in %q", reply)
	}
	h.mustContain(h.say(aliID, "/start "+m[1]), "Xush kelibsiz")
	h.mustContain(textOf(h.tg.take(adminID)), "taklif havolasi orqali qo'shildi")

	// Spent: the next person is asked to wait instead.
	h.mustContain(h.say(valiID, "/start "+m[1]), "So'rovingiz yuborildi")

	h.mustContain(h.say(adminID, "/foydalanuvchilar"), "Faol: 2", "kutmoqda: 1", "Moliya boti")
}

func TestIntegration_MoneyFlowAndIsolation(t *testing.T) {
	h := newHarness(t)
	h.activate(aliID)
	h.activate(valiID)

	h.mustContain(h.say(aliID, "/balans 5mln"), "Boshlang'ich summa")
	h.mustContain(h.say(aliID, "+10mln oylik"), "Kirim", "Qoldi: <b>15 000 000 so'm</b>")
	h.mustContain(h.say(aliID, "tushlik 35k"), "Bugun jami: 35 000 so'm", "Qoldi: <b>14 965 000 so'm</b>")
	h.mustContain(h.say(aliID, "Sardorga qarz 500k"), "Sardor sizga qarzdor: <b>500 000 so'm</b>")
	h.mustContain(h.say(aliID, "korzinka 200k\ntaksi 30k"), "2 ta yozildi", "230 000 so'm")

	// Written yesterday, read today (the bot was down): yesterday's row.
	h.say(aliID, "kofe 25k") // today
	h.sayAt(aliID, "kino 90k", h.now.AddDate(0, 0, -1), 0)
	var day time.Time
	if err := h.pool.QueryRow(context.Background(),
		`SELECT spent_on FROM entries WHERE note = 'kino'`).Scan(&day); err != nil {
		t.Fatal(err)
	}
	if want := h.b.dayOf(h.now.Unix()).AddDate(0, 0, -1).Format("2006-01-02"); day.Format("2006-01-02") != want {
		t.Errorf("kino spent_on = %s, want %s", day.Format("2006-01-02"), want)
	}

	report := h.say(aliID, "/hisob")
	h.mustContain(report, "Kirim: 10 000 000 so'm", "Sarflandi", "Kategoriyalar", "Qoldi:", "Sizga qarzdor")
	h.mustContain(h.say(aliID, "/qarzlar"), "Sardor", "500 000 so'm")
	h.mustContain(h.say(aliID, "/qarzlar Sardor"), "Sardor")

	// Vali sees none of it.
	h.mustContain(h.say(valiID, "/oxirgi"), "Hali hech narsa yozilmagan")
	h.mustContain(h.say(valiID, "/qarzlar"), "Ochiq qarz yo'q")
	h.mustContain(h.say(valiID, "/hisob"), "hech narsa yozilmagan")

	// Nor can Vali touch Ali's rows with a forged button.
	var aliEntry string
	if err := h.pool.QueryRow(context.Background(),
		`SELECT id::text FROM entries WHERE note = 'tushlik'`).Scan(&aliEntry); err != nil {
		t.Fatal(err)
	}
	h.press(valiID, "fn:delok:"+aliEntry)
	h.press(valiID, "fc:transport:"+aliEntry)
	if n := h.count(`SELECT count(*) FROM entries WHERE id = $1 AND category = 'ovqat'`, aliEntry); n != 1 {
		t.Fatal("Vali changed Ali's entry")
	}

	// Editing by question and answer: the prompt is answered by reply.
	h.press(aliID, "fn:amt:"+aliEntry)
	if n := h.count(`SELECT count(*) FROM prompts WHERE user_id = (SELECT id FROM users WHERE telegram_id = $1)`, aliID); n != 1 {
		t.Fatalf("prompts = %d, want 1", n)
	}
	var mid int64
	_ = h.pool.QueryRow(context.Background(), `SELECT message_id FROM prompts`).Scan(&mid)
	// Vali replying to Ali's question is an ordinary message of Vali's: the
	// question stays open and Ali's row is untouched.
	h.sayAt(valiID, "1k", h.now, mid)
	if n := h.count(`SELECT count(*) FROM prompts`); n != 1 {
		t.Fatal("Vali's reply consumed Ali's question")
	}
	h.mustContain(h.sayAt(aliID, "40k", h.now, mid), "Saqlandi", "40 000 so'm")
	if n := h.count(`SELECT count(*) FROM entries WHERE id = $1 AND amount_minor = 4000000`, aliEntry); n != 1 {
		t.Fatal("amount was not updated")
	}

	// Category change from the picker.
	h.press(aliID, "fc:dam:"+aliEntry)
	if n := h.count(`SELECT count(*) FROM entries WHERE id = $1 AND category = 'dam'`, aliEntry); n != 1 {
		t.Fatal("category was not updated")
	}

	// Morning summary: once per day, only to those with entries.
	h.activate(samiID)
	h.tg.reset()
	h.b.sendSummaries(context.Background(), h.b.dayOf(h.now.Unix()))
	h.mustContain(textOf(h.tg.take(aliID)), "Xayrli tong", "Balans")
	if got := textOf(h.tg.take(samiID)); got != "" {
		t.Errorf("Sami (no entries) got a summary: %s", got)
	}
	h.b.sendSummaries(context.Background(), h.b.dayOf(h.now.Unix()))
	if got := textOf(h.tg.take(aliID)); got != "" {
		t.Errorf("second summary on the same day: %s", got)
	}

	h.mustContain(h.say(aliID, "/xulosa off"), "o'chirildi")

	// Delete everything: needs the confirm button, and only touches Ali.
	h.say(valiID, "non 5k")
	h.say(aliID, "/ochirish")
	h.press(aliID, "del:all")
	if n := h.count(`SELECT count(*) FROM entries e JOIN users u ON u.id = e.user_id WHERE u.telegram_id = $1`, aliID); n != 0 {
		t.Fatalf("Ali still has %d entries", n)
	}
	if n := h.count(`SELECT count(*) FROM entries e JOIN users u ON u.id = e.user_id WHERE u.telegram_id = $1`, valiID); n != 2 {
		t.Fatalf("Vali has %d entries, want 2", n)
	}
}

/* ---------------------------------- web ----------------------------------- */

func initData(t *testing.T, tgID int64, at time.Time) string {
	t.Helper()
	user, _ := json.Marshal(map[string]any{"id": tgID, "first_name": "U"})
	return tgauth.Sign(url.Values{
		"auth_date": {strconv.FormatInt(at.Unix(), 10)},
		"user":      {string(user)},
	}, testToken)
}

func (h *harness) web(method, path, token string, body any) (int, map[string]any) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.b.Handler().ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (h *harness) signIn(tgID int64) string {
	h.t.Helper()
	code, out := h.web("POST", "/api/auth", "", map[string]string{"init_data": initData(h.t, tgID, time.Now())})
	if code != http.StatusOK {
		h.t.Fatalf("sign-in %d: %d %v", tgID, code, out)
	}
	return out["token"].(string)
}

func TestIntegration_WebAPI(t *testing.T) {
	h := newHarness(t)
	h.b.now = time.Now // tokens, init data and checkpoints follow the real clock
	h.activate(aliID)
	h.activate(valiID)
	h.say(aliID, "/balans 1mln")
	h.say(aliID, "tushlik 35k")
	h.say(aliID, "Sardorga qarz 100k")

	// Not let in: unknown account, forged signature.
	if code, _ := h.web("POST", "/api/auth", "", map[string]string{"init_data": initData(t, strangerID, time.Now())}); code != http.StatusForbidden {
		t.Errorf("stranger sign-in = %d, want 403", code)
	}
	forged := strings.Replace(initData(t, aliID, time.Now()), "hash=", "hash=00", 1)
	if code, _ := h.web("POST", "/api/auth", "", map[string]string{"init_data": forged}); code != http.StatusUnauthorized {
		t.Errorf("forged sign-in = %d, want 401", code)
	}
	if code, _ := h.web("GET", "/api/finance/summary", "", nil); code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", code)
	}

	ali, vali := h.signIn(aliID), h.signIn(valiID)

	code, sum := h.web("GET", "/api/finance/summary", ali, nil)
	if code != http.StatusOK {
		t.Fatalf("summary = %d %v", code, sum)
	}
	if spent := sum["spent"].(map[string]any)["UZS"].(float64); spent != 3500000 {
		t.Errorf("Ali spent = %v, want 3500000", spent)
	}
	inHand := sum["balance"].(map[string]any)["in_hand"].(map[string]any)["UZS"].(float64)
	if inHand != 100000000-3500000-10000000 {
		t.Errorf("Ali in hand = %v", inHand)
	}

	_, vsum := h.web("GET", "/api/finance/summary", vali, nil)
	if len(vsum["spent"].(map[string]any)) != 0 || len(vsum["debts"].(map[string]any)["balances"].([]any)) != 0 {
		t.Errorf("Vali sees Ali's money: %v", vsum)
	}

	_, list := h.web("GET", "/api/finance/entries", ali, nil)
	entries := list["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("Ali entries = %d, want 2", len(entries))
	}
	var aliEntry string
	for _, e := range entries {
		if e.(map[string]any)["note"] == "tushlik" {
			aliEntry = e.(map[string]any)["id"].(string)
		}
	}

	// Vali cannot change or delete Ali's row.
	edit := map[string]any{"kind": "expense", "amount_minor": 100, "currency": "UZS", "category": "ovqat", "spent_on": "2026-10-01"}
	if code, _ := h.web("PUT", "/api/finance/entries/"+aliEntry, vali, edit); code != http.StatusNotFound {
		t.Errorf("Vali PUT on Ali's entry = %d, want 404", code)
	}
	if code, _ := h.web("DELETE", "/api/finance/entries/"+aliEntry, vali, nil); code != http.StatusNotFound {
		t.Errorf("Vali DELETE on Ali's entry = %d, want 404", code)
	}

	// Ali can, from the web.
	if code, out := h.web("PUT", "/api/finance/entries/"+aliEntry, ali, edit); code != http.StatusOK {
		t.Errorf("Ali PUT = %d %v", code, out)
	}
	if code, out := h.web("POST", "/api/finance/entries", ali, map[string]any{
		"kind": "income", "amount_minor": 500000000, "currency": "UZS", "category": "oylik", "spent_on": todayTashkent(time.Now()).Format(dateLayout),
	}); code != http.StatusCreated {
		t.Errorf("Ali POST = %d %v", code, out)
	}
	if code, out := h.web("POST", "/api/finance/checkpoints", ali, map[string]any{"currency": "UZS", "amount_minor": 600000000}); code != http.StatusCreated || out["computed_minor"] == nil {
		t.Errorf("Ali checkpoint = %d %v", code, out)
	}
	_, debts := h.web("GET", "/api/finance/debts", ali, nil)
	if debts["people"].(float64) != 1 {
		t.Errorf("Ali debts = %v", debts)
	}
	if code, _ := h.web("GET", "/api/finance/meta", vali, nil); code != http.StatusOK {
		t.Errorf("meta = %d", code)
	}

	// Blocking takes effect on the next request, not when the token expires.
	h.b.setStatus(context.Background(), valiID, statusBlocked)
	if code, _ := h.web("GET", "/api/finance/summary", vali, nil); code != http.StatusForbidden {
		t.Errorf("blocked token = %d, want 403", code)
	}
}

func (h *harness) adminCall(method, path, key string, body any) (int, map[string]any) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, reader)
	if key != "" {
		req.Header.Set(adminKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	h.b.Handler().ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestIntegration_AdminAPI(t *testing.T) {
	h := newHarness(t)
	h.b.now = time.Now

	// Without a key configured the admin API does not exist at all.
	if code, _ := h.adminCall("GET", "/internal/admin/users", "", nil); code != http.StatusNotFound {
		t.Fatalf("admin API without FINBOT_ADMIN_KEY = %d, want 404", code)
	}

	h.b.cfg.AdminKey = "secret-key"
	h.b.cfg.AdminSeesMoney = true
	h.activate(aliID)
	h.say(aliID, "/balans 1mln")
	h.say(aliID, "tushlik 35k")
	h.say(aliID, "salom") // the bot cannot read this
	h.say(strangerID, "/start")

	if code, _ := h.adminCall("GET", "/internal/admin/users", "", nil); code != http.StatusUnauthorized {
		t.Errorf("no key = %d, want 401", code)
	}
	if code, _ := h.adminCall("GET", "/internal/admin/users", "wrong", nil); code != http.StatusUnauthorized {
		t.Errorf("wrong key = %d, want 401", code)
	}

	code, out := h.adminCall("GET", "/internal/admin/users", "secret-key", nil)
	if code != http.StatusOK {
		t.Fatalf("users = %d %v", code, out)
	}
	counts := out["counts"].(map[string]any)
	if counts["total"].(float64) != 2 || counts["active"].(float64) != 1 || counts["pending"].(float64) != 1 || counts["active_7d"].(float64) != 1 {
		t.Errorf("counts = %v", counts)
	}
	var ali map[string]any
	for _, u := range out["users"].([]any) {
		if m := u.(map[string]any); m["telegram_id"].(float64) == float64(aliID) {
			ali = m
		}
	}
	if ali == nil || ali["entries"].(float64) != 1 || ali["failures_30d"].(float64) != 1 || ali["checkpoints"].(float64) != 1 {
		t.Fatalf("ali = %v", ali)
	}
	money := ali["money"].(map[string]any)
	if money["month_spent"].(map[string]any)["UZS"].(float64) != 3500000 {
		t.Errorf("ali money = %v", money)
	}
	aliPath := fmt.Sprintf("/internal/admin/users/%.0f", ali["id"].(float64))

	_, detail := h.adminCall("GET", aliPath, "secret-key", nil)
	failures := detail["failures"].([]any)
	if len(failures) != 1 || failures[0].(map[string]any)["text"] != "salom" {
		t.Errorf("failures = %v", failures)
	}

	code, sum := h.adminCall("GET", aliPath+"/finance/summary", "secret-key", nil)
	if code != http.StatusOK || sum["spent"].(map[string]any)["UZS"].(float64) != 3500000 {
		t.Errorf("ali summary for admin = %d %v", code, sum)
	}
	// Read-only: the admin path has no write routes.
	if code, _ := h.adminCall("POST", aliPath+"/finance/entries", "secret-key", map[string]any{}); code != http.StatusMethodNotAllowed && code != http.StatusNotFound {
		t.Errorf("admin write = %d, want 404/405", code)
	}

	// Approving from the dashboard tells the person, as the bot's button does.
	var strangerPath string
	for _, u := range out["users"].([]any) {
		if m := u.(map[string]any); m["telegram_id"].(float64) == float64(strangerID) {
			strangerPath = fmt.Sprintf("/internal/admin/users/%.0f", m["id"].(float64))
		}
	}
	h.tg.reset()
	if code, out := h.adminCall("POST", strangerPath+"/status", "secret-key", map[string]string{"status": "active"}); code != http.StatusOK {
		t.Fatalf("approve = %d %v", code, out)
	}
	welcome := textOf(h.tg.take(strangerID))
	h.mustContain(welcome, "Xush kelibsiz", "boshqa foydalanuvchilar ko'rmaydi")
	// While the admin can see amounts, the bot must not claim otherwise.
	if strings.Contains(welcome, "bot egasi ham") {
		t.Errorf("welcome promises the owner cannot see, but AdminSeesMoney is on:\n%s", welcome)
	}

	// Money hidden: who and how often, never how much.
	h.b.cfg.AdminSeesMoney = false
	_, out = h.adminCall("GET", "/internal/admin/users", "secret-key", nil)
	for _, u := range out["users"].([]any) {
		if u.(map[string]any)["money"] != nil {
			t.Errorf("money shown while hidden: %v", u)
		}
	}
	if code, _ := h.adminCall("GET", aliPath+"/finance/summary", "secret-key", nil); code != http.StatusForbidden {
		t.Errorf("summary while hidden = %d, want 403", code)
	}
	if _, d := h.adminCall("GET", aliPath, "secret-key", nil); len(d["failures"].([]any)) != 0 {
		t.Errorf("failure texts shown while money is hidden")
	}
	h.mustContain(h.say(aliID, "/help"), "bot egasi ham bot orqali ko'rmaydi")
}

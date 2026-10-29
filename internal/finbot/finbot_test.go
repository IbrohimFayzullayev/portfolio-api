package finbot

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestReportPeriod(t *testing.T) {
	today := time.Date(2026, 10, 29, 0, 0, 0, 0, tashkent)
	cases := []struct {
		arg, from, to string
		compare       bool
	}{
		{"", "2026-10-01", "2026-10-30", true},
		{"hafta", "2026-10-23", "2026-10-30", true},
		{"o'tgan", "2026-09-01", "2026-10-01", true},
		{"sentabr", "2026-09-01", "2026-10-01", false},
		{"dekabr", "2025-12-01", "2026-01-01", false},
		{"yil", "2026-01-01", "2026-10-30", false},
		{"kecha", "2026-10-28", "2026-10-29", false},
	}
	for _, c := range cases {
		p := reportPeriod(c.arg, today)
		if got := p.from.Format(dateLayout) + " " + p.to.Format(dateLayout); got != c.from+" "+c.to {
			t.Errorf("%q: %s, want %s %s", c.arg, got, c.from, c.to)
		}
		if (p.compare != nil) != c.compare {
			t.Errorf("%q: compare = %v, want %v", c.arg, p.compare != nil, c.compare)
		}
	}
}

func TestMonthProjectionIsWholeSom(t *testing.T) {
	today := time.Date(2026, 10, 29, 0, 0, 0, 0, tashkent)
	end, ok := monthProjection(today, money{"UZS": 10_000_000_00}, money{"UZS": 365_000_00})
	if !ok || end%100 != 0 {
		t.Fatalf("projection = %d, %v", end, ok)
	}
	if _, ok := monthProjection(time.Date(2026, 10, 3, 0, 0, 0, 0, tashkent), money{"UZS": 1}, money{}); ok {
		t.Error("projection after two days should not be shown")
	}
}

func TestInviteCodeFitsStartPayload(t *testing.T) {
	// Telegram allows A-Z, a-z, 0-9, _ and - in a /start payload, 64 at most.
	re := regexp.MustCompile(`^[a-z0-9]{8,64}$`)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		code, err := newInviteCode()
		if err != nil || !re.MatchString(code) || seen[code] {
			t.Fatalf("bad code %q (%v)", code, err)
		}
		seen[code] = true
	}
}

func TestClientIPTrustsOnlyTheLastHop(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.18.0.5:41000"
	if got := clientIP(r); got != "172.18.0.5" {
		t.Errorf("no header: %q", got)
	}
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.7")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Errorf("forged first hop was trusted: %q", got)
	}
}

func TestAuthLimiter(t *testing.T) {
	l := newAuthLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !l.allow("a") {
			t.Fatal("refused within the limit")
		}
	}
	if l.allow("a") {
		t.Error("allowed past the limit")
	}
	if !l.allow("b") {
		t.Error("one address limited another")
	}
}

func TestHelpTextHidesAdminCommands(t *testing.T) {
	if strings.Contains((&Bot{}).helpText(false), "/taklif") {
		t.Error("admin commands shown to a user")
	}
	if !strings.Contains((&Bot{}).helpText(true), "/taklif") {
		t.Error("admin commands missing for the admin")
	}
}

func TestCallbackDataFitsTelegramLimit(t *testing.T) {
	id := "123e4567-e89b-12d3-a456-426614174000"
	for _, row := range entryKeyboard(id).InlineKeyboard {
		for _, b := range row {
			if len(b.CallbackData) > 64 {
				t.Errorf("%q is %d bytes", b.CallbackData, len(b.CallbackData))
			}
		}
	}
	e := entry{id: id, kind: "expense"}
	_, cats := categoryPicker(e)
	_, kinds := kindPicker(e)
	for _, kb := range []*inlineKeyboard{cats, kinds} {
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				if len(b.CallbackData) > 64 {
					t.Errorf("%q is %d bytes", b.CallbackData, len(b.CallbackData))
				}
			}
		}
	}
}

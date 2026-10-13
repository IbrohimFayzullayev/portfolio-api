package api

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ibrohimcoder/portfolio-api/internal/config"
	"github.com/ibrohimcoder/portfolio-api/internal/tgauth"
)

// These run without a database: every request here is decided by the auth
// layer before any query, which is exactly the property being tested.

const testBotToken = "123456:TEST-token"

func viewerServer(allowed ...int64) *Server {
	return NewServer(&config.Config{
		Env:                "test",
		JWTSecret:          "unit-test-secret-at-least-32-chars!",
		JWTTTL:             time.Hour,
		CORSOrigins:        []string{"https://admin.example.com"},
		TelegramBotToken:   testBotToken,
		TelegramAllowedIDs: allowed,
	}, nil)
}

func initDataFor(userID int64, at time.Time) string {
	return tgauth.Sign(url.Values{
		"auth_date": {strconv.FormatInt(at.Unix(), 10)},
		"user":      {`{"id":` + strconv.FormatInt(userID, 10) + `,"first_name":"Test"}`},
	}, testBotToken)
}

func viewerToken(t *testing.T, s *Server, userID int64) string {
	t.Helper()
	rec := do(t, s, http.MethodPost, "/api/v1/tg/auth", "",
		map[string]string{"init_data": initDataFor(userID, time.Now())})
	if rec.Code != http.StatusOK {
		t.Fatalf("tg auth: %d %s", rec.Code, rec.Body.String())
	}
	var out telegramAuthResponse
	decode(t, rec, &out)
	if out.Token == "" || out.User.ID != userID {
		t.Fatalf("tg auth response: %+v", out)
	}
	return out.Token
}

func TestTelegramAuth(t *testing.T) {
	s := viewerServer(42)

	viewerToken(t, s, 42) // the happy path

	tests := []struct {
		name string
		body map[string]string
		want int
	}{
		{"not on the allow-list", map[string]string{"init_data": initDataFor(7, time.Now())}, http.StatusForbidden},
		{"signed with another bot's token", map[string]string{"init_data": tgauth.Sign(url.Values{
			"auth_date": {strconv.FormatInt(time.Now().Unix(), 10)},
			"user":      {`{"id":42}`},
		}, "999:other")}, http.StatusUnauthorized},
		{"stale", map[string]string{"init_data": initDataFor(42, time.Now().Add(-48*time.Hour))}, http.StatusUnauthorized},
		{"empty", map[string]string{"init_data": ""}, http.StatusUnauthorized},
	}
	for _, tc := range tests {
		if rec := do(t, s, http.MethodPost, "/api/v1/tg/auth", "", tc.body); rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestTelegramAuthDisabledWithoutConfig(t *testing.T) {
	s := NewServer(&config.Config{Env: "test", JWTSecret: "x", JWTTTL: time.Hour}, nil)
	rec := do(t, s, http.MethodPost, "/api/v1/tg/auth", "",
		map[string]string{"init_data": initDataFor(42, time.Now())})
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

// The core promise: a viewer token reads the money pages and nothing else.
func TestViewerTokenIsReadOnlyAndFinanceOnly(t *testing.T) {
	s := viewerServer(42)
	tok := viewerToken(t, s, 42)
	id := uuid.New().String()

	// /finance/meta needs no database, so it shows the token is accepted.
	if rec := do(t, s, http.MethodGet, "/api/v1/finance/meta", tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer GET /finance/meta: %d %s", rec.Code, rec.Body.String())
	}

	refused := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/finance/entries", map[string]any{"kind": "expense"}},
		{http.MethodPut, "/api/v1/finance/entries/" + id, map[string]any{"kind": "expense"}},
		{http.MethodDelete, "/api/v1/finance/entries/" + id, nil},
		{http.MethodGet, "/api/v1/posts", nil},
		{http.MethodGet, "/api/v1/messages", nil},
		{http.MethodGet, "/api/v1/system", nil},
		{http.MethodGet, "/api/v1/auth/me", nil},
		{http.MethodDelete, "/api/v1/posts/" + id, nil},
		{http.MethodPost, "/api/v1/finance/checkpoints", map[string]any{"amount_minor": 1}},
		{http.MethodDelete, "/api/v1/finance/checkpoints/" + id, nil},
	}
	for _, tc := range refused {
		rec := do(t, s, tc.method, tc.path, tok, tc.body)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("viewer %s %s: status %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestFinanceReadsNeedSomeToken(t *testing.T) {
	s := viewerServer(42)
	if rec := do(t, s, http.MethodGet, "/api/v1/finance/meta", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: status %d, want 401", rec.Code)
	}
	if rec := do(t, s, http.MethodGet, "/api/v1/finance/meta", "garbage", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("garbage token: status %d, want 401", rec.Code)
	}
}

// The admin session keeps working on the shared read routes.
func TestAdminTokenReadsFinance(t *testing.T) {
	s := viewerServer(42)
	tok, _, err := s.tokens.Generate(uuid.New(), "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, s, http.MethodGet, "/api/v1/finance/meta", tok, nil); rec.Code != http.StatusOK {
		t.Errorf("admin GET /finance/meta: %d %s", rec.Code, rec.Body.String())
	}
}

// Taking an id off the allow-list cuts off tokens already issued to it.
func TestViewerRevokedWhenRemovedFromAllowList(t *testing.T) {
	s := viewerServer(42)
	tok := viewerToken(t, s, 42)
	s.cfg.TelegramAllowedIDs = []int64{99}
	if rec := do(t, s, http.MethodGet, "/api/v1/finance/meta", tok, nil); rec.Code != http.StatusForbidden {
		t.Errorf("revoked viewer: status %d, want 403", rec.Code)
	}
}

func TestFinanceEntryInputValidate(t *testing.T) {
	today := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	ok := func() financeEntryInput {
		return financeEntryInput{Kind: "expense", AmountMinor: 3_500_000, Currency: "UZS",
			Category: "ovqat", Note: "tushlik", SpentOn: "2026-10-13"}
	}

	in := ok()
	in.normalize()
	if err := in.validate(today); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}

	// Debt kinds are forced into "qarz" and must name someone.
	debt := ok()
	debt.Kind = "lent"
	debt.normalize()
	if debt.Category != "qarz" {
		t.Errorf("debt category = %q", debt.Category)
	}
	if err := debt.validate(today); err == nil {
		t.Error("debt without a person accepted")
	}

	// Leaving debt does not keep "qarz".
	back := ok()
	back.Category = "qarz"
	back.normalize()
	if back.Category != "boshqa" {
		t.Errorf("expense kept category %q", back.Category)
	}

	// Income takes an income category; a spending one falls back.
	inc := ok()
	inc.Kind = "income"
	inc.normalize()
	if inc.Category != "kirim_boshqa" {
		t.Errorf("income with a spending category kept %q", inc.Category)
	}
	if err := inc.validate(today); err != nil {
		t.Errorf("income rejected: %v", err)
	}

	bad := []func(*financeEntryInput){
		func(i *financeEntryInput) { i.Kind = "salary" },
		func(i *financeEntryInput) { i.AmountMinor = 0 },
		func(i *financeEntryInput) { i.AmountMinor = -5 },
		func(i *financeEntryInput) { i.Currency = "EUR" },
		func(i *financeEntryInput) { i.Category = "nonsense" },
		func(i *financeEntryInput) { i.SpentOn = "13.10.2026" },
		func(i *financeEntryInput) { i.SpentOn = "2026-10-20" },
	}
	for n, mutate := range bad {
		in := ok()
		mutate(&in)
		in.normalize()
		if err := in.validate(today); err == nil {
			t.Errorf("bad input %d accepted: %+v", n, in)
		}
	}
}

func TestParseUUIDParamRejectsJunk(t *testing.T) {
	s := viewerServer(42)
	tok, _, _ := s.tokens.Generate(uuid.New(), "admin@example.com")
	rec := do(t, s, http.MethodDelete, "/api/v1/finance/entries/not-a-uuid", tok, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
}

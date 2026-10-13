package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestClient points a real telegramClient at a stub server, so the request
// path under test is the same one production uses.
func newTestClient(t *testing.T, status int, body string) *telegramClient {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := newTelegramClient("test-token")
	c.baseURL = srv.URL
	return c
}

func TestSendMessageRateLimited(t *testing.T) {
	c := newTestClient(t, http.StatusTooManyRequests,
		`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 17","parameters":{"retry_after":17}}`)

	err := c.sendMessage(context.Background(), 1, "hello")

	var te *telegramError
	if !errors.As(err, &te) {
		t.Fatalf("got %T (%v), want *telegramError", err, err)
	}
	if te.RetryAfter != 17*time.Second {
		t.Errorf("RetryAfter = %s, want 17s", te.RetryAfter)
	}
	// A rate limit is not the message's fault: treating it as permanent would
	// park a message that was never wrong.
	if te.permanent() {
		t.Error("429 reported as permanent")
	}
}

func TestSendMessagePermanentError(t *testing.T) {
	c := newTestClient(t, http.StatusBadRequest,
		`{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`)

	err := c.sendMessage(context.Background(), 1, "<b>broken")

	var te *telegramError
	if !errors.As(err, &te) {
		t.Fatalf("got %T (%v), want *telegramError", err, err)
	}
	if !te.permanent() {
		t.Error("400 not reported as permanent")
	}
	if te.RetryAfter != 0 {
		t.Errorf("RetryAfter = %s, want 0", te.RetryAfter)
	}
}

func TestSendMessageServerErrorIsRetriable(t *testing.T) {
	c := newTestClient(t, http.StatusBadGateway,
		`{"ok":false,"error_code":502,"description":"Bad Gateway"}`)

	err := c.sendMessage(context.Background(), 1, "hello")

	var te *telegramError
	if !errors.As(err, &te) {
		t.Fatalf("got %T (%v), want *telegramError", err, err)
	}
	if te.permanent() {
		t.Error("502 reported as permanent")
	}
}

// A failure that never reaches the JSON envelope still has to classify as
// something: the HTTP status is the only signal there is.
func TestSendMessageStatusOnlyError(t *testing.T) {
	c := newTestClient(t, http.StatusForbidden, `{"ok":false,"description":"Forbidden"}`)

	err := c.sendMessage(context.Background(), 1, "hello")

	var te *telegramError
	if !errors.As(err, &te) {
		t.Fatalf("got %T (%v), want *telegramError", err, err)
	}
	if te.Code != http.StatusForbidden {
		t.Errorf("Code = %d, want 403", te.Code)
	}
	if !te.permanent() {
		t.Error("403 not reported as permanent")
	}
}

func TestSendMessageSuccess(t *testing.T) {
	c := newTestClient(t, http.StatusOK, `{"ok":true,"result":{"message_id":1}}`)

	if err := c.sendMessage(context.Background(), 1, "hello"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A Mini App button must not also carry callback_data: Telegram rejects a
// button with two actions.
func TestWebAppButtonJSON(t *testing.T) {
	b := &Bot{cfg: Config{AdminSiteURL: "https://admin.example.com/"}}
	raw, err := json.Marshal(b.webAppKeyboard())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"inline_keyboard":[[{"text":"📊 Grafiklarni ochish","web_app":{"url":"https://admin.example.com/tg"}}]]}`
	if string(raw) != want {
		t.Errorf("keyboard JSON:\n got  %s\n want %s", raw, want)
	}

	if (&Bot{cfg: Config{AdminSiteURL: "http://localhost:3001"}}).webAppKeyboard() != nil {
		t.Error("non-https admin URL should give no Mini App button")
	}
}

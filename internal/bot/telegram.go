package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Minimal Telegram Bot API client.
//
// Deliberately written against net/http instead of pulling in a bot framework:
// this bot uses exactly two endpoints, and an extra dependency in the API's
// go.mod would have to be justified for the whole module.

const telegramAPI = "https://api.telegram.org"

type telegramClient struct {
	token string
	http  *http.Client
}

func newTelegramClient(token string) *telegramClient {
	return &telegramClient{
		token: token,
		// Longer than the getUpdates long-poll timeout so the poll, not the
		// transport, decides when a request ends.
		http: &http.Client{Timeout: 70 * time.Second},
	}
}

/* ------------------------------- payloads -------------------------------- */

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      *Chat  `json:"chat"`
	Text      string `json:"text"`
	Date      int64  `json:"date"`
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

/* -------------------------------- calls ---------------------------------- */

func (c *telegramClient) call(ctx context.Context, method string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}

	endpoint := fmt.Sprintf("%s/bot%s/%s", telegramAPI, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	var parsed apiResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&parsed); err != nil {
		return fmt.Errorf("%s: decode response: %w", method, err)
	}
	if !parsed.OK {
		return fmt.Errorf("%s: telegram error: %s", method, parsed.Description)
	}
	if out != nil {
		return json.Unmarshal(parsed.Result, out)
	}
	return nil
}

// getUpdates long-polls. Telegram holds the request open until something
// arrives or timeout elapses, so this is cheap despite looking like a loop.
func (c *telegramClient) getUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var updates []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeout,
		"allowed_updates": []string{"message"},
	}, &updates)
	return updates, err
}

func (c *telegramClient) sendMessage(ctx context.Context, chatID int64, text string) error {
	return c.call(ctx, "sendMessage", map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}, nil)
}

// getMe is used once at startup to prove the token works and to log the bot's
// identity — a wrong token should fail loudly, not silently poll forever.
func (c *telegramClient) getMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", nil, &u)
	return u, err
}

// escapeHTML keeps user- and database-supplied text from breaking the HTML
// parse mode (labels come from the invitation site, not from us).
func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

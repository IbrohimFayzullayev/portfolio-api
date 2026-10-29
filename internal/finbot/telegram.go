package finbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// A small Telegram Bot API client: the dozen calls this bot makes, over
// net/http. It is a separate copy from internal/bot's on purpose. The two
// bots are separate products with separate tokens, and a change made for one
// (a new field, a retry rule) should not ship to the other by accident.

const telegramAPI = "https://api.telegram.org"

type telegramClient struct {
	token   string
	baseURL string // tests point this at an httptest server
	http    *http.Client
}

func newTelegramClient(token string) *telegramClient {
	return &telegramClient{
		token:   token,
		baseURL: telegramAPI,
		// Longer than the getUpdates long-poll, so the poll decides when a
		// request ends rather than the transport.
		http: &http.Client{Timeout: 70 * time.Second},
	}
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Message struct {
	MessageID      int64    `json:"message_id"`
	From           *User    `json:"from"`
	Chat           *Chat    `json:"chat"`
	Text           string   `json:"text"`
	Date           int64    `json:"date"`
	ReplyToMessage *Message `json:"reply_to_message"`
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // "private", "group", ...
}

type inlineButton struct {
	Text         string      `json:"text"`
	CallbackData string      `json:"callback_data,omitempty"` // 64 bytes at most
	WebApp       *webAppInfo `json:"web_app,omitempty"`
	URL          string      `json:"url,omitempty"`
}

type webAppInfo struct {
	URL string `json:"url"`
}

type inlineKeyboard struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

type telegramError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *telegramError) Error() string {
	return fmt.Sprintf("%s: telegram %d: %s", e.Method, e.Code, e.Description)
}

func (c *telegramClient) call(ctx context.Context, method string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	endpoint := fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		// The URL holds the token; never let it reach a log line.
		return fmt.Errorf("%s: %s", method, strings.ReplaceAll(err.Error(), c.token, "<token>"))
	}
	defer res.Body.Close()

	var parsed apiResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&parsed); err != nil {
		return fmt.Errorf("%s: decode response: %w", method, err)
	}
	if !parsed.OK {
		te := &telegramError{Method: method, Code: parsed.ErrorCode, Description: parsed.Description}
		if te.Code == 0 {
			te.Code = res.StatusCode
		}
		if parsed.Parameters != nil && parsed.Parameters.RetryAfter > 0 {
			te.RetryAfter = time.Duration(parsed.Parameters.RetryAfter) * time.Second
		}
		return te
	}
	if out != nil {
		return json.Unmarshal(parsed.Result, out)
	}
	return nil
}

func (c *telegramClient) getMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", nil, &u)
	return u, err
}

func (c *telegramClient) getUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var updates []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeout,
		"allowed_updates": []string{"message", "callback_query"},
	}, &updates)
	return updates, err
}

func (c *telegramClient) sendMessage(ctx context.Context, chatID int64, text string, markup *inlineKeyboard) error {
	body := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if markup != nil {
		body["reply_markup"] = markup
	}
	return c.call(ctx, "sendMessage", body, nil)
}

func (c *telegramClient) editMessageText(ctx context.Context, chatID, messageID int64, text string, markup *inlineKeyboard) error {
	body := map[string]any{
		"chat_id":                  chatID,
		"message_id":               messageID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if markup != nil {
		body["reply_markup"] = markup
	}
	err := c.call(ctx, "editMessageText", body, nil)
	// Pressing a button that changes nothing (the same category twice) makes
	// Telegram refuse the edit. That is not a failure worth logging.
	var te *telegramError
	if errors.As(err, &te) && strings.Contains(te.Description, "message is not modified") {
		return nil
	}
	return err
}

type forceReply struct {
	ForceReply            bool   `json:"force_reply"`
	InputFieldPlaceholder string `json:"input_field_placeholder,omitempty"`
}

// sendPrompt asks a question with force_reply and returns its message id,
// which the answer will point back at.
func (c *telegramClient) sendPrompt(ctx context.Context, chatID int64, text, placeholder string) (int64, error) {
	var sent Message
	err := c.call(ctx, "sendMessage", map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
		"reply_markup": forceReply{
			ForceReply:            true,
			InputFieldPlaceholder: placeholder,
		},
	}, &sent)
	return sent.MessageID, err
}

func (c *telegramClient) answerCallback(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": id,
		"text":              text,
	}, nil)
}

type menuButton struct {
	Type   string      `json:"type"`
	Text   string      `json:"text,omitempty"`
	WebApp *webAppInfo `json:"web_app,omitempty"`
}

func (c *telegramClient) setChatMenuButton(ctx context.Context, button menuButton) error {
	return c.call(ctx, "setChatMenuButton", map[string]any{"menu_button": button}, nil)
}

type botCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// setMyCommands sets the command menu, for everyone (chatID 0) or for one
// private chat: the admin sees two commands nobody else needs.
func (c *telegramClient) setMyCommands(ctx context.Context, commands []botCommand, chatID int64) error {
	body := map[string]any{"commands": commands}
	if chatID != 0 {
		body["scope"] = map[string]any{"type": "chat", "chat_id": chatID}
	}
	return c.call(ctx, "setMyCommands", body, nil)
}

// escapeHTML keeps typed text from breaking the HTML parse mode.
func escapeHTML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

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
// this bot uses a handful of endpoints, and an extra dependency in the API's
// go.mod would have to be justified for the whole module.

const telegramAPI = "https://api.telegram.org"

type telegramClient struct {
	token string
	// Separate from the constant so tests can point the client at an
	// httptest server; nothing else ever sets it.
	baseURL string
	http    *http.Client
}

func newTelegramClient(token string) *telegramClient {
	return &telegramClient{
		token:   token,
		baseURL: telegramAPI,
		// Longer than the getUpdates long-poll timeout so the poll, not the
		// transport, decides when a request ends.
		http: &http.Client{Timeout: 70 * time.Second},
	}
}

/* ------------------------------- payloads -------------------------------- */

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// CallbackQuery arrives when an inline button is pressed. Telegram shows a
// loading spinner on the button until answerCallback is called, so every
// handler must answer even when it has nothing to say.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

/* ---------------------------- inline keyboards --------------------------- */

type inlineButton struct {
	Text string `json:"text"`
	// Max 64 bytes, Telegram's limit. A UUID with a short prefix fits.
	CallbackData string `json:"callback_data"`
}

type inlineKeyboard struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      *Chat  `json:"chat"`
	Text      string `json:"text"`
	Date      int64  `json:"date"`

	// Set when this message answers one the bot sent with force_reply. It is
	// the whole conversation state: which question is being answered is a
	// property of the message, not of a map in this process.
	ReplyToMessage *Message `json:"reply_to_message"`

	// A .md file sent instead of typing a post into a chat box.
	Document *Document `json:"document"`
	// Text sent with the file, if any.
	Caption string `json:"caption"`
}

type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
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
	ErrorCode   int             `json:"error_code"`
	// Telegram answers a rate limit with the exact number of seconds to wait.
	// Guessing instead of reading it is how a bot earns a longer ban.
	Parameters *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// telegramError is a rejection from Telegram itself, carrying enough detail
// for the outbox to decide what to do about it. The distinction is the whole
// point: 429 is an instruction to wait, 5xx is worth retrying, and 400 will
// fail identically forever no matter how many times it is sent.
type telegramError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *telegramError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%s: telegram %d: %s (retry after %s)",
			e.Method, e.Code, e.Description, e.RetryAfter)
	}
	return fmt.Sprintf("%s: telegram %d: %s", e.Method, e.Code, e.Description)
}

// permanent reports whether resending the same message could ever succeed.
// 4xx means the request itself is wrong — bad HTML, a chat that blocked the
// bot, a message that no longer exists — and 429 is explicitly not that.
func (e *telegramError) permanent() bool {
	return e.Code >= 400 && e.Code < 500 && e.Code != 429
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
		return err
	}
	defer res.Body.Close()

	var parsed apiResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&parsed); err != nil {
		return fmt.Errorf("%s: decode response: %w", method, err)
	}
	if !parsed.OK {
		te := &telegramError{
			Method:      method,
			Code:        parsed.ErrorCode,
			Description: parsed.Description,
		}
		if te.Code == 0 {
			// Some failures never reach the JSON envelope (a proxy, a gateway
			// error page). The HTTP status is then the only signal there is.
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

// getUpdates long-polls. Telegram holds the request open until something
// arrives or timeout elapses, so this is cheap despite looking like a loop.
func (c *telegramClient) getUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var updates []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeout,
		"allowed_updates": []string{"message", "callback_query"},
	}, &updates)
	return updates, err
}

func (c *telegramClient) sendMessage(ctx context.Context, chatID int64, text string) error {
	return c.sendMessageMarkup(ctx, chatID, text, nil)
}

// sendMessageMarkup sends with an optional inline keyboard. A nil markup is
// omitted from the payload entirely rather than sent as null, which Telegram
// rejects.
func (c *telegramClient) sendMessageMarkup(
	ctx context.Context, chatID int64, text string, markup *inlineKeyboard,
) error {
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

// editMessageText replaces a message in place — how a pressed button updates
// the list it came from instead of sending a second copy of it.
func (c *telegramClient) editMessageText(
	ctx context.Context, chatID, messageID int64, text string, markup *inlineKeyboard,
) error {
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
	return c.call(ctx, "editMessageText", body, nil)
}

// forceReply makes the Telegram client open the keyboard with the message
// quoted, so the answer comes back carrying reply_to_message_id.
type forceReply struct {
	ForceReply            bool   `json:"force_reply"`
	InputFieldPlaceholder string `json:"input_field_placeholder,omitempty"`
}

// sendPrompt asks a question and returns the id of the question, which is what
// the answer will point back at.
func (c *telegramClient) sendPrompt(
	ctx context.Context, chatID int64, text, placeholder string,
) (int64, error) {
	var sent Message
	err := c.call(ctx, "sendMessage", map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"reply_markup": forceReply{
			ForceReply:            true,
			InputFieldPlaceholder: placeholder,
		},
	}, &sent)
	return sent.MessageID, err
}

// getFile resolves a file id to a path on Telegram's file server. The path is
// short-lived, so it is fetched at download time rather than stored.
func (c *telegramClient) getFile(ctx context.Context, fileID string) (string, error) {
	var f struct {
		FilePath string `json:"file_path"`
	}
	if err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return "", err
	}
	if f.FilePath == "" {
		return "", fmt.Errorf("getFile: empty path")
	}
	return f.FilePath, nil
}

// downloadFile fetches a file Telegram is holding for us. The limit is a guard,
// not a policy: a blog post is kilobytes, and anything far larger than that is
// a mistake worth refusing cheaply.
func (c *telegramClient) downloadFile(ctx context.Context, path string, limit int64) ([]byte, error) {
	url := fmt.Sprintf("%s/file/bot%s/%s", c.baseURL, c.token, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: HTTP %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, limit))
}

// answerCallback clears the button's loading spinner. Telegram leaves it
// spinning for a few seconds otherwise, which reads as a broken bot.
func (c *telegramClient) answerCallback(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": id,
		"text":              text,
	}, nil)
}

// botCommand is one entry in Telegram's command menu.
type botCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// setMyCommands fills the menu next to the chat's input box. Worth doing once
// at startup rather than documenting in /help alone: the commands are now
// numerous enough that remembering their names is a tax.
func (c *telegramClient) setMyCommands(ctx context.Context, commands []botCommand) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": commands}, nil)
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

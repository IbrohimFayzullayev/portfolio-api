package tutor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// The few Messages API shapes the tutor needs, by hand: one POST is not
// worth a new dependency (see the ecosystem rule on dependencies).

type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

type Block struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type Request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Tools     []Tool    `json:"tools,omitempty"`
	Messages  []Message `json:"messages"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type Response struct {
	Content    []Block `json:"content"`
	StopReason string  `json:"stop_reason"`
	Usage      Usage   `json:"usage"`
}

// LLM is what the agent loop talks to; tests give it a script.
type LLM interface {
	Send(ctx context.Context, req Request) (Response, error)
}

type Anthropic struct {
	APIKey  string
	BaseURL string // default https://api.anthropic.com
	HTTP    *http.Client
	Backoff time.Duration // before the first retry, growing; default 5s
}

func (a *Anthropic) Send(ctx context.Context, req Request) (Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	base := a.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	client := a.HTTP
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}

	backoff := a.Backoff
	if backoff == 0 {
		backoff = 5 * time.Second
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Response{}, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * backoff):
			}
		}
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return Response{}, err
		}
		hreq.Header.Set("content-type", "application/json")
		hreq.Header.Set("x-api-key", a.APIKey)
		hreq.Header.Set("anthropic-version", "2023-06-01")

		res, err := client.Do(hreq)
		if err != nil {
			lastErr = err
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		res.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		// 429 and 5xx (529 is "overloaded") are worth another try; the rest
		// is a mistake in the request and will not get better.
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
			lastErr = fmt.Errorf("anthropic: %s: %s", res.Status, truncate(string(raw), 300))
			continue
		}
		if res.StatusCode != http.StatusOK {
			return Response{}, fmt.Errorf("anthropic: %s: %s", res.Status, truncate(string(raw), 500))
		}
		var out Response
		if err := json.Unmarshal(raw, &out); err != nil {
			return Response{}, fmt.Errorf("anthropic: decode: %w", err)
		}
		return out, nil
	}
	return Response{}, lastErr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

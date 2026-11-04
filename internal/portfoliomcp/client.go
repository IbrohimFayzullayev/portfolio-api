package portfoliomcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client talks to the portfolio API exactly as the dashboard does: same
// routes, same validation, same side effects (a publish still notifies the
// ops bot). It signs in with the admin's email and password and sends the
// token as a Bearer header - the path scripts and tests already use - so
// there is no second way into the API.
type Client struct {
	BaseURL  string // e.g. http://api:8080 inside the compose network
	Email    string
	Password string
	Token    string // optional: a token from /auth/login, skips the sign-in
	HTTP     *http.Client

	mu sync.Mutex
}

// APIError is the API's own error, with its status.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("API %d: %s", e.Status, e.Message) }

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) token(ctx context.Context, fresh bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Token != "" && !fresh {
		return c.Token, nil
	}
	if c.Email == "" || c.Password == "" {
		if c.Token != "" {
			return c.Token, nil
		}
		return "", errors.New("no credentials: set PORTFOLIO_API_TOKEN or PORTFOLIO_API_EMAIL and PORTFOLIO_API_PASSWORD")
	}
	var out struct {
		Token string `json:"token"`
	}
	body := map[string]string{"email": c.Email, "password": c.Password}
	if err := c.send(ctx, http.MethodPost, "/api/v1/auth/login", nil, body, &out, ""); err != nil {
		return "", fmt.Errorf("sign in: %w", err)
	}
	c.Token = out.Token
	return c.Token, nil
}

// Do calls the API with the admin's token. An expired token is renewed
// once, when there are credentials to renew it with.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	tok, err := c.token(ctx, false)
	if err != nil {
		return err
	}
	err = c.send(ctx, method, path, query, body, out, tok)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized && c.Email != "" {
		if tok, err = c.token(ctx, true); err != nil {
			return err
		}
		return c.send(ctx, method, path, query, body, out, tok)
	}
	return err
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, body, out any, tok string) error {
	u := strings.TrimSuffix(c.BaseURL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &APIError{Status: res.StatusCode, Message: msg}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Umami, read from the bot.
//
// The rule this file exists to honour was set when the bot was designed and has
// not changed: if the numbers cannot be fetched, the section is left out
// entirely. A summary that quietly prints zeros because an endpoint moved is
// worse than one that never mentions traffic — zeros look like a bad week, and
// you act on them.
//
// Umami's response shape has changed across 2.x: `stats` returned plain numbers
// in early versions and {value, prev} objects from 2.12 on. Both are accepted
// below, because the alternative is a bot that breaks on a `docker compose
// pull` that nobody connected to Telegram.

const (
	umamiTimeout  = 6 * time.Second
	umamiTokenTTL = 12 * time.Hour
)

type umamiClient struct {
	base      string
	websiteID string
	username  string
	password  string
	apiKey    string
	http      *http.Client

	mu      sync.Mutex
	token   string
	tokenAt time.Time
}

func newUmamiClient(cfg Config) *umamiClient {
	return &umamiClient{
		base:      trimSlash(cfg.UmamiURL),
		websiteID: cfg.UmamiWebsiteID,
		username:  cfg.UmamiUsername,
		password:  cfg.UmamiPassword,
		apiKey:    cfg.UmamiAPIKey,
		http:      &http.Client{Timeout: umamiTimeout},
	}
}

// enabled reports whether there is anything to ask. An unconfigured bot simply
// has no traffic section; that is a configuration state, not an error.
func (c *umamiClient) enabled() bool {
	if c == nil || c.base == "" || c.websiteID == "" {
		return false
	}
	return c.apiKey != "" || (c.username != "" && c.password != "")
}

/* ------------------------------- responses ------------------------------- */

// countValue accepts both shapes Umami has used for a metric: a bare number,
// and {"value": n, "prev": m}.
type countValue struct {
	Value int64
	Prev  int64
}

func (c *countValue) UnmarshalJSON(raw []byte) error {
	var asNumber float64
	if err := json.Unmarshal(raw, &asNumber); err == nil {
		c.Value = int64(asNumber)
		return nil
	}

	var asObject struct {
		Value float64 `json:"value"`
		Prev  float64 `json:"prev"`
	}
	if err := json.Unmarshal(raw, &asObject); err != nil {
		return err
	}
	c.Value = int64(asObject.Value)
	c.Prev = int64(asObject.Prev)
	return nil
}

type umamiStats struct {
	Pageviews countValue `json:"pageviews"`
	Visitors  countValue `json:"visitors"`
	Visits    countValue `json:"visits"`
	Bounces   countValue `json:"bounces"`
}

type umamiMetric struct {
	Name  string `json:"x"`
	Count int64  `json:"y"`
}

/* --------------------------------- calls --------------------------------- */

func (c *umamiClient) stats(ctx context.Context, from, to time.Time) (umamiStats, error) {
	var out umamiStats
	err := c.get(ctx, fmt.Sprintf("/api/websites/%s/stats", c.websiteID), url.Values{
		"startAt": {millis(from)},
		"endAt":   {millis(to)},
	}, &out)
	return out, err
}

// metrics is "top pages" and "top referrers": kind is Umami's `type` parameter
// ("url", "referrer").
func (c *umamiClient) metrics(
	ctx context.Context, from, to time.Time, kind string, limit int,
) ([]umamiMetric, error) {
	var out []umamiMetric
	err := c.get(ctx, fmt.Sprintf("/api/websites/%s/metrics", c.websiteID), url.Values{
		"startAt": {millis(from)},
		"endAt":   {millis(to)},
		"type":    {kind},
		"limit":   {strconv.Itoa(limit)},
	}, &out)
	return out, err
}

func (c *umamiClient) get(ctx context.Context, path string, query url.Values, out any) error {
	token, err := c.authorise(ctx)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+path+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("x-umami-api-key", c.apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusUnauthorized {
		// The cached token has expired early (a restart of Umami, a changed
		// secret). Drop it so the next call logs in again.
		c.forgetToken()
		return fmt.Errorf("umami: unauthorised")
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("umami: HTTP %d", res.StatusCode)
	}

	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}

// authorise returns a bearer token, logging in at most once every TTL. With an
// API key configured there is nothing to do — the key travels on each request.
func (c *umamiClient) authorise(ctx context.Context) (string, error) {
	if c.apiKey != "" {
		return "", nil
	}

	c.mu.Lock()
	if c.token != "" && time.Since(c.tokenAt) < umamiTokenTTL {
		token := c.token
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	body, err := json.Marshal(map[string]string{
		"username": c.username,
		"password": c.password,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("umami: login HTTP %d", res.StatusCode)
	}

	var parsed struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&parsed); err != nil {
		return "", err
	}
	if parsed.Token == "" {
		return "", fmt.Errorf("umami: login returned no token")
	}

	c.mu.Lock()
	c.token, c.tokenAt = parsed.Token, time.Now()
	c.mu.Unlock()

	return parsed.Token, nil
}

func (c *umamiClient) forgetToken() {
	c.mu.Lock()
	c.token, c.tokenAt = "", time.Time{}
	c.mu.Unlock()
}

/* -------------------------------- helpers -------------------------------- */

func millis(t time.Time) string {
	return strconv.FormatInt(t.UnixMilli(), 10)
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

package mcpauth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	claudeCB = "https://claude.ai/api/mcp/auth_callback"
	verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk-and-some-more"
)

func challenge(v string) string {
	sum := sha256.Sum256([]byte(v))
	return b64.EncodeToString(sum[:])
}

type env struct {
	t    *testing.T
	srv  *httptest.Server
	auth *Server
	now  time.Time
}

func setup(t *testing.T) *env {
	e := &env{t: t, now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
	a, err := New(Config{
		PublicURL: "https://mcp.example.uz",
		Key:       []byte(strings.Repeat("k", 32)),
		CheckLogin: func(_ context.Context, email, pw string) error {
			if email == "me@x.uz" && pw == "right" {
				return nil
			}
			return ErrBadCredentials
		},
		Now: func() time.Time { return e.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	e.auth = a
	mux := http.NewServeMux()
	a.Register(mux)
	mux.Handle("/mcp", a.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "tools here")
	})))
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (e *env) do(req *http.Request) (*http.Response, string) {
	e.t.Helper()
	res, err := noRedirect.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (e *env) get(path string) (*http.Response, string) {
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	return e.do(req)
}

func (e *env) form(path string, v url.Values, ip string) (*http.Response, string) {
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	return e.do(req)
}

func (e *env) register(body string) (int, map[string]any) {
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, b := e.do(req)
	var m map[string]any
	json.Unmarshal([]byte(b), &m)
	return res.StatusCode, m
}

var formRe = regexp.MustCompile(`name="form" value="([^"]+)"`)

// login runs /authorize to the redirect and returns the code.
func (e *env) login(clientID, redirect, ip string) string {
	e.t.Helper()
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "state": {"st8"},
		"resource": {"https://mcp.example.uz/mcp"}}
	res, page := e.get("/authorize?" + q.Encode())
	m := formRe.FindStringSubmatch(page)
	if res.StatusCode != 200 || m == nil {
		e.t.Fatalf("authorize page: %d %s", res.StatusCode, page)
	}
	res, _ = e.form("/authorize", url.Values{"form": {htmlUnescape(m[1])}, "email": {"me@x.uz"}, "password": {"right"}, "action": {"allow"}}, ip)
	loc, _ := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != http.StatusFound || loc.Query().Get("state") != "st8" || loc.Query().Get("code") == "" {
		e.t.Fatalf("login redirect: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	return loc.Query().Get("code")
}

func htmlUnescape(s string) string { return strings.NewReplacer("&#43;", "+", "&amp;", "&").Replace(s) }

func (e *env) exchange(v url.Values) (int, map[string]any) {
	res, b := e.form("/token", v, "")
	var m map[string]any
	json.Unmarshal([]byte(b), &m)
	return res.StatusCode, m
}

func TestDiscovery(t *testing.T) {
	e := setup(t)
	res, _ := e.get("/mcp")
	if res.StatusCode != 401 || !strings.Contains(res.Header.Get("WWW-Authenticate"),
		`resource_metadata="https://mcp.example.uz/.well-known/oauth-protected-resource"`) {
		t.Fatalf("401 must point at the metadata: %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}
	_, prm := e.get("/.well-known/oauth-protected-resource")
	if !strings.Contains(prm, `"resource":"https://mcp.example.uz/mcp"`) || !strings.Contains(prm, `"authorization_servers":["https://mcp.example.uz"]`) {
		t.Errorf("protected resource metadata: %s", prm)
	}
	_, asm := e.get("/.well-known/oauth-authorization-server")
	for _, want := range []string{`"registration_endpoint":"https://mcp.example.uz/register"`, `"code_challenge_methods_supported":["S256"]`} {
		if !strings.Contains(asm, want) {
			t.Errorf("authorization server metadata lacks %s: %s", want, asm)
		}
	}
}

func TestFullFlow(t *testing.T) {
	e := setup(t)
	status, client := e.register(`{"redirect_uris":["` + claudeCB + `"],"client_name":"Claude","token_endpoint_auth_method":"none"}`)
	if status != 201 || client["client_secret"] != nil {
		t.Fatalf("register: %d %v", status, client)
	}
	id := client["client_id"].(string)
	code := e.login(id, claudeCB, "10.0.0.1")

	base := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code},
		"redirect_uri": {claudeCB}, "resource": {"https://mcp.example.uz/mcp"}}
	bad := url.Values{}
	for k, v := range base {
		bad[k] = v
	}
	bad.Set("code_verifier", strings.Repeat("x", 50))
	if s, m := e.exchange(bad); s != 400 || m["error"] != "invalid_grant" {
		t.Errorf("wrong verifier: %d %v", s, m)
	}
	base.Set("code_verifier", verifier)
	s, tok := e.exchange(base)
	if s != 200 || tok["token_type"] != "Bearer" || tok["refresh_token"] == nil {
		t.Fatalf("exchange: %d %v", s, tok)
	}
	if s, m := e.exchange(base); s != 400 || m["error"] != "invalid_grant" {
		t.Errorf("a code works once: %d %v", s, m)
	}

	call := func(access string) int {
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/mcp", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+access)
		res, _ := e.do(req)
		return res.StatusCode
	}
	if c := call(tok["access_token"].(string)); c != 200 {
		t.Fatalf("access token: %d", c)
	}
	if c := call(tok["refresh_token"].(string)); c != 401 {
		t.Errorf("a refresh token is not an access token: %d", c)
	}

	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {tok["refresh_token"].(string)}}
	s, tok2 := e.exchange(refresh)
	if s != 200 || tok2["access_token"] == tok["access_token"] {
		t.Fatalf("refresh: %d %v", s, tok2)
	}
	if s, _ := e.exchange(refresh); s != 400 {
		t.Errorf("a refresh token is rotated, not reused: %d", s)
	}

	e.now = e.now.Add(2 * time.Hour)
	if c := call(tok2["access_token"].(string)); c != 401 {
		t.Errorf("an hour-old access token must expire: %d", c)
	}
	refresh.Set("refresh_token", tok2["refresh_token"].(string))
	if s, _ := e.exchange(refresh); s != 200 {
		t.Errorf("the refresh token lives on: %d", s)
	}
}

func TestRegistrationRules(t *testing.T) {
	e := setup(t)
	if s, m := e.register(`{"redirect_uris":["https://evil.example/cb"]}`); s != 400 || m["error"] != "invalid_redirect_uri" {
		t.Errorf("foreign redirect: %d %v", s, m)
	}
	if s, _ := e.register(`{"redirect_uris":[]}`); s != 400 {
		t.Errorf("no redirect: %d", s)
	}
	// A loopback client (Claude Code) may come back on another port.
	_, c := e.register(`{"redirect_uris":["http://localhost:33418/callback"]}`)
	e.login(c["client_id"].(string), "http://localhost:51234/callback", "10.0.0.2")

	// A confidential client must authenticate at the token endpoint.
	_, conf := e.register(`{"redirect_uris":["` + claudeCB + `"],"token_endpoint_auth_method":"client_secret_post"}`)
	id, secret := conf["client_id"].(string), conf["client_secret"].(string)
	code := e.login(id, claudeCB, "10.0.0.3")
	v := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code}, "code_verifier": {verifier}}
	if s, m := e.exchange(v); s != 401 || m["error"] != "invalid_client" {
		t.Errorf("missing secret: %d %v", s, m)
	}
	v.Set("client_secret", secret)
	if s, _ := e.exchange(v); s != 200 {
		t.Errorf("with secret: %d", s)
	}

	// A client id signed with another key (the key was rotated) is unknown:
	// 401 invalid_client, which makes Claude register again.
	other, _ := New(Config{PublicURL: "https://mcp.example.uz", Key: []byte(strings.Repeat("z", 32)),
		CheckLogin: func(context.Context, string, string) error { return nil }})
	stale := other.sign(claims{Typ: "client", RU: []string{claudeCB}})
	if s, m := e.exchange(url.Values{"grant_type": {"authorization_code"}, "client_id": {stale}}); s != 401 || m["error"] != "invalid_client" {
		t.Errorf("stale client: %d %v", s, m)
	}
	if res, page := e.get("/authorize?client_id=" + url.QueryEscape(stale)); res.StatusCode != 400 || !strings.Contains(page, "qaytadan qo") {
		t.Errorf("stale client at authorize: %d", res.StatusCode)
	}
}

func TestAuthorizeChecks(t *testing.T) {
	e := setup(t)
	_, c := e.register(`{"redirect_uris":["` + claudeCB + `"]}`)
	id := c["client_id"].(string)

	// No PKCE: back to the client with an error.
	res, _ := e.get("/authorize?" + url.Values{"response_type": {"code"}, "client_id": {id}, "state": {"s"}}.Encode())
	if loc := res.Header.Get("Location"); res.StatusCode != 302 || !strings.Contains(loc, "error=invalid_request") || !strings.HasPrefix(loc, claudeCB) {
		t.Errorf("no PKCE: %d %s", res.StatusCode, loc)
	}
	// A redirect the client did not register: never redirected to.
	res, _ = e.get("/authorize?" + url.Values{"response_type": {"code"}, "client_id": {id},
		"redirect_uri": {"https://evil.example/cb"}, "code_challenge": {"x"}, "code_challenge_method": {"S256"}}.Encode())
	if res.StatusCode != 400 || res.Header.Get("Location") != "" {
		t.Errorf("foreign redirect: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	// Another resource: refused.
	res, _ = e.get("/authorize?" + url.Values{"response_type": {"code"}, "client_id": {id}, "code_challenge": {"x"},
		"code_challenge_method": {"S256"}, "resource": {"https://other.example/mcp"}}.Encode())
	if !strings.Contains(res.Header.Get("Location"), "error=invalid_target") {
		t.Errorf("foreign resource: %s", res.Header.Get("Location"))
	}
}

func TestLoginLimits(t *testing.T) {
	e := setup(t)
	_, c := e.register(`{"redirect_uris":["` + claudeCB + `"]}`)
	q := url.Values{"response_type": {"code"}, "client_id": {c["client_id"].(string)},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}}
	_, page := e.get("/authorize?" + q.Encode())
	form := htmlUnescape(formRe.FindStringSubmatch(page)[1])
	try := func(pw, ip string) int {
		res, _ := e.form("/authorize", url.Values{"form": {form}, "email": {"me@x.uz"}, "password": {pw}}, ip)
		return res.StatusCode
	}
	for i := 0; i < maxFailsPerIP; i++ {
		if s := try("wrong", "1.1.1.1"); s != 401 {
			t.Fatalf("attempt %d: %d", i, s)
		}
	}
	if s := try("right", "1.1.1.1"); s != 429 {
		t.Errorf("after %d failures the address waits, even with the right password: %d", maxFailsPerIP, s)
	}
	if s := try("right", "2.2.2.2"); s != 302 {
		t.Errorf("another address is not blocked: %d", s)
	}
	e.now = e.now.Add(failWindow + time.Minute)
	if s := try("right", "1.1.1.1"); s != 302 {
		t.Errorf("the wait ends: %d", s)
	}

	// Deny goes back to Claude with access_denied.
	res, _ := e.form("/authorize", url.Values{"form": {form}, "action": {"deny"}}, "3.3.3.3")
	if !strings.Contains(res.Header.Get("Location"), "error=access_denied") {
		t.Errorf("deny: %s", res.Header.Get("Location"))
	}
	// An old page cannot be submitted.
	e.now = e.now.Add(formTTL)
	if s := try("right", "4.4.4.4"); s != 400 {
		t.Errorf("expired form: %d", s)
	}
}

func TestNewRejectsWeakConfig(t *testing.T) {
	ok := func(context.Context, string, string) error { return nil }
	if _, err := New(Config{PublicURL: "https://x.uz", Key: []byte("short"), CheckLogin: ok}); err == nil {
		t.Error("short key accepted")
	}
	if _, err := New(Config{PublicURL: "http://x.uz", Key: []byte(strings.Repeat("k", 32)), CheckLogin: ok}); err == nil {
		t.Error("plain http accepted")
	}
	if _, err := New(Config{PublicURL: "http://localhost:8091", Key: []byte(strings.Repeat("k", 32)), CheckLogin: ok}); err != nil {
		t.Errorf("localhost: %v", err)
	}
}

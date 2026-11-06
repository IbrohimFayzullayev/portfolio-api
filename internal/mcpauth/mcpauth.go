// Package mcpauth puts OAuth 2.1 in front of the MCP endpoint, for exactly
// one user: the portfolio's admin. It is both halves the MCP authorization
// spec (2025-06-18) asks for, in one small server:
//
//   - the resource server: /mcp wants a Bearer token, and a request without
//     one gets 401 with a pointer to the protected-resource metadata
//     (RFC 9728);
//   - the authorization server: metadata (RFC 8414), dynamic client
//     registration (RFC 7591), the authorization-code flow with PKCE S256,
//     refresh tokens with rotation, and resource indicators (RFC 8707).
//
// Who may sign in is not decided here: the login page asks for the admin's
// email and password and checks them against the portfolio API's own
// /auth/login (Config.CheckLogin). There is no second password to keep.
//
// Nothing is stored. Client ids, authorization codes and tokens are all
// signed with one key (HMAC-SHA256) and carry what they need; the only
// state is in memory - codes and refresh tokens already spent, and failed
// logins - and losing it on a restart costs nothing that matters. Changing
// MCP_SIGNING_KEY signs everyone out and makes Claude register again.
package mcpauth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrBadCredentials is what CheckLogin returns for a wrong email or
// password. Any other error means the check itself failed (the API is
// down) and does not count against the person signing in.
var ErrBadCredentials = errors.New("wrong email or password")

const (
	accessTTL  = time.Hour
	refreshTTL = 30 * 24 * time.Hour
	codeTTL    = 2 * time.Minute
	formTTL    = 30 * time.Minute

	maxFailsPerIP = 5  // per failWindow, then that address waits
	maxFailsAll   = 20 // per hour from everywhere, then the door is shut
	failWindow    = 15 * time.Minute
)

// Claude's callback (support.claude.com, "Building custom connectors via
// remote MCP servers"), and the one it says may replace it.
var claudeCallbacks = []string{
	"https://claude.ai/api/mcp/auth_callback",
	"https://claude.com/api/mcp/auth_callback",
}

type Config struct {
	PublicURL  string // https://mcp.fayzullayev.uz, no trailing slash
	Key        []byte // at least 32 bytes
	CheckLogin func(ctx context.Context, email, password string) error
	// Which redirect URIs a client may register. Default: Claude's
	// callbacks and loopback addresses (Claude Code, the MCP Inspector).
	AllowRedirect func(uri string) bool
	Now           func() time.Time
}

type Server struct {
	cfg Config

	mu       sync.Mutex
	spent    map[string]time.Time   // jti -> when it can be forgotten
	fails    map[string][]time.Time // client address -> failed logins
	allFails []time.Time
}

func New(cfg Config) (*Server, error) {
	if len(cfg.Key) < 32 {
		return nil, errors.New("mcpauth: the signing key must be at least 32 bytes")
	}
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !isLoopbackHost(u.Hostname())) {
		return nil, fmt.Errorf("mcpauth: public URL %q must be https (http only on localhost)", cfg.PublicURL)
	}
	cfg.PublicURL = strings.TrimSuffix(cfg.PublicURL, "/")
	if cfg.CheckLogin == nil {
		return nil, errors.New("mcpauth: CheckLogin is required")
	}
	if cfg.AllowRedirect == nil {
		cfg.AllowRedirect = DefaultAllowRedirect
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Server{cfg: cfg, spent: map[string]time.Time{}, fails: map[string][]time.Time{}}, nil
}

// Resource is the MCP endpoint's URL: the audience of every token.
func (a *Server) Resource() string { return a.cfg.PublicURL + "/mcp" }

func (a *Server) metadataURL() string {
	return a.cfg.PublicURL + "/.well-known/oauth-protected-resource"
}

// DefaultAllowRedirect admits Claude's callbacks and loopback http
// addresses on any port (RFC 8252: a native app picks its port at run time).
func DefaultAllowRedirect(uri string) bool {
	for _, c := range claudeCallbacks {
		if uri == c {
			return true
		}
	}
	u, err := url.Parse(uri)
	return err == nil && u.Scheme == "http" && isLoopbackHost(u.Hostname()) && u.Fragment == ""
}

func isLoopbackHost(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

// sameRedirect: exact, except that a loopback URI may come back on another
// port than the one registered.
func sameRedirect(registered, got string) bool {
	if registered == got {
		return true
	}
	r, err1 := url.Parse(registered)
	g, err2 := url.Parse(got)
	if err1 != nil || err2 != nil || r.Scheme != "http" || !isLoopbackHost(r.Hostname()) {
		return false
	}
	return g.Scheme == r.Scheme && g.Hostname() == r.Hostname() && g.Path == r.Path && g.RawQuery == r.RawQuery
}

/* ------------------------------ signed values ------------------------------ */

// claims is what every signed value carries; Typ keeps one kind from being
// used as another (a code is not an access token, a client id is not a code).
type claims struct {
	Typ  string            `json:"t"`
	Exp  int64             `json:"e,omitempty"`
	JTI  string            `json:"j,omitempty"`
	CID  string            `json:"c,omitempty"` // client: a hash of its id in codes and tokens
	RU   []string          `json:"r,omitempty"` // redirect URIs (client) or the one used (code, form)
	Sec  bool              `json:"s,omitempty"` // client: confidential, has a secret
	Name string            `json:"n,omitempty"`
	CC   string            `json:"cc,omitempty"` // PKCE challenge
	Aud  string            `json:"a,omitempty"`
	Q    map[string]string `json:"q,omitempty"` // form: state, scope, the client id
	Iat  int64             `json:"i,omitempty"`
}

var b64 = base64.RawURLEncoding

func (a *Server) mac(payload string) []byte {
	h := hmac.New(sha256.New, a.cfg.Key)
	h.Write([]byte("mcpauth." + payload))
	return h.Sum(nil)
}

func (a *Server) sign(c claims) string {
	raw, _ := json.Marshal(c)
	p := b64.EncodeToString(raw)
	return p + "." + b64.EncodeToString(a.mac(p))
}

var errBadToken = errors.New("invalid or expired")

func (a *Server) parse(tok, typ string) (claims, error) {
	var c claims
	p, sig, ok := strings.Cut(tok, ".")
	if !ok {
		return c, errBadToken
	}
	got, err := b64.DecodeString(sig)
	if err != nil || !hmac.Equal(got, a.mac(p)) {
		return c, errBadToken
	}
	raw, err := b64.DecodeString(p)
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Typ != typ {
		return c, errBadToken
	}
	if c.Exp != 0 && a.cfg.Now().Unix() >= c.Exp {
		return c, errBadToken
	}
	return c, nil
}

func clientHash(clientID string) string {
	sum := sha256.Sum256([]byte(clientID))
	return b64.EncodeToString(sum[:12])
}

func (a *Server) clientSecret(clientID string) string {
	return b64.EncodeToString(a.mac("secret." + clientID))
}

func randomID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return b64.EncodeToString(b)
}

// spend marks a one-time value used; false when it already was.
func (a *Server) spend(jti string, until time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.cfg.Now()
	for k, t := range a.spent {
		if now.After(t) {
			delete(a.spent, k)
		}
	}
	if _, used := a.spent[jti]; used {
		return false
	}
	a.spent[jti] = until
	return true
}

/* ------------------------------- the routes -------------------------------- */

// Register adds the OAuth endpoints and metadata to mux.
func (a *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/.well-known/oauth-protected-resource", a.protectedResource)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", a.protectedResource)
	mux.HandleFunc("/.well-known/oauth-authorization-server", a.authServerMetadata)
	mux.HandleFunc("/register", a.register)
	mux.HandleFunc("/authorize", a.authorize)
	mux.HandleFunc("/token", a.token)
}

// Protect lets through requests with a valid access token for this resource.
func (a *Server) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		tok, found := strings.CutPrefix(auth, "Bearer ")
		challenge := `Bearer resource_metadata="` + a.metadataURL() + `"`
		if !found || tok == "" {
			w.Header().Set("WWW-Authenticate", challenge)
			writeJSON(w, http.StatusUnauthorized, oauthError{"invalid_token", "a Bearer token is required"})
			return
		}
		c, err := a.parse(strings.TrimSpace(tok), "access")
		if err != nil || c.Aud != a.Resource() {
			w.Header().Set("WWW-Authenticate", challenge+`, error="invalid_token"`)
			writeJSON(w, http.StatusUnauthorized, oauthError{"invalid_token", "the token is invalid or expired"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

type oauthError struct {
	Error       string `json:"error"`
	Description string `json:"error_description,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (a *Server) protectedResource(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 a.Resource(),
		"authorization_servers":    []string{a.cfg.PublicURL},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"mcp"},
		"resource_name":            "Portfolio",
	})
}

func (a *Server) authServerMetadata(w http.ResponseWriter, r *http.Request) {
	u := a.cfg.PublicURL
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                u,
		"authorization_endpoint":                u + "/authorize",
		"token_endpoint":                        u + "/token",
		"registration_endpoint":                 u + "/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                      []string{"mcp"},
	})
}

/* --------------------------- client registration --------------------------- */

func (a *Server) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, oauthError{"invalid_request", "POST only"})
		return
	}
	var in struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, oauthError{"invalid_client_metadata", "the body must be JSON client metadata"})
		return
	}
	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 5 {
		writeJSON(w, http.StatusBadRequest, oauthError{"invalid_redirect_uri", "one to five redirect_uris are required"})
		return
	}
	for _, u := range in.RedirectURIs {
		if !a.cfg.AllowRedirect(u) {
			writeJSON(w, http.StatusBadRequest, oauthError{"invalid_redirect_uri", "redirect_uri not allowed: " + u})
			return
		}
	}
	if len(in.ClientName) > 100 {
		in.ClientName = in.ClientName[:100]
	}
	secret := in.AuthMethod == "client_secret_post" || in.AuthMethod == "client_secret_basic"
	method := "none"
	if secret {
		method = in.AuthMethod
	}
	now := a.cfg.Now().Unix()
	id := a.sign(claims{Typ: "client", RU: in.RedirectURIs, Sec: secret, Name: in.ClientName, Iat: now})
	out := map[string]any{
		"client_id":                  id,
		"client_id_issued_at":        now,
		"client_name":                in.ClientName,
		"redirect_uris":              in.RedirectURIs,
		"token_endpoint_auth_method": method,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	}
	if secret {
		out["client_secret"] = a.clientSecret(id)
		out["client_secret_expires_at"] = 0
	}
	writeJSON(w, http.StatusCreated, out)
}

/* ------------------------------ authorization ------------------------------ */

// authRequest is a checked /authorize request.
type authRequest struct {
	clientID, redirect, challenge, state, scope, resource string
	clientName                                            string
}

func (a *Server) authorize(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		req, ok := a.checkAuthorize(w, r.URL.Query())
		if !ok {
			return
		}
		form := a.sign(claims{
			Typ: "form", Exp: a.cfg.Now().Add(formTTL).Unix(), RU: []string{req.redirect}, CC: req.challenge,
			Aud: req.resource, Q: map[string]string{"client_id": req.clientID, "state": req.state, "scope": req.scope},
		})
		renderLogin(w, http.StatusOK, loginPage{Client: req.clientName, Form: form})
	case http.MethodPost:
		a.authorizeSubmit(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// checkAuthorize validates the request. Until the client and its redirect
// are known good, errors are shown here; after that they go back to the
// client, as RFC 6749 4.1.2.1 says.
func (a *Server) checkAuthorize(w http.ResponseWriter, q url.Values) (authRequest, bool) {
	req := authRequest{
		clientID: q.Get("client_id"), redirect: q.Get("redirect_uri"), challenge: q.Get("code_challenge"),
		state: q.Get("state"), scope: q.Get("scope"), resource: q.Get("resource"),
	}
	client, err := a.parse(req.clientID, "client")
	if err != nil {
		renderError(w, http.StatusBadRequest, "Bu ilova tanilmadi. Connector'ni Claude sozlamalaridan o'chirib, qaytadan qo'shing.")
		return req, false
	}
	if req.redirect == "" && len(client.RU) == 1 {
		req.redirect = client.RU[0]
	}
	registered := false
	for _, u := range client.RU {
		if sameRedirect(u, req.redirect) {
			registered = true
		}
	}
	if !registered {
		renderError(w, http.StatusBadRequest, "Qaytish manzili bu ilova uchun ro'yxatdan o'tmagan.")
		return req, false
	}
	req.clientName = client.Name

	fail := func(code, desc string) (authRequest, bool) {
		redirectWith(w, req.redirect, url.Values{"error": {code}, "error_description": {desc}, "state": {req.state}})
		return req, false
	}
	if q.Get("response_type") != "code" {
		return fail("unsupported_response_type", "only response_type=code")
	}
	if req.challenge == "" || q.Get("code_challenge_method") != "S256" {
		return fail("invalid_request", "PKCE with code_challenge_method=S256 is required")
	}
	req.resource = strings.TrimSuffix(req.resource, "/")
	if req.resource == "" || req.resource == a.cfg.PublicURL {
		req.resource = a.Resource()
	}
	if req.resource != a.Resource() {
		return fail("invalid_target", "this server only issues tokens for "+a.Resource())
	}
	return req, true
}

func redirectWith(w http.ResponseWriter, target string, params url.Values) {
	u, err := url.Parse(target)
	if err != nil {
		renderError(w, http.StatusBadRequest, "Qaytish manzili noto'g'ri.")
		return
	}
	q := u.Query()
	for k, vs := range params {
		if len(vs) > 0 && vs[0] != "" {
			q.Set(k, vs[0])
		}
	}
	u.RawQuery = q.Encode()
	w.Header().Set("Location", u.String())
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusFound)
}

func (a *Server) authorizeSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		renderError(w, http.StatusBadRequest, "So'rovni o'qib bo'lmadi.")
		return
	}
	formTok := r.PostForm.Get("form")
	f, err := a.parse(formTok, "form")
	if err != nil || len(f.RU) != 1 {
		renderError(w, http.StatusBadRequest, "Sahifa eskirib qoldi. Claude'da \"Connect\" tugmasini qayta bosing.")
		return
	}
	page := loginPage{Form: formTok, Email: r.PostForm.Get("email")}
	if c, err := a.parse(f.Q["client_id"], "client"); err == nil {
		page.Client = c.Name
	}

	if r.PostForm.Get("action") == "deny" {
		redirectWith(w, f.RU[0], url.Values{"error": {"access_denied"}, "state": {f.Q["state"]}})
		return
	}
	ip := clientIP(r)
	if msg := a.blocked(ip); msg != "" {
		page.Error = msg
		renderLogin(w, http.StatusTooManyRequests, page)
		return
	}
	err = a.cfg.CheckLogin(r.Context(), strings.TrimSpace(r.PostForm.Get("email")), r.PostForm.Get("password"))
	if errors.Is(err, ErrBadCredentials) {
		a.failed(ip)
		page.Error = "Email yoki parol noto'g'ri."
		renderLogin(w, http.StatusUnauthorized, page)
		return
	}
	if err != nil {
		page.Error = "Hozir tekshirib bo'lmadi (API javob bermadi). Birozdan keyin urinib ko'ring."
		renderLogin(w, http.StatusBadGateway, page)
		return
	}
	a.succeeded(ip)

	code := a.sign(claims{
		Typ: "code", Exp: a.cfg.Now().Add(codeTTL).Unix(), JTI: randomID(),
		CID: clientHash(f.Q["client_id"]), RU: f.RU, CC: f.CC, Aud: f.Aud,
	})
	redirectWith(w, f.RU[0], url.Values{"code": {code}, "state": {f.Q["state"]}})
}

/* ------------------------------ failed logins ------------------------------ */

func clientIP(r *http.Request) string {
	// Behind Caddy: the last X-Forwarded-For entry is the address Caddy saw.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func recent(ts []time.Time, since time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if t.After(since) {
			out = append(out, t)
		}
	}
	return out
}

func (a *Server) blocked(ip string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.cfg.Now()
	a.allFails = recent(a.allFails, now.Add(-time.Hour))
	if len(a.allFails) >= maxFailsAll {
		return "Juda ko'p noto'g'ri urinish bo'ldi, kirish bir soatga yopildi."
	}
	a.fails[ip] = recent(a.fails[ip], now.Add(-failWindow))
	if len(a.fails[ip]) >= maxFailsPerIP {
		return "Juda ko'p noto'g'ri urinish. 15 daqiqadan keyin qayta urinib ko'ring."
	}
	return ""
}

func (a *Server) failed(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.cfg.Now()
	a.fails[ip] = append(a.fails[ip], now)
	a.allFails = append(a.allFails, now)
}

func (a *Server) succeeded(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.fails, ip)
}

/* ---------------------------------- tokens --------------------------------- */

func (a *Server) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, oauthError{"invalid_request", "POST only"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, oauthError{"invalid_request", "form body expected"})
		return
	}
	f := r.PostForm
	clientID, secret := f.Get("client_id"), f.Get("client_secret")
	if id, sec, ok := r.BasicAuth(); ok {
		clientID, secret = unescape(id), unescape(sec)
	}
	client, err := a.parse(clientID, "client")
	if err != nil {
		// 401 invalid_client tells Claude to register again (its docs).
		writeJSON(w, http.StatusUnauthorized, oauthError{"invalid_client", "unknown client; register again"})
		return
	}
	if client.Sec && subtle.ConstantTimeCompare([]byte(secret), []byte(a.clientSecret(clientID))) != 1 {
		writeJSON(w, http.StatusUnauthorized, oauthError{"invalid_client", "client authentication failed"})
		return
	}
	cid := clientHash(clientID)
	now := a.cfg.Now()

	var aud string
	switch f.Get("grant_type") {
	case "authorization_code":
		c, err := a.parse(f.Get("code"), "code")
		switch {
		case err != nil:
			writeJSON(w, http.StatusBadRequest, oauthError{"invalid_grant", "the code is invalid or expired"})
			return
		case c.CID != cid:
			writeJSON(w, http.StatusBadRequest, oauthError{"invalid_grant", "the code was issued to another client"})
			return
		case f.Get("redirect_uri") != "" && !sameRedirect(c.RU[0], f.Get("redirect_uri")):
			writeJSON(w, http.StatusBadRequest, oauthError{"invalid_grant", "redirect_uri does not match"})
			return
		case !pkceOK(f.Get("code_verifier"), c.CC):
			writeJSON(w, http.StatusBadRequest, oauthError{"invalid_grant", "code_verifier does not match"})
			return
		case f.Get("resource") != "" && strings.TrimSuffix(f.Get("resource"), "/") != c.Aud &&
			strings.TrimSuffix(f.Get("resource"), "/") != a.cfg.PublicURL:
			writeJSON(w, http.StatusBadRequest, oauthError{"invalid_target", "resource does not match"})
			return
		case !a.spend(c.JTI, time.Unix(c.Exp, 0)):
			writeJSON(w, http.StatusBadRequest, oauthError{"invalid_grant", "the code was already used"})
			return
		}
		aud = c.Aud
	case "refresh_token":
		c, err := a.parse(f.Get("refresh_token"), "refresh")
		if err != nil || c.CID != cid || !a.spend(c.JTI, time.Unix(c.Exp, 0)) {
			writeJSON(w, http.StatusBadRequest, oauthError{"invalid_grant", "the refresh token is invalid, expired or already used"})
			return
		}
		aud = c.Aud
	default:
		writeJSON(w, http.StatusBadRequest, oauthError{"unsupported_grant_type", "authorization_code or refresh_token"})
		return
	}

	access := a.sign(claims{Typ: "access", Exp: now.Add(accessTTL).Unix(), JTI: randomID(), CID: cid, Aud: aud})
	refresh := a.sign(claims{Typ: "refresh", Exp: now.Add(refreshTTL).Unix(), JTI: randomID(), CID: cid, Aud: aud})
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"refresh_token": refresh,
		"scope":         "mcp",
	})
}

// pkceOK: BASE64URL(SHA256(verifier)) == challenge, verifier per RFC 7636.
func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(b64.EncodeToString(sum[:])), []byte(challenge)) == 1
}

// unescape: Basic credentials are form-encoded first (RFC 6749 2.3.1).
func unescape(s string) string {
	if v, err := url.QueryUnescape(s); err == nil {
		return v
	}
	return s
}

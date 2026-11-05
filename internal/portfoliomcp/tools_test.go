package portfoliomcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

const (
	idUz   = "11111111-1111-1111-1111-111111111111"
	idEn   = "22222222-2222-2222-2222-222222222222"
	idDemo = "33333333-3333-3333-3333-333333333333"
	idProj = "44444444-4444-4444-4444-444444444444"
	idMsg  = "55555555-5555-5555-5555-555555555555"
)

// fakeAPI answers like internal/api for the routes the tools use. It accepts
// one token at a time and expires the first after one use, to exercise the
// sign-in renewal.
type fakeAPI struct {
	t       *testing.T
	posts   map[string]*post
	project project
	token   string
	logins  int
	calls   []string
	puts    []map[string]any
	read    []string
	audit   []map[string]any // rows POSTed to /agent-changes
	noAudit bool             // make the log refuse writes
}

func newFake(t *testing.T) *fakeAPI {
	long := strings.Repeat("so'z ", 400)
	return &fakeAPI{
		t: t,
		posts: map[string]*post{
			idUz: {ID: idUz, Locale: "uz", Slug: "vps-deploy", Title: "VPS'ga deploy", Description: "Vercel'siz",
				Body: long, Tags: []string{"devops"}, Draft: true, Date: "2026-10-01", TranslationKey: "vps-deploy",
				UpdatedAt: now.AddDate(0, 0, -10)},
			idEn: {ID: idEn, Locale: "en", Slug: "vps-deploy", Title: "Deploying to a VPS", Description: "No Vercel",
				Body: long, Tags: []string{"devops"}, Draft: false, Date: "2026-10-01", TranslationKey: "vps-deploy",
				UpdatedAt: now},
			idDemo: {ID: idDemo, Locale: "en", Slug: "hello-world", Title: "Hello world", Body: "demo text",
				Tags: []string{}, Draft: false, Date: "2026-01-01", UpdatedAt: now},
		},
		project: project{ID: idProj, Locale: "uz", Slug: "radius-crm", Title: "Radius CRM",
			Description: "CRM", Body: "25+ ekranli CRM. Natija: 100+ xodim.", Tags: []string{}, Stack: []string{"React"},
			URL: "https://radius.uz", UpdatedAt: now},
	}
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	write := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	if r.URL.Path == "/api/v1/auth/login" {
		var in struct{ Email, Password string }
		json.NewDecoder(r.Body).Decode(&in)
		if in.Email != "me@x.uz" || in.Password != "pw" {
			write(401, map[string]string{"error": "invalid email or password"})
			return
		}
		f.logins++
		f.token = "tok" + string(rune('0'+f.logins))
		write(200, map[string]any{"token": f.token})
		return
	}
	if f.logins == 1 && len(f.calls) == 3 {
		f.token = "expired" // the second call after sign-in finds the token expired
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		write(401, map[string]string{"error": "invalid or expired token"})
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	switch {
	case r.Method == "GET" && path == "/posts":
		out := []post{}
		for _, id := range []string{idUz, idEn, idDemo} {
			p := *f.posts[id]
			if loc := r.URL.Query().Get("locale"); loc != "" && p.Locale != loc {
				continue
			}
			out = append(out, p)
		}
		write(200, out)
	case r.Method == "GET" && strings.HasPrefix(path, "/posts/"):
		p, ok := f.posts[strings.TrimPrefix(path, "/posts/")]
		if !ok {
			write(404, map[string]string{"error": "post not found"})
			return
		}
		write(200, p)
	case r.Method == "PUT" && strings.HasPrefix(path, "/posts/"):
		// The API decodes with DisallowUnknownFields; so do we.
		var in postInput
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			write(400, map[string]string{"error": err.Error()})
			return
		}
		if in.Slug == "taken" {
			write(409, map[string]string{"error": "a post with this locale and slug already exists"})
			return
		}
		raw, _ := json.Marshal(in)
		var m map[string]any
		json.Unmarshal(raw, &m)
		f.puts = append(f.puts, m)
		p := f.posts[strings.TrimPrefix(path, "/posts/")]
		p.Title, p.Body, p.Description, p.Draft, p.Tags = in.Title, in.Body, in.Description, in.Draft, in.Tags
		p.TranslationKey = in.TranslationKey
		write(200, p)
	case r.Method == "PATCH" && strings.HasSuffix(path, "/publish"):
		var in struct{ Draft bool }
		json.NewDecoder(r.Body).Decode(&in)
		p := f.posts[strings.TrimSuffix(strings.TrimPrefix(path, "/posts/"), "/publish")]
		p.Draft = in.Draft
		write(200, p)
	case r.Method == "POST" && path == "/posts":
		var in postInput
		json.NewDecoder(r.Body).Decode(&in)
		write(201, post{ID: "66666666-6666-6666-6666-666666666666", Locale: in.Locale, Slug: in.Slug,
			Title: in.Title, Body: in.Body, Draft: in.Draft, Date: in.Date, TranslationKey: in.TranslationKey})
	case r.Method == "GET" && path == "/projects":
		write(200, []project{f.project})
	case r.Method == "GET" && path == "/messages":
		write(200, map[string]any{"total": 2, "unread": 1, "items": []map[string]any{
			{"id": "77777777-7777-7777-7777-777777777777", "name": "A", "email": "a@x.uz", "message": "eski", "read_at": now, "created_at": now},
			{"id": idMsg, "name": "B", "email": "b@x.uz", "message": strings.Repeat("x", 900), "user_agent": "Mozilla", "created_at": now},
		}})
	case r.Method == "PATCH" && strings.HasSuffix(path, "/read"):
		f.read = append(f.read, path)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "GET" && path == "/finance/summary":
		if r.URL.Query().Get("from") != "2026-11-01" {
			f.t.Errorf("finance from = %q", r.URL.Query().Get("from"))
		}
		write(200, map[string]any{"spent": map[string]int{"UZS": 100}, "balance_series": []int{1, 2, 3},
			"by_day": []int{1}, "largest": []map[string]any{{"note": "a", "raw_text": "secret"}},
			"by_month": []map[string]any{
				{"month": "2026-10", "amounts": map[string]any{}, "income": map[string]any{}},
				{"month": "2026-11", "amounts": map[string]any{"UZS": 100}, "income": map[string]any{}},
			}})
	case r.Method == "POST" && path == "/agent-changes":
		if f.noAudit {
			write(500, map[string]string{"error": "could not record the change"})
			return
		}
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		f.audit = append(f.audit, m)
		write(201, map[string]any{"id": len(f.audit)})
	case r.Method == "GET" && path == "/agent-changes":
		items := []map[string]any{}
		for i := len(f.audit) - 1; i >= 0; i-- {
			items = append(items, f.audit[i])
		}
		write(200, map[string]any{"items": items})
	default:
		write(404, map[string]string{"error": "not found: " + r.Method + " " + path})
	}
}

// call runs one tool through the whole MCP server, as a client would.
func call(t *testing.T, srvURL string, calls ...string) []map[string]any {
	t.Helper()
	c := &Client{BaseURL: srvURL, Email: "me@x.uz", Password: "pw"}
	s := New(c, func() time.Time { return now })
	var in strings.Builder
	for i, cl := range calls {
		name, args, _ := strings.Cut(cl, " ")
		if args == "" {
			args = "{}"
		}
		in.WriteString(`{"jsonrpc":"2.0","id":`);in.WriteString(string(rune('1' + i)));in.WriteString(`,"method":"tools/call","params":{"name":"`);in.WriteString(name);in.WriteString(`","arguments":`);in.WriteString(args);in.WriteString("}}\n")
	}
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(in.String()), &out); err != nil {
		t.Fatal(err)
	}
	var res []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m struct {
			Result struct {
				Content []struct{ Text string }
				IsError bool
			}
		}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		text := m.Result.Content[0].Text
		if m.Result.IsError {
			res = append(res, map[string]any{"error": text})
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(text), &v); err != nil {
			t.Fatalf("result is not JSON: %q", text)
		}
		res = append(res, v)
	}
	return res
}

func TestListPostsIsSmallAndRenewsToken(t *testing.T) {
	f := newFake(t)
	srv := httptest.NewServer(f)
	defer srv.Close()

	res := call(t, srv.URL, `list_posts`, `list_posts {"locale":"uz"}`)
	if res[0]["count"].(float64) != 3 || res[1]["count"].(float64) != 1 {
		t.Fatalf("counts: %v / %v", res[0]["count"], res[1]["count"])
	}
	first := res[0]["posts"].([]any)[0].(map[string]any)
	if _, hasBody := first["body"]; hasBody || first["words"].(float64) != 400 {
		t.Errorf("a list carries word counts, not bodies: %v", first)
	}
	if f.logins != 2 {
		t.Errorf("the expired token should be renewed once, logins = %d", f.logins)
	}
}

func TestUpdatePostKeepsEverythingElse(t *testing.T) {
	f := newFake(t)
	srv := httptest.NewServer(f)
	defer srv.Close()

	res := call(t, srv.URL,
		`update_post {"id":"`+idUz+`","title":"Vercel'siz VPS'ga deploy \u2014 amalda"}`,
		`update_post {"id":"`+idUz+`","title":"Vercel'siz VPS'ga deploy - amalda"}`,
		`update_post {"id":"not-a-uuid","title":"x"}`,
	)
	if ch := res[0]["changed"].([]any); len(ch) != 1 || ch[0] != "title" {
		t.Errorf("changed: %v", ch)
	}
	if len(f.puts) != 1 {
		t.Fatalf("an unchanged second call must not PUT; puts = %d", len(f.puts))
	}
	if res[0]["dashes_replaced"] != float64(1) || f.posts[idUz].Title != "Vercel'siz VPS'ga deploy - amalda" {
		t.Errorf("an em dash must reach the API as a plain hyphen: %v / %q", res[0]["dashes_replaced"], f.posts[idUz].Title)
	}
	put := f.puts[0]
	if put["draft"] != true || put["translation_key"] != "vps-deploy" || put["date"] != "2026-10-01" {
		t.Errorf("fields not given must be sent back as they were: %v", put)
	}
	if res[2]["error"] == nil {
		t.Errorf("a bad id is a tool error: %v", res[2])
	}
}

func TestCreateIsAlwaysDraftAndPublishIsExplicit(t *testing.T) {
	f := newFake(t)
	srv := httptest.NewServer(f)
	defer srv.Close()

	res := call(t, srv.URL,
		`create_post_draft {"locale":"uz","slug":"yangi","title":"Yangi","body":"matn","draft":false}`,
		`set_post_published {"id":"`+idUz+`"}`,
		`set_post_published {"id":"`+idUz+`","published":true}`,
	)
	created := res[0]["created"].(map[string]any)
	if created["draft"] != true || created["date"] != "2026-11-04" {
		t.Errorf("created: %v", created)
	}
	if res[1]["error"] == nil {
		t.Errorf("publish without 'published' must fail: %v", res[1])
	}
	if f.posts[idUz].Draft {
		t.Error("post should be published")
	}
}

func TestContentAudit(t *testing.T) {
	f := newFake(t)
	srv := httptest.NewServer(f)
	defer srv.Close()

	res := call(t, srv.URL, `content_audit`)
	issues := map[string][]string{}
	for _, x := range res[0]["findings"].([]any) {
		m := x.(map[string]any)
		for _, i := range m["issues"].([]any) {
			issues[m["slug"].(string)+"/"+m["locale"].(string)] = append(issues[m["slug"].(string)+"/"+m["locale"].(string)], i.(string))
		}
	}
	has := func(key, issue string) bool {
		for _, i := range issues[key] {
			if strings.HasPrefix(i, issue) {
				return true
			}
		}
		return false
	}
	if !has("vps-deploy/uz", "draft_10_days") || has("vps-deploy/uz", "no_translation_pair") {
		t.Errorf("uz post: %v", issues["vps-deploy/uz"])
	}
	if _, ok := issues["vps-deploy/en"]; ok {
		t.Errorf("a complete published post has no findings: %v", issues["vps-deploy/en"])
	}
	for _, want := range []string{"no_translation_pair", "empty_description", "thin_body_2", "looks_like_demo"} {
		if !has("hello-world/en", want) {
			t.Errorf("demo post lacks %s: %v", want, issues["hello-world/en"])
		}
	}
	for _, want := range []string{"no_repo_link", "not_problem_decision_result"} {
		if !has("radius-crm/uz", want) {
			t.Errorf("project lacks %s: %v", want, issues["radius-crm/uz"])
		}
	}
	if has("radius-crm/uz", "no_live_url") {
		t.Error("the project has a url")
	}
}

func TestInboxAndFinanceAreTrimmed(t *testing.T) {
	f := newFake(t)
	srv := httptest.NewServer(f)
	defer srv.Close()

	res := call(t, srv.URL,
		`list_messages {"unread_only":true}`,
		`mark_message_read {"id":"`+idMsg+`"}`,
		`finance_summary {"from":"2026-11-01"}`,
		`finance_summary {"from":"1 noyabr"}`,
	)
	items := res[0]["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("unread only: %v", items)
	}
	m := items[0].(map[string]any)
	if _, ok := m["user_agent"]; ok || len([]rune(m["message"].(string))) > 601 {
		t.Errorf("message not trimmed: %v", m)
	}
	if res[1]["read"] != idMsg || len(f.read) != 1 {
		t.Errorf("mark read: %v %v", res[1], f.read)
	}
	fin := res[2]
	if _, ok := fin["balance_series"]; ok {
		t.Error("series should be dropped")
	}
	if l := fin["largest"].([]any)[0].(map[string]any); l["raw_text"] != nil {
		t.Errorf("raw text should be dropped: %v", l)
	}
	if months := fin["by_month"].([]any); len(months) != 1 {
		t.Errorf("empty months should be dropped: %v", months)
	}
	if res[3]["error"] == nil {
		t.Error("a bad date is a tool error")
	}
}

func TestPrompts(t *testing.T) {
	s := New(&Client{}, func() time.Time { return now })
	var out bytes.Buffer
	in := `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"haftalik_xulosa"}}
{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"loyihani_qayta_yoz","arguments":{"slug":"../etc"}}}
`
	s.Serve(context.Background(), strings.NewReader(in), &out)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if !strings.Contains(lines[0], "2026-10-29") || !strings.Contains(lines[0], "growth_summary") {
		t.Errorf("weekly prompt: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"error"`) {
		t.Errorf("a bad slug is refused: %s", lines[1])
	}
}

func TestLinkTranslations(t *testing.T) {
	f := newFake(t)
	srv := httptest.NewServer(f)
	defer srv.Close()

	res := call(t, srv.URL,
		// The en post already holds "vps-deploy", so the uz post cannot keep it.
		`link_translations {"type":"post","uz_id":"`+idUz+`","en_id":"`+idDemo+`","key":"vps-deploy"}`,
		`link_translations {"type":"post","uz_id":"`+idEn+`","en_id":"`+idDemo+`"}`,
		`link_translations {"type":"post","uz_id":"`+idUz+`","en_id":"`+idDemo+`","key":"Bad Key"}`,
		`link_translations {"type":"post","uz_id":"`+idUz+`","en_id":"`+idDemo+`"}`,
	)
	if e, _ := res[0]["error"].(string); !strings.Contains(e, "already used") || !strings.Contains(e, "vps-deploy") {
		t.Errorf("a clash must be refused before the API sees it: %v", res[0])
	}
	if e, _ := res[1]["error"].(string); !strings.Contains(e, "not uz") {
		t.Errorf("a wrong locale must be refused: %v", res[1])
	}
	if res[2]["error"] == nil {
		t.Errorf("a bad key must be refused: %v", res[2])
	}
	// Default key: neither side's key is free, so the English slug.
	if res[3]["key"] != "hello-world" || len(res[3]["changed"].([]any)) != 2 {
		t.Fatalf("link: %v", res[3])
	}
	if f.posts[idUz].TranslationKey != "hello-world" || f.posts[idDemo].TranslationKey != "hello-world" {
		t.Errorf("both rows must carry the key: %q %q", f.posts[idUz].TranslationKey, f.posts[idDemo].TranslationKey)
	}
	if len(f.puts) != 2 || f.puts[0]["draft"] != true || f.puts[0]["title"] != "VPS'ga deploy" {
		t.Errorf("only the key may change: %v", f.puts)
	}
}

// The shapes of the real data on 2026-11-04: three draft pairs with
// different slugs written on the same day each, a project with one slug in
// both languages, and a linked pair that must be left alone.
func TestSuggestPairs(t *testing.T) {
	it := func(typ, id, loc, slug, title, date, key string, tags ...string) item {
		return item{typ, id, loc, slug, title, date, key, tags}
	}
	items := []item{
		it("post", "u1", "uz", "nextjs-ssg-isr-amaliyot", "Amaliyotda SSG va ISR (App Router)", "2026-06-08", "", "nextjs", "tezkorlik", "rendering"),
		it("post", "e1", "en", "nextjs-ssg-isr-in-practice", "SSG and ISR in practice with the App Router", "2026-06-08", "", "nextjs", "performance", "rendering"),
		it("post", "u2", "uz", "dizayn-tizimini-qurish", "Kengayadigan dizayn tizimini qurish", "2026-05-28", "", "dizayn-tizimlari", "css", "react"),
		it("post", "e2", "en", "building-a-design-system", "Building a design system that scales", "2026-05-28", "", "design-systems", "css", "react"),
		it("post", "u3", "uz", "typescript-bilan-tez-yetkazish", "TypeScript'ni buzmasdan tez yetkazib berish", "2026-04-19", "", "typescript", "dx"),
		it("post", "e3", "en", "shipping-fast-with-typescript", "Shipping fast without breaking TypeScript", "2026-04-19", "", "typescript", "dx"),
		it("post", "u4", "uz", "uch-servis-bitta-vps", "Uch servis", "2026-10-08", "vps-deploy", "deploy"),
		it("post", "e4", "en", "three-services-one-vps", "Three services", "2026-10-08", "vps-deploy", "deploy"),
		// Same day, nothing else in common: not a pair.
		it("post", "u5", "uz", "vercelsiz-deploy", "Vercelsiz deploy", "2026-11-04", ""),
		it("post", "e5", "en", "getting-started", "Getting started", "2026-11-04", ""),
		it("project", "pu", "uz", "zukko-ai", "Zukko AI", "2026-01-22", "", "ai"),
		it("project", "pe", "en", "zukko-ai", "Zukko AI", "2026-01-22", "", "ai"),
	}
	got := map[string]string{}
	for _, s := range SuggestPairs(items) {
		got[s.UzID] = s.EnID + " " + s.Key
	}
	want := map[string]string{
		"u1": "e1 nextjs-ssg-isr-in-practice",
		"u2": "e2 building-a-design-system",
		"u3": "e3 shipping-fast-with-typescript",
		"pu": "pe zukko-ai",
	}
	if len(got) != len(want) {
		t.Fatalf("suggestions: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
}

func TestPlaceholderLinks(t *testing.T) {
	for link, want := range map[string]bool{
		"https://example.com": true, "http://localhost:3000/x": true, "#": true,
		"https://ncrm.radius.uz": false, "https://example.community": false, "": false,
	} {
		if got := placeholderRe.MatchString(link); got != want {
			t.Errorf("%q: got %v", link, got)
		}
	}
}

// Every write leaves a row in the agent log with the values before and
// after; failed writes too; a no-op leaves none.
func TestWritesAreAudited(t *testing.T) {
	f := newFake(t)
	srv := httptest.NewServer(f)
	defer srv.Close()

	res := call(t, srv.URL,
		`update_post {"id":"`+idUz+`","title":"Yangi sarlavha"}`,
		`set_post_published {"id":"`+idUz+`","published":true}`,
		`set_post_published {"id":"`+idUz+`","published":true}`,
		`update_post {"id":"`+idUz+`","slug":"taken"}`,
		`mark_message_read {"id":"`+idMsg+`"}`,
		`link_translations {"type":"post","uz_id":"`+idUz+`","en_id":"`+idDemo+`"}`,
	)
	if res[2]["unchanged"] != true {
		t.Errorf("publishing a published post is a no-op: %v", res[2])
	}
	if res[3]["error"] == nil {
		t.Errorf("the 409 must reach the model: %v", res[3])
	}
	tools := []string{}
	for _, a := range f.audit {
		tools = append(tools, a["tool"].(string))
		if a["actor"] != "mcp" {
			t.Errorf("actor: %v", a["actor"])
		}
	}
	want := "update_post set_post_published update_post mark_message_read link_translations"
	if strings.Join(tools, " ") != want {
		t.Fatalf("logged %v, want %s", tools, want)
	}

	title := f.audit[0]["changes"].(map[string]any)["title"].(map[string]any)
	if title["from"] != "VPS'ga deploy" || title["to"] != "Yangi sarlavha" || f.audit[0]["ok"] != true {
		t.Errorf("update diff: %v", f.audit[0])
	}
	if tg := f.audit[0]["targets"].([]any)[0].(map[string]any); tg["label"] != "uz/vps-deploy" || tg["id"] != idUz {
		t.Errorf("target: %v", tg)
	}
	draft := f.audit[1]["changes"].(map[string]any)["draft"].(map[string]any)
	if draft["from"] != true || draft["to"] != false {
		t.Errorf("publish diff: %v", draft)
	}
	if f.audit[2]["ok"] != false || !strings.Contains(f.audit[2]["error"].(string), "already exists") {
		t.Errorf("a failed write is logged as one: %v", f.audit[2])
	}
	if f.audit[3]["target_type"] != "message" {
		t.Errorf("read mark: %v", f.audit[3])
	}
	key := f.audit[4]["changes"].(map[string]any)["translation_key"].(map[string]any)
	if key["to"] != "hello-world" || key["from"].(map[string]any)["uz"] != "vps-deploy" || len(f.audit[4]["targets"].([]any)) != 2 {
		t.Errorf("link diff: %v", f.audit[4])
	}
}

func TestAuditFailureDoesNotHideTheWrite(t *testing.T) {
	f := newFake(t)
	f.noAudit = true
	srv := httptest.NewServer(f)
	defer srv.Close()

	res := call(t, srv.URL, `update_post {"id":"`+idUz+`","title":"Yangi"}`)
	if f.posts[idUz].Title != "Yangi" {
		t.Fatal("the write itself must go through")
	}
	if e, _ := res[0]["audit_error"].(string); !strings.Contains(e, "not logged") {
		t.Errorf("the model must be told the log missed it: %v", res[0])
	}
}

func TestListChangesShortensLongValues(t *testing.T) {
	f := newFake(t)
	srv := httptest.NewServer(f)
	defer srv.Close()

	long := strings.Repeat("a", 2000)
	res := call(t, srv.URL,
		`update_post {"id":"`+idUz+`","body":"`+long+`"}`,
		`list_changes {"limit":5}`,
	)
	items := res[1]["items"].([]any)
	body := items[0].(map[string]any)["changes"].(map[string]any)["body"].(map[string]any)
	if to := body["to"].(string); !strings.HasSuffix(to, "(2000 chars)") || len(to) > 200 {
		t.Errorf("long value not shortened: %.80q", to)
	}
	if f.audit[0]["changes"].(map[string]any)["body"].(map[string]any)["to"] != long {
		t.Error("the log itself keeps the whole value")
	}
}

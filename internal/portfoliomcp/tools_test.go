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
		raw, _ := json.Marshal(in)
		var m map[string]any
		json.Unmarshal(raw, &m)
		f.puts = append(f.puts, m)
		p := f.posts[strings.TrimPrefix(path, "/posts/")]
		p.Title, p.Body, p.Description, p.Draft, p.Tags = in.Title, in.Body, in.Description, in.Draft, in.Tags
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

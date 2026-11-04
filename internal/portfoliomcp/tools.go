// Package portfoliomcp is the portfolio's MCP server: the tools an assistant
// (Claude Code, Claude Desktop) uses to read and change the portfolio through
// its own API - the same API the dashboard uses, so it is one more front end,
// not a way around the back end.
//
// Two rules shape every tool:
//
//   - Small answers. A list never carries post bodies; system, finance and
//     growth answers drop the per-day series. The model pays for every byte
//     it reads, and a summary is what it needs to decide what to read next.
//   - Nothing irreversible. There is no delete tool. Writes create drafts or
//     edit fields; publishing is its own tool, marked destructive so the
//     client asks first, and the instructions say to use it only when asked.
//
// The finance bot's users are out of reach on purpose: their data is theirs,
// and nothing here calls /finbot.
package portfoliomcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/mcp"
)

const Version = "0.1.0"

var (
	idRe   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// New builds the server with every tool and prompt.
func New(c *Client, now func() time.Time) *mcp.Server {
	if now == nil {
		now = time.Now
	}
	t := &tools{c: c, now: now}
	s := &mcp.Server{Name: "portfolio", Version: Version, Instructions: instructions}
	for _, tool := range t.all() {
		s.AddTool(tool)
	}
	for _, p := range prompts(now) {
		s.AddPrompt(p)
	}
	return s
}

type tools struct {
	c   *Client
	now func() time.Time
}

/* --------------------------------- shapes --------------------------------- */

// post and project mirror the API's responses (internal/api/dto.go).
type post struct {
	ID             string     `json:"id"`
	Locale         string     `json:"locale"`
	Slug           string     `json:"slug"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	Body           string     `json:"body"`
	Tags           []string   `json:"tags"`
	Cover          string     `json:"cover"`
	Featured       bool       `json:"featured"`
	Draft          bool       `json:"draft"`
	Date           string     `json:"date"`
	TranslationKey string     `json:"translation_key"`
	PublishedAt    *time.Time `json:"published_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// postInput is exactly the API's postInput: it rejects unknown fields.
type postInput struct {
	Locale         string   `json:"locale"`
	Slug           string   `json:"slug"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Body           string   `json:"body"`
	Tags           []string `json:"tags"`
	Cover          string   `json:"cover"`
	Featured       bool     `json:"featured"`
	Draft          bool     `json:"draft"`
	Date           string   `json:"date"`
	TranslationKey string   `json:"translation_key"`
}

func (p post) input() postInput {
	return postInput{p.Locale, p.Slug, p.Title, p.Description, p.Body, p.Tags, p.Cover,
		p.Featured, p.Draft, p.Date, p.TranslationKey}
}

type project struct {
	ID             string    `json:"id"`
	Locale         string    `json:"locale"`
	Slug           string    `json:"slug"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	Body           string    `json:"body"`
	Tags           []string  `json:"tags"`
	Stack          []string  `json:"stack"`
	URL            string    `json:"url"`
	Repo           string    `json:"repo"`
	Order          int32     `json:"order"`
	Featured       bool      `json:"featured"`
	Draft          bool      `json:"draft"`
	Date           string    `json:"date"`
	TranslationKey string    `json:"translation_key"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type projectInput struct {
	Locale         string   `json:"locale"`
	Slug           string   `json:"slug"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Body           string   `json:"body"`
	Tags           []string `json:"tags"`
	Stack          []string `json:"stack"`
	URL            string   `json:"url"`
	Repo           string   `json:"repo"`
	Order          int32    `json:"order"`
	Featured       bool     `json:"featured"`
	Draft          bool     `json:"draft"`
	Date           string   `json:"date"`
	TranslationKey string   `json:"translation_key"`
}

func (p project) input() projectInput {
	return projectInput{p.Locale, p.Slug, p.Title, p.Description, p.Body, p.Tags, p.Stack,
		p.URL, p.Repo, p.Order, p.Featured, p.Draft, p.Date, p.TranslationKey}
}

// itemSummary is a post or project without its body.
type itemSummary struct {
	ID             string     `json:"id"`
	Locale         string     `json:"locale"`
	Slug           string     `json:"slug"`
	Title          string     `json:"title"`
	Description    string     `json:"description,omitempty"`
	Draft          bool       `json:"draft"`
	Featured       bool       `json:"featured,omitempty"`
	Date           string     `json:"date"`
	Tags           []string   `json:"tags,omitempty"`
	Stack          []string   `json:"stack,omitempty"`
	URL            string     `json:"url,omitempty"`
	Repo           string     `json:"repo,omitempty"`
	TranslationKey string     `json:"translation_key,omitempty"`
	Words          int        `json:"words"`
	PublishedAt    *time.Time `json:"published_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func summarizePost(p post) itemSummary {
	return itemSummary{ID: p.ID, Locale: p.Locale, Slug: p.Slug, Title: p.Title,
		Description: p.Description, Draft: p.Draft, Featured: p.Featured, Date: p.Date,
		Tags: p.Tags, TranslationKey: p.TranslationKey, Words: len(strings.Fields(p.Body)),
		PublishedAt: p.PublishedAt, UpdatedAt: p.UpdatedAt}
}

func summarizeProject(p project) itemSummary {
	return itemSummary{ID: p.ID, Locale: p.Locale, Slug: p.Slug, Title: p.Title,
		Description: p.Description, Draft: p.Draft, Featured: p.Featured, Date: p.Date,
		Tags: p.Tags, Stack: p.Stack, URL: p.URL, Repo: p.Repo,
		TranslationKey: p.TranslationKey, Words: len(strings.Fields(p.Body)), UpdatedAt: p.UpdatedAt}
}

/* ---------------------------------- tools --------------------------------- */

var (
	readOnly    = &mcp.Annotations{ReadOnlyHint: true, IdempotentHint: true}
	writeSafe   = &mcp.Annotations{IdempotentHint: true}
	writePublic = &mcp.Annotations{DestructiveHint: true, IdempotentHint: true, OpenWorldHint: true}
)

func schema(s string) json.RawMessage { return json.RawMessage(s) }

const (
	emptySchema  = `{"type":"object","properties":{}}`
	idSchema     = `{"type":"object","properties":{"id":{"type":"string","description":"UUID from a list tool"}},"required":["id"]}`
	rangeSchema  = `{"type":"object","properties":{"from":{"type":"string","description":"YYYY-MM-DD"},"to":{"type":"string","description":"YYYY-MM-DD, default today"}}}`
	localeFilter = `"locale":{"type":"string","enum":["uz","en"]}`
)

func (t *tools) all() []mcp.Tool {
	return []mcp.Tool{
		{
			Name: "content_audit", Title: "Kontent auditi",
			Description: "Checks every post and project and lists what needs work: missing uz/en pair, old drafts, empty descriptions, thin bodies, demo leftovers, projects without repo/url or not in problem → decision → result form. Start here for any content question.",
			InputSchema: schema(emptySchema), Annotations: readOnly, Handler: t.contentAudit,
		},
		{
			Name: "list_posts", Title: "Postlar ro'yxati",
			Description: "Blog posts without bodies (word count instead). Use get_post for one body.",
			InputSchema: schema(`{"type":"object","properties":{` + localeFilter + `,"status":{"type":"string","enum":["all","draft","published"]}}}`),
			Annotations: readOnly, Handler: t.listPosts,
		},
		{
			Name: "get_post", Title: "Post", Description: "One post with its full Markdown body.",
			InputSchema: schema(idSchema), Annotations: readOnly, Handler: t.getPost,
		},
		{
			Name: "create_post_draft", Title: "Yangi qoralama post",
			Description: "Creates a post as a draft (never published by this tool). For a bilingual post create both locales with the same translation_key.",
			InputSchema: schema(`{"type":"object","properties":{
 "locale":{"type":"string","enum":["uz","en"]},
 "slug":{"type":"string","description":"lowercase-words-with-hyphens, unique per locale"},
 "title":{"type":"string"},"description":{"type":"string","description":"one or two sentences for cards and SEO"},
 "body":{"type":"string","description":"Markdown/MDX"},
 "tags":{"type":"array","items":{"type":"string"}},
 "date":{"type":"string","description":"YYYY-MM-DD, default today"},
 "translation_key":{"type":"string","description":"same value on the uz and en versions"},
 "cover":{"type":"string"}},
 "required":["locale","slug","title","body"]}`),
			Annotations: &mcp.Annotations{}, Handler: t.createPostDraft,
		},
		{
			Name: "update_post", Title: "Postni tahrirlash",
			Description: "Changes only the fields given; everything else, including draft/published state, stays. Read the post first.",
			InputSchema: schema(`{"type":"object","properties":{
 "id":{"type":"string"},"slug":{"type":"string"},"title":{"type":"string"},"description":{"type":"string"},
 "body":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"date":{"type":"string"},
 "translation_key":{"type":"string"},"cover":{"type":"string"},"featured":{"type":"boolean"}},
 "required":["id"]}`),
			Annotations: writeSafe, Handler: t.updatePost,
		},
		{
			Name: "set_post_published", Title: "Nashr qilish / yashirish",
			Description: "Publishes a draft on the public site, or takes a published post back to draft. Only when the user explicitly asked for it in this conversation.",
			InputSchema: schema(`{"type":"object","properties":{"id":{"type":"string"},"published":{"type":"boolean"}},"required":["id","published"]}`),
			Annotations: writePublic, Handler: t.setPostPublished,
		},
		{
			Name: "list_projects", Title: "Loyihalar ro'yxati",
			Description: "Portfolio projects without bodies.",
			InputSchema: schema(`{"type":"object","properties":{` + localeFilter + `}}`),
			Annotations: readOnly, Handler: t.listProjects,
		},
		{
			Name: "get_project", Title: "Loyiha", Description: "One project with its full body.",
			InputSchema: schema(idSchema), Annotations: readOnly, Handler: t.getProject,
		},
		{
			Name: "update_project", Title: "Loyihani tahrirlash",
			Description: "Changes only the fields given; draft/published state stays. Read the project first.",
			InputSchema: schema(`{"type":"object","properties":{
 "id":{"type":"string"},"slug":{"type":"string"},"title":{"type":"string"},"description":{"type":"string"},
 "body":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"stack":{"type":"array","items":{"type":"string"}},
 "url":{"type":"string"},"repo":{"type":"string"},"order":{"type":"integer"},"featured":{"type":"boolean"},
 "date":{"type":"string"},"translation_key":{"type":"string"}},
 "required":["id"]}`),
			Annotations: writeSafe, Handler: t.updateProject,
		},
		{
			Name: "list_messages", Title: "Aloqa xabarlari",
			Description: "Messages from the site's contact form, newest first, with unread count.",
			InputSchema: schema(`{"type":"object","properties":{"unread_only":{"type":"boolean"},"limit":{"type":"integer","minimum":1,"maximum":100}}}`),
			Annotations: readOnly, Handler: t.listMessages,
		},
		{
			Name: "mark_message_read", Title: "Xabarni o'qilgan qilish",
			Description: "Marks one contact message as read.",
			InputSchema: schema(idSchema), Annotations: writeSafe, Handler: t.markMessageRead,
		},
		{
			Name: "list_invitations", Title: "Taklifnomalar",
			Description: "Answers from the invitation site, newest first.",
			InputSchema: schema(`{"type":"object","properties":{"limit":{"type":"integer","minimum":1,"maximum":100}}}`),
			Annotations: readOnly, Handler: t.listInvitations,
		},
		{
			Name: "system_overview", Title: "Tizim holati",
			Description: "Uptime of each domain (7 days), error groups, last deploys, Telegram outbox, ops bot heartbeat, last backup.",
			InputSchema: schema(emptySchema), Annotations: readOnly, Handler: t.systemOverview,
		},
		{
			Name: "finance_summary", Title: "Shaxsiy moliya",
			Description: "Ibrohim's own money log for a period (default: this month so far): spent, income, net, by category, by month, largest entries, debts, comparison with the previous period. Amounts are in minor units (so'm × 100).",
			InputSchema: schema(rangeSchema), Annotations: readOnly, Handler: t.financeSummary,
		},
		{
			Name: "growth_summary", Title: "Ingliz tili va sprintlar",
			Description: "English practice for a period (default: last 28 days): minutes by kind and day, sprints with outcomes, IELTS mock scores.",
			InputSchema: schema(rangeSchema), Annotations: readOnly, Handler: t.growthSummary,
		},
	}
}

// The site never shows an em or en dash, only a plain "-" (Ibrohim's text
// rule in CLAUDE.md, for UI strings and for posts and projects alike).
// Models write them out of habit, so the text is fixed here rather than
// trusted to the instructions.
var dashes = strings.NewReplacer(" \u2014 ", " - ", "\u2014", "-", " \u2013 ", " - ", "\u2013", "-")

func fixDashes(fields ...*string) int {
	n := 0
	for _, f := range fields {
		if f == nil {
			continue
		}
		if c := strings.Count(*f, "\u2014") + strings.Count(*f, "\u2013"); c > 0 {
			*f = dashes.Replace(*f)
			n += c
		}
	}
	return n
}

// withDashes adds the count to a result when something was replaced.
func withDashes(res map[string]any, n int) map[string]any {
	if n > 0 {
		res["dashes_replaced"] = n
	}
	return res
}

func decode(args json.RawMessage, v any) error {
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

func checkID(id string) (string, error) {
	if !idRe.MatchString(id) {
		return "", fmt.Errorf("id %q is not a UUID; take it from a list tool", id)
	}
	return id, nil
}

/* ---------------------------------- posts --------------------------------- */

func (t *tools) posts(ctx context.Context, locale, status string) ([]post, error) {
	q := url.Values{}
	if locale != "" {
		q.Set("locale", locale)
	}
	if status == "draft" || status == "published" {
		q.Set("status", status)
	}
	var out []post
	err := t.c.Do(ctx, http.MethodGet, "/api/v1/posts", q, nil, &out)
	return out, err
}

func (t *tools) listPosts(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct{ Locale, Status string }
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	ps, err := t.posts(ctx, in.Locale, in.Status)
	if err != nil {
		return nil, err
	}
	out := make([]itemSummary, 0, len(ps))
	for _, p := range ps {
		out = append(out, summarizePost(p))
	}
	return map[string]any{"count": len(out), "posts": out}, nil
}

func (t *tools) post(ctx context.Context, id string) (post, error) {
	var p post
	id, err := checkID(id)
	if err != nil {
		return p, err
	}
	err = t.c.Do(ctx, http.MethodGet, "/api/v1/posts/"+id, nil, nil, &p)
	return p, err
}

func (t *tools) getPost(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct{ ID string }
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	return t.post(ctx, in.ID)
}

func (t *tools) createPostDraft(ctx context.Context, args json.RawMessage) (any, error) {
	var in postInput
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	in.Draft, in.Featured = true, false
	fixed := fixDashes(&in.Title, &in.Description, &in.Body)
	if in.Date == "" {
		in.Date = t.now().Format("2006-01-02")
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}
	var p post
	if err := t.c.Do(ctx, http.MethodPost, "/api/v1/posts", nil, in, &p); err != nil {
		return nil, err
	}
	return withDashes(map[string]any{"created": summarizePost(p)}, fixed), nil
}

func (t *tools) updatePost(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		ID             string    `json:"id"`
		Slug           *string   `json:"slug"`
		Title          *string   `json:"title"`
		Description    *string   `json:"description"`
		Body           *string   `json:"body"`
		Tags           *[]string `json:"tags"`
		Date           *string   `json:"date"`
		TranslationKey *string   `json:"translation_key"`
		Cover          *string   `json:"cover"`
		Featured       *bool     `json:"featured"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	fixed := fixDashes(in.Title, in.Description, in.Body)
	p, err := t.post(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	next := p.input()
	var changed []string
	set := func(name string, dst *string, v *string) {
		if v != nil && *v != *dst {
			*dst, changed = *v, append(changed, name)
		}
	}
	set("slug", &next.Slug, in.Slug)
	set("title", &next.Title, in.Title)
	set("description", &next.Description, in.Description)
	set("body", &next.Body, in.Body)
	set("date", &next.Date, in.Date)
	set("translation_key", &next.TranslationKey, in.TranslationKey)
	set("cover", &next.Cover, in.Cover)
	if in.Tags != nil && strings.Join(*in.Tags, "\x00") != strings.Join(next.Tags, "\x00") {
		next.Tags, changed = *in.Tags, append(changed, "tags")
	}
	if in.Featured != nil && *in.Featured != next.Featured {
		next.Featured, changed = *in.Featured, append(changed, "featured")
	}
	if len(changed) == 0 {
		return map[string]any{"changed": []string{}, "post": summarizePost(p)}, nil
	}
	if next.Tags == nil {
		next.Tags = []string{}
	}
	var out post
	if err := t.c.Do(ctx, http.MethodPut, "/api/v1/posts/"+p.ID, nil, next, &out); err != nil {
		return nil, err
	}
	return withDashes(map[string]any{"changed": changed, "post": summarizePost(out)}, fixed), nil
}

func (t *tools) setPostPublished(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		ID        string `json:"id"`
		Published *bool  `json:"published"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	if in.Published == nil {
		return nil, fmt.Errorf("published is required: true to publish, false to take back to draft")
	}
	id, err := checkID(in.ID)
	if err != nil {
		return nil, err
	}
	var out post
	body := map[string]bool{"draft": !*in.Published}
	if err := t.c.Do(ctx, http.MethodPatch, "/api/v1/posts/"+id+"/publish", nil, body, &out); err != nil {
		return nil, err
	}
	return map[string]any{"post": summarizePost(out)}, nil
}

/* -------------------------------- projects -------------------------------- */

func (t *tools) projects(ctx context.Context, locale string) ([]project, error) {
	q := url.Values{}
	if locale != "" {
		q.Set("locale", locale)
	}
	var out []project
	err := t.c.Do(ctx, http.MethodGet, "/api/v1/projects", q, nil, &out)
	return out, err
}

func (t *tools) listProjects(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct{ Locale string }
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	ps, err := t.projects(ctx, in.Locale)
	if err != nil {
		return nil, err
	}
	out := make([]itemSummary, 0, len(ps))
	for _, p := range ps {
		out = append(out, summarizeProject(p))
	}
	return map[string]any{"count": len(out), "projects": out}, nil
}

func (t *tools) project(ctx context.Context, id string) (project, error) {
	var p project
	id, err := checkID(id)
	if err != nil {
		return p, err
	}
	err = t.c.Do(ctx, http.MethodGet, "/api/v1/projects/"+id, nil, nil, &p)
	return p, err
}

func (t *tools) getProject(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct{ ID string }
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	return t.project(ctx, in.ID)
}

func (t *tools) updateProject(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		ID             string    `json:"id"`
		Slug           *string   `json:"slug"`
		Title          *string   `json:"title"`
		Description    *string   `json:"description"`
		Body           *string   `json:"body"`
		Tags           *[]string `json:"tags"`
		Stack          *[]string `json:"stack"`
		URL            *string   `json:"url"`
		Repo           *string   `json:"repo"`
		Order          *int32    `json:"order"`
		Featured       *bool     `json:"featured"`
		Date           *string   `json:"date"`
		TranslationKey *string   `json:"translation_key"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	fixed := fixDashes(in.Title, in.Description, in.Body)
	p, err := t.project(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	next := p.input()
	var changed []string
	set := func(name string, dst *string, v *string) {
		if v != nil && *v != *dst {
			*dst, changed = *v, append(changed, name)
		}
	}
	setList := func(name string, dst *[]string, v *[]string) {
		if v != nil && strings.Join(*v, "\x00") != strings.Join(*dst, "\x00") {
			*dst, changed = *v, append(changed, name)
		}
	}
	set("slug", &next.Slug, in.Slug)
	set("title", &next.Title, in.Title)
	set("description", &next.Description, in.Description)
	set("body", &next.Body, in.Body)
	set("url", &next.URL, in.URL)
	set("repo", &next.Repo, in.Repo)
	set("date", &next.Date, in.Date)
	set("translation_key", &next.TranslationKey, in.TranslationKey)
	setList("tags", &next.Tags, in.Tags)
	setList("stack", &next.Stack, in.Stack)
	if in.Order != nil && *in.Order != next.Order {
		next.Order, changed = *in.Order, append(changed, "order")
	}
	if in.Featured != nil && *in.Featured != next.Featured {
		next.Featured, changed = *in.Featured, append(changed, "featured")
	}
	if len(changed) == 0 {
		return map[string]any{"changed": []string{}, "project": summarizeProject(p)}, nil
	}
	if next.Tags == nil {
		next.Tags = []string{}
	}
	if next.Stack == nil {
		next.Stack = []string{}
	}
	var out project
	if err := t.c.Do(ctx, http.MethodPut, "/api/v1/projects/"+p.ID, nil, next, &out); err != nil {
		return nil, err
	}
	return withDashes(map[string]any{"changed": changed, "project": summarizeProject(out)}, fixed), nil
}

/* ---------------------------------- audit --------------------------------- */

type finding struct {
	Type   string   `json:"type"` // post | project
	ID     string   `json:"id"`
	Locale string   `json:"locale"`
	Slug   string   `json:"slug"`
	Title  string   `json:"title"`
	Draft  bool     `json:"draft"`
	Issues []string `json:"issues"`
}

const thinBody = 150 // words

var demoRe = regexp.MustCompile(`(?i)\b(demo|test|example|sample|lorem|hello[- ]world)\b`)

// Section words that make a project read as a case study, in either language.
var caseStudyWords = [][]string{
	{"muammo", "problem", "challenge"},
	{"qaror", "yechim", "decision", "solution", "approach"},
	{"natija", "result", "outcome", "impact"},
}

func (t *tools) contentAudit(ctx context.Context, _ json.RawMessage) (any, error) {
	ps, err := t.posts(ctx, "", "")
	if err != nil {
		return nil, err
	}
	prs, err := t.projects(ctx, "")
	if err != nil {
		return nil, err
	}
	today := t.now()

	// translation_key → locales that have it, per type
	pairs := map[string]map[string]bool{}
	mark := func(typ, key, locale string) {
		if key == "" {
			return
		}
		k := typ + "|" + key
		if pairs[k] == nil {
			pairs[k] = map[string]bool{}
		}
		pairs[k][locale] = true
	}
	for _, p := range ps {
		mark("post", p.TranslationKey, p.Locale)
	}
	for _, p := range prs {
		mark("project", p.TranslationKey, p.Locale)
	}
	paired := func(typ, key string) bool { return key != "" && len(pairs[typ+"|"+key]) >= 2 }

	common := func(typ, slug, title, desc, body, key string, draft bool, updated time.Time, tags []string) []string {
		var is []string
		if !paired(typ, key) {
			is = append(is, "no_translation_pair")
		}
		if draft {
			is = append(is, "draft_"+strconv.Itoa(int(today.Sub(updated).Hours()/24))+"_days_untouched")
		}
		if strings.TrimSpace(desc) == "" {
			is = append(is, "empty_description")
		}
		if n := len(strings.Fields(body)); n < thinBody {
			is = append(is, "thin_body_"+strconv.Itoa(n)+"_words")
		}
		if demoRe.MatchString(slug + " " + title + " " + strings.Join(tags, " ")) {
			is = append(is, "looks_like_demo")
		}
		return is
	}

	var findings []finding
	published, drafts := 0, 0
	for _, p := range ps {
		if p.Draft {
			drafts++
		} else {
			published++
		}
		if is := common("post", p.Slug, p.Title, p.Description, p.Body, p.TranslationKey, p.Draft, p.UpdatedAt, p.Tags); len(is) > 0 {
			findings = append(findings, finding{"post", p.ID, p.Locale, p.Slug, p.Title, p.Draft, is})
		}
	}
	for _, p := range prs {
		is := common("project", p.Slug, p.Title, p.Description, p.Body, p.TranslationKey, p.Draft, p.UpdatedAt, p.Tags)
		if strings.TrimSpace(p.Repo) == "" {
			is = append(is, "no_repo_link")
		}
		if strings.TrimSpace(p.URL) == "" {
			is = append(is, "no_live_url")
		}
		if !isCaseStudy(p.Body) {
			is = append(is, "not_problem_decision_result")
		}
		if len(is) > 0 {
			findings = append(findings, finding{"project", p.ID, p.Locale, p.Slug, p.Title, p.Draft, is})
		}
	}
	sort.SliceStable(findings, func(i, j int) bool { return len(findings[i].Issues) > len(findings[j].Issues) })

	return map[string]any{
		"summary": map[string]int{
			"posts": len(ps), "posts_published": published, "posts_draft": drafts,
			"projects": len(prs), "items_with_issues": len(findings),
		},
		"findings": findings,
	}, nil
}

// isCaseStudy: at least two of problem / decision / result are named.
func isCaseStudy(body string) bool {
	lower := strings.ToLower(body)
	n := 0
	for _, group := range caseStudyWords {
		for _, w := range group {
			if strings.Contains(lower, w) {
				n++
				break
			}
		}
	}
	return n >= 2
}

/* ---------------------------------- inbox --------------------------------- */

func limitArg(v, def int) int {
	if v < 1 || v > 100 {
		return def
	}
	return v
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (t *tools) listMessages(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		UnreadOnly bool `json:"unread_only"`
		Limit      int  `json:"limit"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	limit := limitArg(in.Limit, 20)
	var res struct {
		Items []struct {
			ID        string     `json:"id"`
			Name      string     `json:"name"`
			Email     string     `json:"email"`
			Message   string     `json:"message"`
			Locale    string     `json:"locale"`
			ReadAt    *time.Time `json:"read_at"`
			CreatedAt time.Time  `json:"created_at"`
		} `json:"items"`
		Total  int64 `json:"total"`
		Unread int64 `json:"unread"`
	}
	// Unread ones can sit behind read ones; read a page wide enough to find them.
	q := url.Values{"limit": {strconv.Itoa(max(limit, 200))}}
	if err := t.c.Do(ctx, http.MethodGet, "/api/v1/messages", q, nil, &res); err != nil {
		return nil, err
	}
	items := []map[string]any{}
	for _, m := range res.Items {
		if in.UnreadOnly && m.ReadAt != nil {
			continue
		}
		if len(items) == limit {
			break
		}
		items = append(items, map[string]any{
			"id": m.ID, "name": m.Name, "email": m.Email, "locale": m.Locale,
			"message": clipText(m.Message, 600), "unread": m.ReadAt == nil, "created_at": m.CreatedAt,
		})
	}
	return map[string]any{"total": res.Total, "unread": res.Unread, "items": items}, nil
}

func (t *tools) markMessageRead(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct{ ID string }
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	id, err := checkID(in.ID)
	if err != nil {
		return nil, err
	}
	if err := t.c.Do(ctx, http.MethodPatch, "/api/v1/messages/"+id+"/read", nil, nil, nil); err != nil {
		return nil, err
	}
	return map[string]any{"read": id}, nil
}

func (t *tools) listInvitations(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct{ Limit int }
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	var res struct {
		Items []map[string]any `json:"items"`
		Total int64            `json:"total"`
	}
	q := url.Values{"limit": {strconv.Itoa(limitArg(in.Limit, 20))}}
	if err := t.c.Do(ctx, http.MethodGet, "/api/v1/invitations", q, nil, &res); err != nil {
		return nil, err
	}
	for _, it := range res.Items {
		for _, k := range []string{"user_agent", "session_id", "food_id", "place_id", "venue_id", "food_emoji", "place_emoji"} {
			delete(it, k)
		}
	}
	return res, nil
}

/* ------------------------- system, money, growth -------------------------- */

func (t *tools) systemOverview(ctx context.Context, _ json.RawMessage) (any, error) {
	var res map[string]any
	if err := t.c.Do(ctx, http.MethodGet, "/api/v1/system", nil, nil, &res); err != nil {
		return nil, err
	}
	if errs, ok := res["errors"].([]any); ok {
		for _, e := range errs {
			if m, ok := e.(map[string]any); ok {
				if s, ok := m["message"].(string); ok {
					m["message"] = clipText(s, 200)
				}
				delete(m, "fingerprint")
			}
		}
		res["errors"] = firstN(errs, 10)
	}
	if d, ok := res["deploys"].([]any); ok {
		res["deploys"] = firstN(d, 5)
	}
	return res, nil
}

func firstN(xs []any, n int) []any {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

func rangeQuery(args json.RawMessage) (url.Values, error) {
	var in struct{ From, To string }
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	q := url.Values{}
	for k, v := range map[string]string{"from": in.From, "to": in.To} {
		if v == "" {
			continue
		}
		if !dateRe.MatchString(v) {
			return nil, fmt.Errorf("%s must be YYYY-MM-DD", k)
		}
		q.Set(k, v)
	}
	return q, nil
}

func (t *tools) financeSummary(ctx context.Context, args json.RawMessage) (any, error) {
	q, err := rangeQuery(args)
	if err != nil {
		return nil, err
	}
	var res map[string]any
	if err := t.c.Do(ctx, http.MethodGet, "/api/v1/finance/summary", q, nil, &res); err != nil {
		return nil, err
	}
	// The day-by-day series are for drawing charts; the totals answer questions.
	delete(res, "balance_series")
	delete(res, "by_day")
	if l, ok := res["largest"].([]any); ok {
		for _, e := range l {
			if m, ok := e.(map[string]any); ok {
				delete(m, "raw_text")
				delete(m, "created_at")
				delete(m, "updated_at")
			}
		}
		res["largest"] = firstN(l, 5)
	}
	// Twelve months come back even when most are empty; keep the ones with money.
	if months, ok := res["by_month"].([]any); ok {
		res["by_month"] = keep(months, func(m map[string]any) bool {
			return nonEmpty(m["amounts"]) || nonEmpty(m["income"])
		})
	}
	return res, nil
}

// keep returns the objects of xs for which ok is true.
func keep(xs []any, ok func(map[string]any) bool) []any {
	out := []any{}
	for _, x := range xs {
		if m, isMap := x.(map[string]any); isMap && ok(m) {
			out = append(out, m)
		}
	}
	return out
}

func nonEmpty(v any) bool {
	m, ok := v.(map[string]any)
	return ok && len(m) > 0
}

func (t *tools) growthSummary(ctx context.Context, args json.RawMessage) (any, error) {
	q, err := rangeQuery(args)
	if err != nil {
		return nil, err
	}
	var res map[string]any
	if err := t.c.Do(ctx, http.MethodGet, "/api/v1/growth/summary", q, nil, &res); err != nil {
		return nil, err
	}
	// Every day of the range comes back; the days with practice are the news.
	if days, ok := res["by_day"].([]any); ok {
		res["by_day"] = keep(days, func(d map[string]any) bool {
			m, _ := d["minutes"].(float64)
			return m > 0
		})
	}
	return res, nil
}

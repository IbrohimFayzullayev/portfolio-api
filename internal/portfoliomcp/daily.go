package portfoliomcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/finance"
	"github.com/ibrohimcoder/portfolio-api/internal/financetext"
	"github.com/ibrohimcoder/portfolio-api/internal/growth"
)

// The everyday writes: scheduling a post, logging English practice, adding a
// money entry. Each goes through the API like every other tool and is
// recorded in the agent log.
//
// The text tools use the same parsers as the ops bot (internal/financetext,
// internal/growth), so "taksi 25k" or "45 w essay" means the same thing in
// Telegram and in Claude. A model that rewords the line before sending it
// would get a different reading from the one the user is used to - the
// descriptions ask for the user's own words.

var (
	scheduleLayouts = []string{"2006-01-02 15:04", "2006-01-02T15:04", "02.01.2006 15:04"}
	tashkent        = financetext.Tashkent
)

// parseWhen reads a moment the way a person writes it, in Tashkent time, or
// an RFC 3339 time from a model that prefers one.
func parseWhen(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, l := range scheduleLayouts {
		if t, err := time.ParseInLocation(l, s, tashkent); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("at %q: write it as YYYY-MM-DD HH:MM (Tashkent time)", s)
}

func localTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.In(tashkent).Format("2006-01-02 15:04")
}

/* ------------------------------ schedule_post ------------------------------ */

func (t *tools) schedulePost(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		ID     string `json:"id"`
		At     string `json:"at"`
		Cancel bool   `json:"cancel"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	if (in.At == "") == !in.Cancel {
		return nil, errors.New("give either at (YYYY-MM-DD HH:MM, Tashkent) or cancel: true")
	}
	var at *time.Time
	if !in.Cancel {
		v, err := parseWhen(in.At)
		if err != nil {
			return nil, err
		}
		at = &v
	}
	p, err := t.post(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if !p.Draft {
		return nil, fmt.Errorf("%s is already published; only drafts can be scheduled", label(p.Locale, p.Slug))
	}
	if at == nil && p.PublishAt == nil {
		return map[string]any{"unchanged": true, "post": summarizePost(p)}, nil
	}
	if at != nil && p.PublishAt != nil && at.Equal(*p.PublishAt) {
		return map[string]any{"unchanged": true, "post": summarizePost(p)}, nil
	}

	var out post
	err = t.c.Do(ctx, http.MethodPatch, "/api/v1/posts/"+p.ID+"/schedule", nil,
		map[string]any{"publish_at": at}, &out)
	return t.audited(ctx, change{
		Tool: "schedule_post", TargetType: "post",
		Targets: []target{{p.ID, label(p.Locale, p.Slug)}},
		Changes: map[string]any{"publish_at": map[string]any{"from": localTime(p.PublishAt), "to": localTime(at)}},
	}, map[string]any{"post": summarizePost(out), "publish_at_tashkent": localTime(out.PublishAt)}, err)
}

/* ------------------------------ log_practice ------------------------------- */

func (t *tools) logPractice(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		Text    string `json:"text"`
		Kind    string `json:"kind"`
		Minutes int    `json:"minutes"`
		Note    string `json:"note"`
		Day     string `json:"day"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	today := financetext.DayStart(t.now())
	body := map[string]any{}
	switch {
	case strings.TrimSpace(in.Text) != "":
		// The /qildim syntax: "45 w essay", "kecha 25 anki", "1s l cambridge".
		l, err := growth.ParseLog(in.Text)
		if err != nil {
			return nil, fmt.Errorf("%v (format: \"45 w essay\", \"kecha 30 s part 1\"; w s l r v g m)", err)
		}
		body["kind"], body["minutes"], body["note"] = l.Kind, l.Minutes, l.Note
		body["day"] = today.AddDate(0, 0, l.DayOffset).Format("2006-01-02")
	case in.Kind != "" && in.Minutes > 0:
		day := in.Day
		switch strings.ToLower(strings.TrimSpace(day)) {
		case "", "today", "bugun":
			day = today.Format("2006-01-02")
		case "yesterday", "kecha":
			day = today.AddDate(0, 0, -1).Format("2006-01-02")
		}
		body["kind"], body["minutes"], body["note"], body["day"] = in.Kind, in.Minutes, in.Note, day
	default:
		return nil, errors.New("give text (\"45 w essay\") or kind and minutes")
	}

	var out struct {
		ID      string `json:"id"`
		Day     string `json:"day"`
		Kind    string `json:"kind"`
		Minutes int    `json:"minutes"`
		Note    string `json:"note"`
	}
	err := t.c.Do(ctx, http.MethodPost, "/api/v1/growth/logs", nil, body, &out)
	lbl := fmt.Sprintf("%v %vm %v", body["kind"], body["minutes"], body["day"])
	return t.audited(ctx, change{
		Tool: "log_practice", TargetType: "practice",
		Targets: []target{{out.ID, lbl}},
		Changes: map[string]any{"created": body},
	}, map[string]any{"logged": out}, err)
}

/* ---------------------------- add_finance_entry ---------------------------- */

// financeView is how a parsed line is shown back: what the bot's card shows.
func financeView(e financetext.Entry) map[string]any {
	k, _ := finance.KindOf(e.Kind)
	v := map[string]any{
		"kind": e.Kind, "kind_label": k.Label,
		"amount": financetext.FormatMoney(e.AmountMinor, e.Currency), "amount_minor": e.AmountMinor,
		"currency": e.Currency, "category": e.Category, "category_label": finance.CategoryByKey(e.Category).Label,
		"date": e.SpentOn.Format("2006-01-02"),
	}
	if e.Person != "" {
		v["person"] = e.Person
	}
	if e.Note != "" {
		v["note"] = e.Note
	}
	return v
}

func (t *tools) addFinanceEntry(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		Text   string `json:"text"`
		DryRun bool   `json:"dry_run"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, errors.New("text is required: the line as the user wrote it, e.g. \"taksi 25k\"")
	}
	e, err := financetext.Parse(text, financetext.DayStart(t.now()))
	switch {
	case errors.Is(err, financetext.ErrNoAmount):
		return nil, errors.New("no amount in the line (25k, 1.5mln, 30000, $12)")
	case errors.Is(err, financetext.ErrNoPerson):
		return nil, errors.New("a debt needs a name: \"Aliga 500k qarz berdim\"")
	case err != nil:
		return nil, err
	}
	view := financeView(e)
	if in.DryRun {
		return map[string]any{"dry_run": true, "parsed": view}, nil
	}

	body := map[string]any{
		"kind": e.Kind, "amount_minor": e.AmountMinor, "currency": e.Currency,
		"category": e.Category, "person": e.Person, "note": e.Note,
		"spent_on": e.SpentOn.Format("2006-01-02"),
	}
	var out struct {
		ID string `json:"id"`
	}
	err = t.c.Do(ctx, http.MethodPost, "/api/v1/finance/entries", nil, body, &out)
	return t.audited(ctx, change{
		Tool: "add_finance_entry", TargetType: "finance_entry",
		Targets: []target{{out.ID, fmt.Sprintf("%s %s", view["category"], view["date"])}},
		Changes: map[string]any{"created": view, "text": text},
	}, map[string]any{"id": out.ID, "recorded": view}, err)
}

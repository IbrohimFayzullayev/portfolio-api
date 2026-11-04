package tutor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/finance"
	"github.com/ibrohimcoder/portfolio-api/internal/financetext"
)

type Agent struct {
	LLM       LLM
	Model     string
	MaxTurns  int // model round trips; default 12
	MaxTokens int // per answer; default 4000
	Now       func() time.Time
}

// TestCase is a made-up line for parse_test.go. Never a real one: the
// repository is public.
type TestCase struct {
	Text     string `json:"text"`
	Category string `json:"category"`

	Got     financetext.Entry `json:"-"` // how the parser reads it with the proposals
	Problem string            `json:"-"` // why it was left out, when it was
}

type Final struct {
	Proposals []Proposal
	Result    Result // re-computed here, never taken from the model
	Tests     []TestCase
	Dropped   []TestCase
	Notes     string // the model's suggestions a person has to decide on
	Usage     Usage
	Turns     int
}

var ErrNoFinish = errors.New("the model stopped without calling finish")

// Run gives the model the week's material and lets it propose, check and
// finish. Whatever it claims, the result in Final is computed from the
// samples: an unsafe proposal set is never returned.
func (a *Agent) Run(ctx context.Context, samples []Sample) (Final, error) {
	if a.MaxTurns == 0 {
		a.MaxTurns = 12
	}
	if a.MaxTokens == 0 {
		a.MaxTokens = 4000
	}
	if a.Now == nil {
		a.Now = time.Now
	}

	brief, err := json.MarshalIndent(Brief(samples), "", " ")
	if err != nil {
		return Final{}, err
	}
	msgs := []Message{{Role: "user", Content: []Block{{Type: "text", Text: string(brief)}}}}
	var fin Final

	for turn := 1; turn <= a.MaxTurns; turn++ {
		fin.Turns = turn
		res, err := a.LLM.Send(ctx, Request{
			Model: a.Model, MaxTokens: a.MaxTokens, System: systemPrompt,
			Tools: tools, Messages: msgs,
		})
		if err != nil {
			return fin, err
		}
		fin.Usage.InputTokens += res.Usage.InputTokens
		fin.Usage.OutputTokens += res.Usage.OutputTokens
		msgs = append(msgs, Message{Role: "assistant", Content: res.Content})

		var results []Block
		done := false
		for _, b := range res.Content {
			if b.Type != "tool_use" {
				continue
			}
			out, isErr, finished := a.runTool(b, samples, &fin)
			results = append(results, Block{Type: "tool_result", ToolUseID: b.ID, Content: out, IsError: isErr})
			done = done || finished
		}
		if done {
			return fin, nil
		}
		if len(results) == 0 {
			// Talked instead of acting. One nudge per turn, until turns run out.
			results = []Block{{Type: "text", Text: "Call evaluate to check proposals, then finish. Do not answer in prose."}}
		}
		msgs = append(msgs, Message{Role: "user", Content: results})
	}
	return fin, ErrNoFinish
}

func (a *Agent) runTool(b Block, samples []Sample, fin *Final) (out string, isErr, finished bool) {
	switch b.Name {
	case "parse":
		var in struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(b.Input, &in); err != nil {
			return "bad input: " + err.Error(), true, false
		}
		e, err := financetext.Parse(in.Text, financetext.DayStart(a.Now()))
		if err != nil {
			return "error: " + err.Error(), false, false
		}
		return mustJSON(map[string]any{
			"kind": e.Kind, "category": e.Category, "person_found": e.Person != "", "note": e.Note,
		}), false, false

	case "evaluate":
		var in struct {
			Proposals []Proposal `json:"proposals"`
		}
		if err := json.Unmarshal(b.Input, &in); err != nil {
			return "bad input: " + err.Error(), true, false
		}
		r, err := Evaluate(samples, in.Proposals)
		if err != nil {
			return err.Error(), true, false
		}
		return mustJSON(clip(r)), false, false

	case "finish":
		var in struct {
			Proposals []Proposal `json:"proposals"`
			Tests     []TestCase `json:"test_cases"`
			Notes     string     `json:"notes"`
		}
		if err := json.Unmarshal(b.Input, &in); err != nil {
			return "bad input: " + err.Error(), true, false
		}
		r, err := Evaluate(samples, in.Proposals)
		if err != nil {
			return err.Error(), true, false
		}
		if !r.Safe() {
			return "rejected: these proposals break lines people accepted. Remove the stems that cause it and finish again.\n" +
				mustJSON(clip(r).Broken), true, false
		}
		fin.Proposals = dedupe(in.Proposals)
		fin.Result = r
		fin.Notes = strings.TrimSpace(in.Notes)
		fin.Tests, fin.Dropped = verifyTests(in.Tests, fin.Proposals, samples, a.Now())
		return "ok", false, true
	}
	return "unknown tool " + b.Name, true, false
}

// verifyTests keeps the made-up lines that parse, with the proposals, to the
// category they claim - and none that is a real message.
func verifyTests(tests []TestCase, props []Proposal, samples []Sample, now time.Time) (kept, dropped []TestCase) {
	real := map[string]bool{}
	for _, s := range samples {
		real[strings.ToLower(strings.TrimSpace(s.Text))] = true
	}
	restore, err := Apply(props)
	if err != nil {
		return nil, tests
	}
	defer restore()
	today := financetext.DayStart(now)
	for _, t := range tests {
		t.Text = strings.TrimSpace(t.Text)
		switch got, perr := financetext.Parse(t.Text, today); {
		case real[strings.ToLower(t.Text)]:
			t.Problem = "real message"
		case strings.Contains(strings.ToLower(t.Text), "ism"):
			t.Problem = "contains a masked name"
		case perr != nil:
			t.Problem = perr.Error()
		case got.Category != t.Category:
			t.Got, t.Problem = got, "parses to "+got.Category
		default:
			t.Got = got
			kept = append(kept, t)
			continue
		}
		dropped = append(dropped, t)
	}
	return kept, dropped
}

// clip keeps tool results small: the model needs examples, not all of them.
func clip(r Result) Result {
	cut := func(c []Change, n int) []Change {
		if len(c) > n {
			return c[:n]
		}
		return c
	}
	r.Fixed, r.Broken = cut(r.Fixed, 15), cut(r.Broken, 15)
	r.Shifted, r.NewlyRead = cut(r.Shifted, 10), cut(r.NewlyRead, 10)
	return r
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("encode: %v", err)
	}
	return string(b)
}

/* --------------------------------- brief ---------------------------------- */

type catalogEntry struct {
	Key   string   `json:"key"`
	Label string   `json:"label"`
	Stems []string `json:"stems,omitempty"`
}

// Brief is the first message: the state of the parser and its material.
func Brief(samples []Sample) map[string]any {
	cat := func(list []finance.Category) []catalogEntry {
		out := make([]catalogEntry, 0, len(list))
		for _, c := range list {
			out = append(out, catalogEntry{c.Key, c.Label, c.Stems})
		}
		return out
	}
	return map[string]any{
		"summary":             Summary(samples),
		"spending_categories": cat(finance.Categories),
		"income_categories":   cat(finance.IncomeCategories),
		"read_wrong":          Wrong(samples, 60),
		"unreadable":          Unreadable(samples, 60),
		"unknown_words":       UnknownWords(samples, 50),
	}
}

/* --------------------------------- prompt --------------------------------- */

const systemPrompt = `You improve the keyword catalog of a rule-based parser for an Uzbek personal-finance Telegram bot.

People write lines like "tushlik 35k", "kecha taksi 18000", "Aliga 500k qarz berdim", "+10mln oylik". The parser (Go, deterministic) finds the amount and date, decides the kind (expense, income, gift, lent, repaid_in, borrowed, repaid_out) from verbs and case endings, and picks a category from word stems. A stem matches a word that is the stem itself or the stem plus a common suffix (-ga, -da, -dan, -ni, -lar...); stems of five letters or more match any ending.

The first message is real usage data, already masked: every digit is 9 and every known person name is "Ism" (with its case ending, e.g. "Ismga"). It contains:
- read_wrong: lines the parser reads differently from what was stored. Source "corrected" means the owner edited the row: strong evidence. "accepted" means it was stored that way and left alone.
- unreadable: lines the parser could not read at all (no amount, or a debt with no name).
- unknown_words: words of lines that still fall into the "boshqa" (other) category.
- the current catalog with every stem.

Your job: propose new stems for EXISTING categories that fix real lines, then prove them.
Rules:
1. Only two proposal types exist: {"type":"stem","category":<spending key>,"stem":...} and {"type":"income_stem","category":<income key>,"stem":...}. Never invent a category. Never use qarz, boshqa, kirim_boshqa or oila.
2. A stem is one lower-case word, at least 3 letters, as people actually type it (Latin script; Russian loanwords are fine). Prefer the shortest form that is still unambiguous; short stems (3-4 letters) only match exact suffixes, so a common word may need its own spelling variants.
3. Always call evaluate before finish. "broken" must be empty: a stem that breaks one accepted line is wrong, drop it. Look at "shifted" too.
4. Propose only what the data supports. Five good stems beat thirty guesses. Zero is a fine answer.
5. Things you cannot fix with a stem (a word that is wrongly read as a name, a new category, an amount format the parser misses, a grammar rule) go in notes, briefly, for the developer.
6. In finish, add 1-3 made-up test lines per proposed stem, written like a real user would ("pepsi 12k"), with the category you expect. Never copy a line from the data, never use "Ism", never use real names.
7. Write notes in Uzbek (Latin script), short, as a list.`

func schema(s string) json.RawMessage { return json.RawMessage(s) }

const proposalSchema = `{"type":"array","items":{"type":"object","properties":{
 "type":{"type":"string","enum":["stem","income_stem"]},
 "category":{"type":"string"},
 "stem":{"type":"string"},
 "why":{"type":"string","description":"one short line: which data it fixes"}},
 "required":["type","category","stem"]}}`

var tools = []Tool{
	{
		Name:        "parse",
		Description: "Run the current parser (without your proposals) on one line and see what it reads.",
		InputSchema: schema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
	},
	{
		Name:        "evaluate",
		Description: "Apply proposals in memory and re-read every real line, before and after. Returns counts and examples of fixed, broken (regressions), shifted and newly readable lines.",
		InputSchema: schema(`{"type":"object","properties":{"proposals":` + proposalSchema + `},"required":["proposals"]}`),
	},
	{
		Name:        "finish",
		Description: "Submit the final proposals, test lines and notes. Rejected if any proposal breaks a line.",
		InputSchema: schema(`{"type":"object","properties":{
 "proposals":` + proposalSchema + `,
 "test_cases":{"type":"array","items":{"type":"object","properties":{"text":{"type":"string"},"category":{"type":"string"}},"required":["text","category"]}},
 "notes":{"type":"string"}},
 "required":["proposals","test_cases","notes"]}`),
	},
}

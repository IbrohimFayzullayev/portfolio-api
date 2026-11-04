package tutor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/finance"
	"github.com/ibrohimcoder/portfolio-api/internal/financetext"
)

var (
	created = time.Date(2026, 10, 20, 9, 0, 0, 0, financetext.Tashkent)
	now     = time.Date(2026, 11, 4, 9, 0, 0, 0, financetext.Tashkent)
)

// row is an entry as the bot stored it; edited marks a later change by hand.
func row(raw, kind, cat, person string, minor int64, edited bool) EntryRow {
	r := EntryRow{RawText: raw, Kind: kind, Category: cat, Person: person,
		AmountMinor: minor, Currency: "UZS", CreatedAt: created, UpdatedAt: created}
	if edited {
		r.UpdatedAt = created.Add(time.Minute)
	}
	return r
}

func fixture() []Sample {
	return BuildSamples([]EntryRow{
		// "lagmon" without the apostrophe is not a stem yet: the owner fixed it.
		row("lagmon 40k", "expense", "ovqat", "", 40_000_00, true),
		row("lagmon 35k", "expense", "boshqa", "", 35_000_00, false),
		row("taksi 20k", "expense", "transport", "", 20_000_00, false),
		row("kitoblar 50k", "expense", "talim", "", 50_000_00, false),
		row("Aliga 500k qarz", "lent", "qarz", "Ali", 500_000_00, false),
	}, []FailureRow{
		{Text: "Dilnozaga yarim mln", Reason: "no_amount", CreatedAt: created},
	})
}

func TestBuildSamples(t *testing.T) {
	s := fixture()
	if len(s) != 6 {
		t.Fatalf("got %d samples, want 6", len(s))
	}
	if s[0].Source != SourceCorrected || s[1].Source != SourceAccepted {
		t.Errorf("sources: %q %q", s[0].Source, s[1].Source)
	}
	if s[1].Want.Category != "" {
		t.Errorf("an accepted 'boshqa' must not label the category, got %q", s[1].Want.Category)
	}
	if s[0].Want.Category != "ovqat" {
		t.Errorf("a corrected row labels its category, got %q", s[0].Want.Category)
	}
	if got := s[4].Masked; got != "Ismga 999k qarz" {
		t.Errorf("masked %q", got)
	}
	if s[5].Want != nil || s[5].Reason != "no_amount" {
		t.Errorf("failure sample: %+v", s[5])
	}
}

func TestMasker(t *testing.T) {
	m := NewMasker([]string{"Ali", "aka", "Bo"})
	cases := map[string]string{
		"Ali aka 1.5mln qaytardi": "Ism aka 9.9mln qaytardi", // kinship words stay
		"aliga 200k":              "ismga 999k",
		"Bobur 10k":               "Bobur 99k", // names under three letters are not masked
		"tushlik 35000":           "tushlik 99999",
	}
	for in, want := range cases {
		if got := m.Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEvaluateFixesAndRestores(t *testing.T) {
	before := len(finance.Categories[0].Stems)
	r, err := Evaluate(fixture(), []Proposal{{Type: ProposeStem, Category: "ovqat", Stem: "lagmon"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(finance.Categories[0].Stems) != before {
		t.Fatal("Evaluate left the proposal in the catalog")
	}
	if !r.Safe() {
		t.Fatalf("broken: %+v", r.Broken)
	}
	if len(r.Fixed) != 1 || r.Fixed[0].Masked != "lagmon 99k" {
		t.Errorf("fixed: %+v", r.Fixed)
	}
	// The accepted "boshqa" row moves but its label does not decide: shifted.
	if len(r.Shifted) != 1 {
		t.Errorf("shifted: %+v", r.Shifted)
	}
}

func TestEvaluateCatchesRegression(t *testing.T) {
	// ovqat comes before talim, so this steals "kitoblar".
	r, err := Evaluate(fixture(), []Proposal{{Type: ProposeStem, Category: "ovqat", Stem: "kitoblar"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Safe() || len(r.Broken) != 1 {
		t.Fatalf("want one regression, got %+v", r.Broken)
	}
}

func TestProposalValidate(t *testing.T) {
	bad := []Proposal{
		{Type: ProposeStem, Category: "ovqat", Stem: "taksi"},      // exists
		{Type: ProposeStem, Category: "kripto", Stem: "binance"},   // no such category
		{Type: ProposeStem, Category: "boshqa", Stem: "nimadir"},   // stemless
		{Type: ProposeStem, Category: "ovqat", Stem: "Lagmon"},     // not lower case
		{Type: ProposeStem, Category: "ovqat", Stem: "ab"},         // too short
		{Type: ProposeStem, Category: "ovqat", Stem: "osh 2"},      // not one word
		{Type: "not_name", Category: "", Stem: "tabaka"},           // unknown type
		{Type: ProposeIncomeStem, Category: "ovqat", Stem: "kesh"}, // wrong list
	}
	for _, p := range bad {
		if err := p.Validate(); err == nil {
			t.Errorf("%+v: want an error", p)
		}
	}
	if err := (Proposal{Type: ProposeIncomeStem, Category: "qoshimcha", Stem: "kwork"}).Validate(); err != nil {
		t.Errorf("valid income stem: %v", err)
	}
}

/* ------------------------------- agent loop ------------------------------- */

// script answers each call with the next canned response.
type script struct {
	steps []Response
	reqs  []Request
}

func (s *script) Send(_ context.Context, req Request) (Response, error) {
	s.reqs = append(s.reqs, req)
	if len(s.reqs) > len(s.steps) {
		return Response{}, errors.New("script ran out")
	}
	return s.steps[len(s.reqs)-1], nil
}

func toolUse(id, name string, input any) Block {
	b, _ := json.Marshal(input)
	return Block{Type: "tool_use", ID: id, Name: name, Input: b}
}

func TestAgentRun(t *testing.T) {
	lagmon := []Proposal{{Type: ProposeStem, Category: "ovqat", Stem: "lagmon", Why: "tuzatilgan qator"}}
	llm := &script{steps: []Response{
		{Content: []Block{toolUse("1", "evaluate", map[string]any{"proposals": lagmon})}, StopReason: "tool_use"},
		// An unsafe finish first: must be rejected and the loop go on.
		{Content: []Block{toolUse("2", "finish", map[string]any{
			"proposals":  append(lagmon, Proposal{Type: ProposeStem, Category: "ovqat", Stem: "kitoblar"}),
			"test_cases": []any{}, "notes": "",
		})}, StopReason: "tool_use"},
		{Content: []Block{toolUse("3", "finish", map[string]any{
			"proposals": lagmon,
			"test_cases": []map[string]string{
				{"text": "kecha lagmon 45k", "category": "ovqat"},
				{"text": "lagmon 40k", "category": "ovqat"}, // a real line: dropped
				{"text": "lagmonga 30k", "category": "dam"}, // wrong category: dropped
			},
			"notes": "- \"yarim mln\" summasini parser tushunmaydi",
		})}, StopReason: "tool_use"},
	}}
	for i := range llm.steps {
		llm.steps[i].Usage = Usage{InputTokens: 100, OutputTokens: 10}
	}

	a := &Agent{LLM: llm, Model: "test", Now: func() time.Time { return now }}
	fin, err := a.Run(context.Background(), fixture())
	if err != nil {
		t.Fatal(err)
	}
	if fin.Turns != 3 || len(fin.Proposals) != 1 || fin.Proposals[0].Stem != "lagmon" {
		t.Fatalf("final: turns %d, proposals %+v", fin.Turns, fin.Proposals)
	}
	if len(fin.Tests) != 1 || fin.Tests[0].Text != "kecha lagmon 45k" || len(fin.Dropped) != 2 {
		t.Errorf("tests %+v dropped %+v", fin.Tests, fin.Dropped)
	}
	if fin.Usage.InputTokens != 300 {
		t.Errorf("usage %+v", fin.Usage)
	}

	// The rejection went back to the model as an error result.
	last := llm.reqs[2].Messages
	res := last[len(last)-1].Content[0]
	if !res.IsError || !strings.Contains(res.Content, "rejected") {
		t.Errorf("want a rejection, got %+v", res)
	}
	// Nothing real in the brief: no unmasked amount, no name.
	brief := llm.reqs[0].Messages[0].Content[0].Text
	for _, leak := range []string{"40k", "Aliga", "Dilnoza", "500k"} {
		if strings.Contains(brief, leak) {
			t.Errorf("brief leaks %q", leak)
		}
	}

	report := Report(ReportInput{Samples: fixture(), Final: fin, Model: "test", Days: 30, Now: now})
	for _, want := range []string{
		`"lagmon",`,
		`{"kecha lagmon 45k", kindExpense, 45_000_00, "UZS", "ovqat", "", yesterday},`,
		"buzildi **0**",
		"yarim mln",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
}

func TestAgentNoFinish(t *testing.T) {
	talk := Response{Content: []Block{{Type: "text", Text: "Menimcha..."}}, StopReason: "end_turn"}
	llm := &script{steps: []Response{talk, talk}}
	a := &Agent{LLM: llm, Model: "test", MaxTurns: 2, Now: func() time.Time { return now }}
	if _, err := a.Run(context.Background(), fixture()); !errors.Is(err, ErrNoFinish) {
		t.Fatalf("want ErrNoFinish, got %v", err)
	}
	if got := llm.reqs[1].Messages[2].Content[0].Text; !strings.Contains(got, "evaluate") {
		t.Errorf("want a nudge, got %q", got)
	}
}

func TestMinorLiteral(t *testing.T) {
	for minor, want := range map[int64]string{35_000_00: "35_000_00", 12_50: "12_50", 1_500_000_00: "1_500_000_00", 500_00: "500_00"} {
		if got := minorLiteral(minor); got != want {
			t.Errorf("minorLiteral(%d) = %q, want %q", minor, got, want)
		}
	}
}

func TestAnthropicClient(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, `{"type":"error","error":{"type":"overloaded_error"}}`, 529)
			return
		}
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("request: %s %v", r.URL.Path, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		var req Request
		if err := json.Unmarshal(body, &req); err != nil || req.Model != "m" || len(req.Tools) != 3 {
			t.Errorf("body: %s", body)
		}
		io.WriteString(w, `{"content":[{"type":"tool_use","id":"t1","name":"parse","input":{"text":"osh 99k"}}],"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":2}}`)
	}))
	defer srv.Close()

	a := &Anthropic{APIKey: "k", BaseURL: srv.URL, Backoff: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := a.Send(ctx, Request{Model: "m", MaxTokens: 10, Tools: tools,
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || res.Content[0].Name != "parse" || res.Usage.InputTokens != 5 {
		t.Errorf("calls %d res %+v", calls, res)
	}
}

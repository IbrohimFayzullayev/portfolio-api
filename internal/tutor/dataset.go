// Package tutor is the finance bot's parser tutor: an agent that reads what
// the parser got wrong in real use and proposes the smallest catalog change
// that fixes it, then proves the change on everything people have written.
//
// The split of work is the whole design:
//
//   - The model only proposes. It sees masked text (no amounts, no names, no
//     user ids) and suggests stems for existing categories.
//   - Code decides. Every proposal is applied in memory and the real parser
//     is run over every labelled line, before and after. A proposal that
//     breaks one line people accepted is rejected, whatever the model says.
//   - A person merges. The output is a report with ready lines for
//     internal/finance/catalog.go and parse_test.go; nothing is written to
//     the repository or the database.
//
// Everything here is pure except the HTTP client in anthropic.go: rows in,
// report out. cmd/parsertutor does the reading.
package tutor

import (
	"sort"
	"strings"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/financetext"
)

// Where a sample comes from.
const (
	// The parser gave up ("Summani topolmadim"): parse_failures.
	SourceFailure = "failure"
	// Parsed, and its owner later edited the row: the stored row is what
	// they meant, a gold label.
	SourceCorrected = "corrected"
	// Parsed and left alone: assumed right. A silver label - it is how the
	// parser of that day read it and nobody objected.
	SourceAccepted = "accepted"
)

// An edit within this long after insert is the bot's own write, not a person.
// The same margin the admin page uses for "corrected".
const editMargin = 2 * time.Second

// EntryRow is one finance-bot entry as stored (source = 'bot').
type EntryRow struct {
	RawText     string
	Kind        string
	Category    string
	Person      string
	AmountMinor int64
	Currency    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// FailureRow is one parse_failures row.
type FailureRow struct {
	Text      string
	Reason    string
	CreatedAt time.Time
}

// Label is what a line should parse to. Category is empty when the label
// does not know it (see BuildSamples).
type Label struct {
	Kind        string
	Category    string
	Person      string
	AmountMinor int64
	Currency    string
}

type Sample struct {
	Text   string    // the real text; never leaves this process
	Masked string    // what the model may see
	Day    time.Time // the "today" the parser saw when it was written
	Source string
	Reason string // failures only: no_amount, no_person
	Want   *Label // nil for failures: nobody knows what they meant
}

// Fallback categories say "the parser did not know", not "the person chose
// this". A stored row still in one of them labels its kind but not its
// category, so a stem that moves it somewhere better is not a regression.
var fallbackCategory = map[string]bool{"boshqa": true, "kirim_boshqa": true}

// BuildSamples turns stored rows into labelled samples. Names that appear in
// any row's person field are masked in every sample's Masked text.
func BuildSamples(entries []EntryRow, failures []FailureRow) []Sample {
	masker := NewMasker(personsOf(entries))
	out := make([]Sample, 0, len(entries)+len(failures))

	for _, e := range entries {
		if strings.TrimSpace(e.RawText) == "" {
			continue
		}
		s := Sample{
			Text:   e.RawText,
			Masked: masker.Mask(e.RawText),
			Day:    financetext.DayStart(e.CreatedAt),
			Source: SourceAccepted,
			Want: &Label{
				Kind:        e.Kind,
				Category:    e.Category,
				Person:      e.Person,
				AmountMinor: e.AmountMinor,
				Currency:    e.Currency,
			},
		}
		if e.UpdatedAt.After(e.CreatedAt.Add(editMargin)) {
			s.Source = SourceCorrected
		}
		if s.Source == SourceAccepted && fallbackCategory[e.Category] {
			s.Want.Category = ""
		}
		out = append(out, s)
	}

	for _, f := range failures {
		if strings.TrimSpace(f.Text) == "" {
			continue
		}
		out = append(out, Sample{
			Text:   f.Text,
			Masked: masker.Mask(f.Text),
			Day:    financetext.DayStart(f.CreatedAt),
			Source: SourceFailure,
			Reason: f.Reason,
		})
	}
	return out
}

func personsOf(entries []EntryRow) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		for _, part := range strings.Fields(e.Person) {
			p := strings.ToLower(financetext.NormalizeWord(part))
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Outcome is one sample read by the parser as it is right now.
type Outcome struct {
	Got  financetext.Entry
	Err  error
	Diff []string // fields that differ from the label; empty when right
}

// Right: a labelled sample read as labelled, or a failure that now reads.
func (o Outcome) Right(s Sample) bool {
	if s.Want == nil {
		return o.Err == nil
	}
	return o.Err == nil && len(o.Diff) == 0
}

// Check runs the parser on one sample.
func Check(s Sample) Outcome {
	got, err := financetext.Parse(s.Text, s.Day)
	o := Outcome{Got: got, Err: err}
	if err != nil || s.Want == nil {
		return o
	}
	w := s.Want
	if got.Kind != w.Kind {
		o.Diff = append(o.Diff, "kind")
	}
	if w.Category != "" && got.Category != w.Category {
		o.Diff = append(o.Diff, "category")
	}
	if !strings.EqualFold(strings.TrimSpace(got.Person), strings.TrimSpace(w.Person)) {
		o.Diff = append(o.Diff, "person")
	}
	if got.AmountMinor != w.AmountMinor || got.Currency != w.Currency {
		o.Diff = append(o.Diff, "amount")
	}
	return o
}

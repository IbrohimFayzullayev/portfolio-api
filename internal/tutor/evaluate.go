package tutor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ibrohimcoder/portfolio-api/internal/finance"
	"github.com/ibrohimcoder/portfolio-api/internal/financetext"
)

// Change is one sample whose reading a proposal set changed. Only masked
// text is kept: a Change may be shown to the model.
type Change struct {
	Masked string `json:"text"`
	Source string `json:"source"`
	Before string `json:"before"`
	After  string `json:"after"`
	Want   string `json:"want,omitempty"`
}

type Result struct {
	Labelled   int `json:"labelled"`    // samples with a label
	RightNow   int `json:"right_now"`   // of those, read right without the proposals
	RightAfter int `json:"right_after"` // ... and with them
	Failures   int `json:"failures"`    // unreadable lines
	ReadAfter  int `json:"read_after"`  // of those, readable with the proposals

	Fixed     []Change `json:"fixed"`      // wrong before, right after
	Broken    []Change `json:"broken"`     // right before, wrong after: a regression
	Shifted   []Change `json:"shifted"`    // read differently, label does not decide
	NewlyRead []Change `json:"newly_read"` // failures that now parse: a person checks these
}

// Safe: nothing that was right is wrong now.
func (r Result) Safe() bool { return len(r.Broken) == 0 }

// Evaluate reads every sample with the catalog as it is and again with the
// proposals applied, and reports what moved.
func Evaluate(samples []Sample, props []Proposal) (Result, error) {
	props = dedupe(props)
	before := make([]Outcome, len(samples))
	for i, s := range samples {
		before[i] = Check(s)
	}

	restore, err := Apply(props)
	if err != nil {
		return Result{}, err
	}
	after := make([]Outcome, len(samples))
	for i, s := range samples {
		after[i] = Check(s)
	}
	restore()

	var r Result
	for i, s := range samples {
		b, a := before[i], after[i]
		if s.Want == nil {
			r.Failures++
			if a.Err == nil {
				r.ReadAfter++
				if b.Err != nil {
					r.NewlyRead = append(r.NewlyRead, change(s, b, a))
				}
			}
			continue
		}
		r.Labelled++
		bRight, aRight := b.Right(s), a.Right(s)
		if bRight {
			r.RightNow++
		}
		if aRight {
			r.RightAfter++
		}
		switch {
		case !bRight && aRight:
			r.Fixed = append(r.Fixed, change(s, b, a))
		case bRight && !aRight:
			r.Broken = append(r.Broken, change(s, b, a))
		case describe(b) != describe(a):
			r.Shifted = append(r.Shifted, change(s, b, a))
		}
	}
	return r, nil
}

func dedupe(props []Proposal) []Proposal {
	seen := map[string]bool{}
	out := make([]Proposal, 0, len(props))
	for _, p := range props {
		p.Stem = strings.TrimSpace(p.Stem)
		k := p.Type + "|" + p.Stem
		if !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return out
}

func change(s Sample, b, a Outcome) Change {
	c := Change{Masked: s.Masked, Source: s.Source, Before: describe(b), After: describe(a)}
	if s.Want != nil {
		c.Want = describeLabel(*s.Want)
	}
	return c
}

// describe: "expense/ovqat", "lent/qarz +person", "error: no amount".
// Amounts and names stay out: this text may reach the model.
func describe(o Outcome) string {
	if o.Err != nil {
		return "error: " + o.Err.Error()
	}
	return describeLabel(Label{Kind: o.Got.Kind, Category: o.Got.Category, Person: o.Got.Person})
}

func describeLabel(l Label) string {
	s := l.Kind + "/" + l.Category
	if l.Category == "" {
		s = l.Kind + "/?"
	}
	if l.Person != "" {
		s += " +person"
	}
	return s
}

/* ------------------------------ what to show ------------------------------ */

// Wrong lists labelled samples the parser reads wrong today, grouped by
// masked text with a count, most frequent first.
func Wrong(samples []Sample, limit int) []Counted {
	byText := map[string]*Counted{}
	for _, s := range samples {
		if s.Want == nil {
			continue
		}
		o := Check(s)
		if o.Right(s) {
			continue
		}
		key := s.Masked + "\x00" + describe(o)
		c, ok := byText[key]
		if !ok {
			c = &Counted{Text: s.Masked, Source: s.Source, Got: describe(o), Want: describeLabel(*s.Want)}
			byText[key] = c
		}
		c.Count++
		if s.Source == SourceCorrected {
			c.Source = SourceCorrected
		}
	}
	return top(byText, limit)
}

// Unreadable lists failures the parser still cannot read, grouped the same way.
func Unreadable(samples []Sample, limit int) []Counted {
	byText := map[string]*Counted{}
	for _, s := range samples {
		if s.Want != nil {
			continue
		}
		o := Check(s)
		if o.Err == nil {
			continue
		}
		c, ok := byText[s.Masked]
		if !ok {
			c = &Counted{Text: s.Masked, Source: s.Source, Got: describe(o)}
			byText[s.Masked] = c
		}
		c.Count++
	}
	return top(byText, limit)
}

// UnknownWords counts the words of lines that still land in "boshqa": the
// raw material for new stems. Numbers, known stems and very short words are
// left out. Words are counted once per line.
func UnknownWords(samples []Sample, limit int) []Counted {
	counts := map[string]*Counted{}
	for _, s := range samples {
		if s.Want == nil {
			continue
		}
		o := Check(s)
		if o.Err != nil || !finance.IsSpending(o.Got.Kind) || o.Got.Category != "boshqa" {
			continue
		}
		seen := map[string]bool{}
		for _, w := range strings.Fields(s.Masked) {
			n := financetext.NormalizeWord(w)
			if len([]rune(n)) < 3 || seen[n] || strings.ContainsAny(n, "0123456789") ||
				strings.HasPrefix(n, "ism") || financetext.CategoryOf(n) != "" {
				continue
			}
			seen[n] = true
			c, ok := counts[n]
			if !ok {
				c = &Counted{Text: n}
				counts[n] = c
			}
			c.Count++
		}
	}
	return top(counts, limit)
}

type Counted struct {
	Text   string `json:"text"`
	Count  int    `json:"count"`
	Source string `json:"source,omitempty"`
	Got    string `json:"got,omitempty"`
	Want   string `json:"want,omitempty"`
}

func top(m map[string]*Counted, limit int) []Counted {
	out := make([]Counted, 0, len(m))
	for _, c := range m {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Text < out[j].Text
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Summary is the one-line state of the parser on this data.
func Summary(samples []Sample) string {
	r, _ := Evaluate(samples, nil)
	return fmt.Sprintf("%d/%d labelled lines read right; %d unreadable lines, %d of them readable now",
		r.RightNow, r.Labelled, r.Failures, r.ReadAfter)
}

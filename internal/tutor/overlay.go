package tutor

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ibrohimcoder/portfolio-api/internal/finance"
	"github.com/ibrohimcoder/portfolio-api/internal/financetext"
)

// Proposal types the tutor can verify. Anything else (a new category, a
// word that is never a name, a grammar change) is a product or code decision
// and goes into the report's notes for a person to make.
const (
	ProposeStem       = "stem"        // a word for an existing spending category
	ProposeIncomeStem = "income_stem" // a word for an existing income category
)

type Proposal struct {
	Type     string `json:"type"`
	Category string `json:"category"`
	Stem     string `json:"stem"`
	Why      string `json:"why,omitempty"`
}

func (p Proposal) String() string { return fmt.Sprintf("%s → %s", p.Stem, p.Category) }

// Categories that take no stems: "qarz" is set by kind, "boshqa" and
// "kirim_boshqa" are where nothing matched, "oila" is reached through
// FamilyHomeStems only (see the comment on it in catalog.go).
var stemless = map[string]bool{"qarz": true, "boshqa": true, "kirim_boshqa": true, "oila": true}

// Validate checks one proposal against the catalog as it is now.
func (p Proposal) Validate() error {
	var list []finance.Category
	switch p.Type {
	case ProposeStem:
		list = finance.Categories
	case ProposeIncomeStem:
		list = finance.IncomeCategories
	default:
		return fmt.Errorf("unknown proposal type %q (only %q and %q)", p.Type, ProposeStem, ProposeIncomeStem)
	}
	if stemless[p.Category] {
		return fmt.Errorf("category %q takes no stems", p.Category)
	}
	found := false
	for _, c := range list {
		if c.Key == p.Category {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no %s category %q; new categories are a decision for a person, put it in notes", p.Type, p.Category)
	}

	stem := p.Stem
	if stem != strings.ToLower(financetext.NormalizeWord(stem)) || stem == "" {
		return fmt.Errorf("stem %q must be one lower-case word without punctuation", p.Stem)
	}
	if utf8.RuneCountInString(stem) < 3 {
		return fmt.Errorf("stem %q is too short: it would match too many words", stem)
	}
	for _, r := range stem {
		if !unicode.IsLetter(r) && r != '\'' && r != '-' {
			return fmt.Errorf("stem %q may hold only letters, ' and -", stem)
		}
	}
	for _, l := range [][]finance.Category{finance.Categories, finance.IncomeCategories} {
		for _, c := range l {
			for _, s := range c.Stems {
				if s == stem {
					return fmt.Errorf("stem %q is already in %q", stem, c.Key)
				}
			}
		}
	}
	return nil
}

// Apply puts the proposals into the catalog in memory and returns the
// function that takes them out again. Not safe for concurrent use: the
// parser reads package-level lists, and this swaps them.
func Apply(props []Proposal) (restore func(), err error) {
	for _, p := range props {
		if err := p.Validate(); err != nil {
			return func() {}, err
		}
	}
	oldSpend, oldIncome := finance.Categories, finance.IncomeCategories
	finance.Categories = withStems(oldSpend, props, ProposeStem)
	finance.IncomeCategories = withStems(oldIncome, props, ProposeIncomeStem)
	return func() {
		finance.Categories, finance.IncomeCategories = oldSpend, oldIncome
	}, nil
}

// withStems copies the list - never the caller's backing arrays - and adds
// the stems of one proposal type at the end of their category.
func withStems(list []finance.Category, props []Proposal, typ string) []finance.Category {
	out := make([]finance.Category, len(list))
	for i, c := range list {
		c.Stems = append([]string(nil), c.Stems...)
		for _, p := range props {
			if p.Type == typ && p.Category == c.Key {
				c.Stems = append(c.Stems, p.Stem)
			}
		}
		out[i] = c
	}
	return out
}

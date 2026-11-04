package tutor

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ibrohimcoder/portfolio-api/internal/financetext"
)

// Masker hides what the model has no need to see: every digit becomes 9
// (so "35k" is "99k" and "1.5mln" is "9.9mln" - the shape the parser cares
// about survives, the amount does not) and every known name becomes "Ism",
// keeping its case ending ("Aliga" -> "Ismga"), which is what decides the
// kind of a debt line.
//
// Masking is best effort and errs towards hiding: a word that merely starts
// with a known name is masked too.
type Masker struct {
	names []string // lower case, at least three letters
}

// Words that follow a name and are stored with it ("Ali aka") but are not
// names themselves; masking them would hide the grammar the parser reads.
var kinship = map[string]bool{
	"aka": true, "opa": true, "uka": true, "singil": true, "ota": true,
	"ona": true, "amaki": true, "tog'a": true, "xola": true, "amma": true,
	"bobo": true, "buvi": true, "domla": true, "ustoz": true,
}

func NewMasker(names []string) *Masker {
	m := &Masker{}
	for _, n := range names {
		n = strings.ToLower(financetext.NormalizeWord(n))
		if utf8.RuneCountInString(n) >= 3 && !kinship[n] {
			m.names = append(m.names, n)
		}
	}
	return m
}

func (m *Masker) Mask(text string) string {
	words := strings.Fields(text)
	for i, w := range words {
		words[i] = m.maskWord(maskDigits(w))
	}
	return strings.Join(words, " ")
}

func maskDigits(w string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return '9'
		}
		return r
	}, w)
}

func (m *Masker) maskWord(w string) string {
	lower := strings.ToLower(financetext.NormalizeWord(w))
	for _, n := range m.names {
		if !strings.HasPrefix(lower, n) {
			continue
		}
		suffix := lower[len(n):]
		r, _ := utf8.DecodeRuneInString(w)
		if unicode.IsUpper(r) {
			return "Ism" + suffix
		}
		return "ism" + suffix
	}
	// A name nobody stored yet - the failures are mostly debts whose name
	// was never saved: a capitalised word with a case ending that is not a
	// category word, which is how the parser itself spots a name.
	r, _ := utf8.DecodeRuneInString(w)
	if unicode.IsUpper(r) && financetext.CategoryOf(lower) == "" {
		for _, cut := range []func(string) (string, bool){financetext.DativeStem, financetext.AblativeStem} {
			if stem, ok := cut(lower); ok {
				return "Ism" + lower[len(stem):]
			}
		}
	}
	return w
}

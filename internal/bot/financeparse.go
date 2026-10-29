package bot

import (
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/finance"
	"github.com/ibrohimcoder/portfolio-api/internal/financetext"
)

// Reading one line of money talk.
//
// The parser itself lives in internal/financetext, shared with the standalone
// finance bot (internal/finbot) so both read "tushlik 35k" the same way. This
// file keeps the short local names the rest of this package was written
// against; it holds no logic of its own.

const (
	kindExpense   = finance.KindExpense
	kindGift      = finance.KindGift
	kindLent      = finance.KindLent
	kindRepaidIn  = finance.KindRepaidIn
	kindBorrowed  = finance.KindBorrowed
	kindRepaidOut = finance.KindRepaidOut
	kindIncome    = finance.KindIncome
)

func isDebtKind(kind string) bool { return finance.IsDebt(kind) }

// needsPerson: an entry of this kind means nothing without a name.
func needsPerson(kind string) bool { return finance.IsDebt(kind) }

func kindOf(key string) finance.Kind {
	k, _ := finance.KindOf(key)
	return k
}

func categoryByKey(key string) finance.Category { return finance.CategoryByKey(key) }

type parsedEntry struct {
	kind        string
	amountMinor int64
	currency    string
	category    string
	person      string
	note        string
	spentOn     time.Time
}

var (
	errNoAmount = financetext.ErrNoAmount
	errNoPerson = financetext.ErrNoPerson
)

func parseEntry(text string, today time.Time) (parsedEntry, error) {
	e, err := financetext.Parse(text, today)
	return parsedEntry{
		kind: e.Kind, amountMinor: e.AmountMinor, currency: e.Currency,
		category: e.Category, person: e.Person, note: e.Note, spentOn: e.SpentOn,
	}, err
}

func splitEntries(text string, today time.Time) []string { return financetext.Split(text, today) }

func normalizeWord(w string) string                 { return financetext.NormalizeWord(w) }
func cleanOriginal(w string) string                 { return financetext.CleanOriginal(w) }
func capitalise(s string) string                    { return financetext.Capitalise(s) }
func dativeStem(w string) (string, bool)            { return financetext.DativeStem(w) }
func ablativeStem(w string) (string, bool)          { return financetext.AblativeStem(w) }
func categoryOf(word string) string                 { return financetext.CategoryOf(word) }
func parseAmount(text string) (int64, string, bool) { return financetext.ParseAmount(text) }

func parseDay(text string, today time.Time) (time.Time, bool) {
	return financetext.ParseDay(text, today)
}

// guessCategory picks from the list that fits the kind, reading only the
// words at the given positions.
func guessCategory(kind string, norm []string, words []int) string {
	picked := make([]string, 0, len(words))
	for _, i := range words {
		picked = append(picked, norm[i])
	}
	return financetext.GuessCategory(kind, picked)
}

/* ------------------------------- formatting ------------------------------- */

// formatMoney: "35 000 so'm", "$12.50", "$1 200".
func formatMoney(minor int64, currency string) string {
	return financetext.FormatMoney(minor, currency)
}

// money is an amount per currency. Most people will only ever have so'm in
// it; a dollar loan should still add up without being converted at a rate
// nobody wrote down.
type money map[string]int64

func (m money) add(currency string, minor int64) { m[currency] += minor }

func (m money) isZero() bool { return financetext.Money(m).IsZero() }

func (m money) String() string { return financetext.Money(m).String() }

var uzMonths = financetext.UzMonths

func monthName(m time.Month) string { return financetext.MonthName(m) }

// dayLabel: "13.10 (bugun)", "12.10 (kecha)", "05.10".
func dayLabel(d, today time.Time) string { return financetext.DayLabel(d, today) }

func sameDay(a, b time.Time) bool { return financetext.SameDay(a, b) }

// dayStart is today's date at midnight in Tashkent — what spent_on means.
func dayStart(t time.Time) time.Time {
	t = t.In(tashkent)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, tashkent)
}

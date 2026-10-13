package finance

import (
	"sort"
	"strings"
	"time"
)

// Debts are not stored anywhere. A person's balance is the net of their rows,
// so a partial repayment is just another row and fixing a typo in any of them
// fixes the balance with nothing else to keep in sync.

// Row is one debt entry, as read from finance_entries.
type Row struct {
	Person   string
	Currency string
	Kind     string
	Minor    int64
	Day      time.Time
}

// Balance is where one person stands in one currency.
type Balance struct {
	Person   string `json:"person"`
	Currency string `json:"currency"`
	// Positive: they owe me. Negative: I owe them.
	Minor int64 `json:"amount_minor"`
	// When the balance last left zero (or changed sides) — how long this
	// debt has been open.
	Since time.Time `json:"since"`
}

// Signed is a row's effect on "they owe me".
func Signed(kind string, minor int64) int64 {
	switch kind {
	case KindLent, KindRepaidOut:
		return minor
	case KindRepaidIn, KindBorrowed:
		return -minor
	}
	return 0
}

// Fold walks the rows, which must be in date order, and returns the open
// balances, largest first. Names match case-insensitively; the latest
// spelling is the one shown.
func Fold(rows []Row) []Balance {
	type key struct{ person, currency string }
	state := map[key]*Balance{}
	var order []key

	for _, r := range rows {
		k := key{strings.ToLower(r.Person), r.Currency}
		bal, ok := state[k]
		if !ok {
			bal = &Balance{Person: r.Person, Currency: r.Currency}
			state[k] = bal
			order = append(order, k)
		}
		before := bal.Minor
		bal.Minor += Signed(r.Kind, r.Minor)
		// Opened, or flipped sides: the clock starts again.
		if before == 0 || (before > 0) != (bal.Minor > 0) {
			bal.Since = r.Day
		}
		bal.Person = r.Person
	}

	var out []Balance
	for _, k := range order {
		if bal := state[k]; bal.Minor != 0 {
			out = append(out, *bal)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return Abs(out[i].Minor) > Abs(out[j].Minor)
	})
	return out
}

// Amounts is a sum per currency, in minor units.
type Amounts map[string]int64

func (a Amounts) Add(currency string, minor int64) { a[currency] += minor }

// Totals splits balances into what I am owed and what I owe, both positive.
func Totals(balances []Balance) (owedToMe, iOwe Amounts, people int) {
	owedToMe, iOwe = Amounts{}, Amounts{}
	seen := map[string]bool{}
	for _, b := range balances {
		if b.Minor > 0 {
			owedToMe.Add(b.Currency, b.Minor)
		} else {
			iOwe.Add(b.Currency, -b.Minor)
		}
		seen[strings.ToLower(b.Person)] = true
	}
	return owedToMe, iOwe, len(seen)
}

func Abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

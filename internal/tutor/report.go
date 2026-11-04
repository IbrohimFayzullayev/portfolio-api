package tutor

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/financetext"
)

// ReportInput is everything the report shows. Final is empty in offline mode.
type ReportInput struct {
	Samples []Sample
	Final   Final
	Model   string
	Days    int
	Now     time.Time
	Offline bool
}

// Report renders the week's markdown: what to paste where, and why it is safe.
func Report(in ReportInput) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	f := in.Final
	base, _ := Evaluate(in.Samples, nil)

	w("# Parser o'qituvchisi: hisobot\n\n")
	w("%s · oxirgi %d kun", in.Now.In(financetext.Tashkent).Format("2006-01-02 15:04"), in.Days)
	if in.Offline {
		w(" · **offline** (modelsiz, faqat holat)")
	} else {
		w(" · model `%s`", in.Model)
	}
	w("\n\n## Holat\n\n")
	w("- Yorliqli qatorlar: **%d**, hozir to'g'ri o'qilyapti: **%d** (%s)\n", base.Labelled, base.RightNow, pct(base.RightNow, base.Labelled))
	w("- O'qilmagan xabarlar: **%d**, hozirgi parser ulardan **%d** tasini o'qiydi\n", base.Failures, base.ReadAfter)

	if !in.Offline {
		r := f.Result
		w("\n## Takliflar\n\n")
		if len(f.Proposals) == 0 {
			w("Bu hafta xavfsiz taklif yo'q.\n")
		} else {
			w("Takliflar bilan: to'g'ri o'qilgan **%d → %d**, tuzatildi **%d**, buzildi **%d**, o'qiladigan bo'ldi **%d**.\n\n",
				r.RightNow, r.RightAfter, len(r.Fixed), len(r.Broken), len(r.NewlyRead))
			w("| So'z (stem) | Kategoriya | Sabab |\n|---|---|---|\n")
			for _, p := range f.Proposals {
				w("| `%s` | %s | %s |\n", p.Stem, p.Category, cell(p.Why))
			}
			w("\n### Qo'llash\n\n")
			w("1. `internal/finance/catalog.go` dagi kategoriyalar oxiriga:\n\n```go\n%s```\n\n", catalogSnippet(f.Proposals))
			if len(f.Tests) > 0 {
				w("2. `internal/financetext/parse_test.go` dagi `TestParseEntry` jadvaliga:\n\n```go\n%s```\n\n", testSnippet(f.Tests, in.Now))
			}
			w("3. `go test ./internal/financetext/ ./internal/tutor/`, keyin commit.\n")
		}
		changes(&b, "Tuzatilgan qatorlar", r.Fixed, 20)
		changes(&b, "Boshqacha o'qiladigan qatorlar (ko'z bilan tekshiring)", r.Shifted, 10)
		changes(&b, "Endi o'qiladigan xabarlar (ko'z bilan tekshiring)", r.NewlyRead, 20)
		if len(f.Dropped) > 0 {
			w("\n### Tashlab yuborilgan test qatorlari\n\n")
			for _, t := range f.Dropped {
				w("- `%s` → %s: %s\n", t.Text, t.Category, t.Problem)
			}
		}
		if f.Notes != "" {
			w("\n## Modelning izohlari (qaror sizda)\n\n%s\n", f.Notes)
		}
	}

	counted(&b, "Hali noto'g'ri o'qilayotganlar", Wrong(in.Samples, 15))
	counted(&b, "Hali o'qilmayotganlar", Unreadable(in.Samples, 15))
	if in.Offline {
		counted(&b, "\"Boshqa\"ga tushayotgan so'zlar", UnknownWords(in.Samples, 25))
	}

	if !in.Offline {
		w("\n---\n%d qadam · %d kirish + %d chiqish token\n", f.Turns, f.Usage.InputTokens, f.Usage.OutputTokens)
	}
	w("\nMatnlar niqoblangan: raqamlar `9`, ismlar `Ism`.\n")
	return b.String()
}

func changes(b *strings.Builder, title string, cs []Change, limit int) {
	if len(cs) == 0 {
		return
	}
	fmt.Fprintf(b, "\n### %s (%d)\n\n", title, len(cs))
	for i, c := range cs {
		if i == limit {
			fmt.Fprintf(b, "- … yana %d ta\n", len(cs)-limit)
			break
		}
		fmt.Fprintf(b, "- `%s`: %s → **%s**", c.Masked, c.Before, c.After)
		if c.Want != "" {
			fmt.Fprintf(b, " (kerak: %s)", c.Want)
		}
		b.WriteString("\n")
	}
}

func counted(b *strings.Builder, title string, cs []Counted) {
	if len(cs) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## %s\n\n", title)
	for _, c := range cs {
		fmt.Fprintf(b, "- `%s` ×%d", c.Text, c.Count)
		if c.Got != "" {
			fmt.Fprintf(b, ": %s", c.Got)
		}
		if c.Want != "" {
			fmt.Fprintf(b, " (saqlangan: %s, %s)", c.Want, c.Source)
		}
		b.WriteString("\n")
	}
}

// catalogSnippet groups the stems by category, in catalog order.
func catalogSnippet(props []Proposal) string {
	var b strings.Builder
	order := []string{}
	byCat := map[string][]string{}
	for _, p := range props {
		k := p.Category
		if p.Type == ProposeIncomeStem {
			k = "income:" + k
		}
		if _, ok := byCat[k]; !ok {
			order = append(order, k)
		}
		byCat[k] = append(byCat[k], strconv.Quote(p.Stem))
	}
	for _, k := range order {
		if strings.HasPrefix(k, "income:") {
			fmt.Fprintf(&b, "// IncomeCategories, %q:\n", strings.TrimPrefix(k, "income:"))
		} else {
			fmt.Fprintf(&b, "// Categories, %q:\n", k)
		}
		fmt.Fprintf(&b, "%s,\n", strings.Join(byCat[k], ", "))
	}
	return b.String()
}

var kindConst = map[string]string{
	"expense": "kindExpense", "gift": "kindGift", "lent": "kindLent",
	"repaid_in": "kindRepaidIn", "borrowed": "kindBorrowed",
	"repaid_out": "kindRepaidOut", "income": "kindIncome",
}

// testSnippet writes rows in TestParseEntry's shape. The table's `today` is
// fixed, so a line is written with `today` or `yesterday` by how it parsed.
func testSnippet(tests []TestCase, now time.Time) string {
	var b strings.Builder
	today := financetext.DayStart(now)
	for _, t := range tests {
		g := t.Got
		day := "today"
		switch {
		case financetext.SameDay(g.SpentOn, today):
		case financetext.SameDay(g.SpentOn, today.AddDate(0, 0, -1)):
			day = "yesterday"
		default:
			fmt.Fprintf(&b, "// %q: date is not today or yesterday, write it by hand\n", t.Text)
			continue
		}
		fmt.Fprintf(&b, "{%q, %s, %s, %q, %q, %q, %s},\n",
			t.Text, kindConst[g.Kind], minorLiteral(g.AmountMinor), g.Currency, g.Category, g.Person, day)
	}
	return b.String()
}

// minorLiteral: 3500000 → 35_000_00, as the test table writes amounts.
func minorLiteral(minor int64) string {
	whole := strings.ReplaceAll(financetext.GroupThousands(minor/100), " ", "_")
	return fmt.Sprintf("%s_%02d", whole, minor%100)
}

func pct(a, b int) string {
	if b == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", float64(a)*100/float64(b))
}

func cell(s string) string {
	return strings.NewReplacer("|", "/", "\n", " ").Replace(s)
}

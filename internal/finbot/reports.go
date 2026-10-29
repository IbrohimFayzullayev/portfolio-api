package finbot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ibrohimcoder/portfolio-api/internal/finance"
	"github.com/ibrohimcoder/portfolio-api/internal/financetext"
)

// Reports in the chat: /hisob, /qarzlar, /balans, /oxirgi and the morning
// summary. The same arithmetic as the ops bot's money reports, scoped to one
// user. The charts live in the Mini App (web.go).

var tashkent = financetext.Tashkent

/* -------------------------------- /oxirgi --------------------------------- */

func (b *Bot) recentList(ctx context.Context, u *user) (string, *inlineKeyboard) {
	rows, err := b.pool.Query(ctx, `
		SELECT id::text, kind, amount_minor, currency, category, person, note, spent_on
		FROM entries
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT 10`, u.ID)
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err)), nil
	}
	defer rows.Close()

	var list []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.kind, &e.amountMinor, &e.currency, &e.category,
			&e.person, &e.note, &e.spentOn); err != nil {
			return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err)), nil
		}
		list = append(list, e)
	}
	if err := rows.Err(); err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err)), nil
	}
	if len(list) == 0 {
		return "Hali hech narsa yozilmagan.\n\nShunchaki yozing: <code>tushlik 35k</code>", nil
	}

	var sb strings.Builder
	sb.WriteString("📋 <b>Oxirgi yozuvlar</b>\n\n")
	var buttons [][]inlineButton
	var row []inlineButton
	for i, e := range list {
		label := firstNonEmpty(e.note, e.person, categoryByKey(e.category).Label)
		if e.person != "" && finance.IsDebt(e.kind) {
			label = e.person
		}
		sb.WriteString(fmt.Sprintf("%d. %s %s <b>%s</b> - %s\n",
			i+1, e.spentOn.Format("02.01"), kindOf(e.kind).Emoji,
			formatMoney(e.amountMinor, e.currency), escapeHTML(truncate(label, 40))))
		row = append(row, inlineButton{Text: fmt.Sprintf("%d", i+1), CallbackData: "fn:card:" + e.id})
		if len(row) == 5 {
			buttons = append(buttons, row)
			row = nil
		}
	}
	if len(row) > 0 {
		buttons = append(buttons, row)
	}
	sb.WriteString("\n<i>Tuzatish yoki o'chirish uchun raqamni bosing.</i>")
	return sb.String(), &inlineKeyboard{InlineKeyboard: buttons}
}

/* --------------------------------- sums ----------------------------------- */

// spentBetween: expenses and gifts on days in [from, to).
func (b *Bot) spentBetween(ctx context.Context, u *user, from, to time.Time) (money, int64, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT currency, count(*), COALESCE(sum(amount_minor), 0)::bigint
		FROM entries
		WHERE user_id = $1 AND kind IN ('expense', 'gift')
		  AND spent_on >= $2::date AND spent_on < $3::date
		GROUP BY currency`, u.ID, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	m := money{}
	var count int64
	for rows.Next() {
		var cur string
		var n, sum int64
		if err := rows.Scan(&cur, &n, &sum); err != nil {
			return nil, 0, err
		}
		m.Add(cur, sum)
		count += n
	}
	return m, count, rows.Err()
}

func (b *Bot) incomeBetween(ctx context.Context, u *user, from, to time.Time) (money, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT currency, COALESCE(sum(amount_minor), 0)::bigint
		FROM entries
		WHERE user_id = $1 AND kind = 'income' AND spent_on >= $2::date AND spent_on < $3::date
		GROUP BY currency`, u.ID, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := money{}
	for rows.Next() {
		var cur string
		var sum int64
		if err := rows.Scan(&cur, &sum); err != nil {
			return nil, err
		}
		m.Add(cur, sum)
	}
	return m, rows.Err()
}

/* ------------------------------- debts ------------------------------------ */

func debtBalances(ctx context.Context, q pgxQuerier, userID int64, person string) ([]finance.Balance, error) {
	rows, err := q.Query(ctx, `
		SELECT person, currency, kind, amount_minor, spent_on
		FROM entries
		WHERE user_id = $1
		  AND kind IN ('lent', 'repaid_in', 'borrowed', 'repaid_out')
		  AND person <> ''
		  AND ($2 = '' OR lower(person) = lower($2))
		ORDER BY spent_on, created_at`, userID, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []finance.Row
	for rows.Next() {
		var r finance.Row
		if err := rows.Scan(&r.Person, &r.Currency, &r.Kind, &r.Minor, &r.Day); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return finance.Fold(all), nil
}

func (b *Bot) personBalanceLine(ctx context.Context, u *user, person string) string {
	balances, err := debtBalances(ctx, b.pool, u.ID, person)
	if err != nil {
		return ""
	}
	owed, owe, _ := finance.Totals(balances)
	owedToMe, iOwe := money(owed), money(owe)
	name := escapeHTML(person)
	switch {
	case owedToMe.IsZero() && iOwe.IsZero():
		return "📊 " + name + " bilan hisob teng"
	case iOwe.IsZero():
		return fmt.Sprintf("📊 %s sizga qarzdor: <b>%s</b>", name, owedToMe)
	case owedToMe.IsZero():
		return fmt.Sprintf("📊 Siz qarzdorsiz (%s): <b>%s</b>", name, iOwe)
	default:
		return fmt.Sprintf("📊 %s sizga: %s · siz unga: %s", name, owedToMe, iOwe)
	}
}

func (b *Bot) debtsReport(ctx context.Context, u *user, arg string, today time.Time) string {
	if arg = strings.TrimSpace(arg); arg != "" {
		return b.personHistory(ctx, u, arg)
	}
	balances, err := debtBalances(ctx, b.pool, u.ID, "")
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}
	if len(balances) == 0 {
		return "🤝 Ochiq qarz yo'q.\n\n<i>Yozish: <code>Aliga qarz 500k</code>, <code>Ali qaytardi 200k</code></i>"
	}
	owed, owe, _ := finance.Totals(balances)

	var sb strings.Builder
	sb.WriteString("🤝 <b>Qarzlar</b>\n")
	section := func(title string, total money, positive bool) {
		if total.IsZero() {
			return
		}
		sb.WriteString(fmt.Sprintf("\n<b>%s</b> · %s\n", title, total))
		for _, d := range balances {
			if (d.Minor > 0) != positive {
				continue
			}
			age := "bugundan"
			if days := int(today.Sub(d.Since).Hours()/24 + 0.5); days > 0 {
				age = fmt.Sprintf("%s dan, %d kun", d.Since.Format("02.01"), days)
			}
			sb.WriteString(fmt.Sprintf("• %s - %s · %s\n",
				escapeHTML(d.Person), formatMoney(finance.Abs(d.Minor), d.Currency), age))
		}
	}
	section("Sizga qarzdor", money(owed), true)
	section("Siz qarzdorsiz", money(owe), false)
	sb.WriteString("\n<i>Bitta odam tarixi: <code>/qarzlar Ali</code></i>")
	return sb.String()
}

// personHistory: every row with one person - loans and gifts alike.
func (b *Bot) personHistory(ctx context.Context, u *user, name string) string {
	name = financetext.CleanOriginal(name)
	candidates := []string{name}
	if s, ok := financetext.DativeStem(name); ok {
		candidates = append(candidates, s)
	}
	if s, ok := financetext.AblativeStem(name); ok {
		candidates = append(candidates, s)
	}

	for _, who := range candidates {
		rows, err := b.pool.Query(ctx, `
			SELECT kind, amount_minor, currency, note, spent_on, person
			FROM entries
			WHERE user_id = $1 AND lower(person) = lower($2)
			ORDER BY spent_on, created_at`, u.ID, who)
		if err != nil {
			return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
		}
		var (
			lines   []string
			display string
			given   = money{}
		)
		for rows.Next() {
			var kind, currency, note, person string
			var minor int64
			var day time.Time
			if err := rows.Scan(&kind, &minor, &currency, &note, &day, &person); err != nil {
				rows.Close()
				return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
			}
			display = person
			if kind == finance.KindGift {
				given.Add(currency, minor)
			}
			line := fmt.Sprintf("%s %s %s", day.Format("02.01.06"), kindOf(kind).Emoji, formatMoney(minor, currency))
			if note != "" {
				line += " - " + escapeHTML(truncate(note, 40))
			}
			lines = append(lines, line)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
		}
		if len(lines) == 0 {
			continue
		}
		if len(lines) > 25 {
			lines = append([]string{fmt.Sprintf("<i>… yana %d ta oldinroq</i>", len(lines)-25)},
				lines[len(lines)-25:]...)
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("👤 <b>%s</b>\n\n", escapeHTML(display)))
		sb.WriteString(strings.Join(lines, "\n"))
		sb.WriteString("\n\n" + b.personBalanceLine(ctx, u, display))
		if !given.IsZero() {
			sb.WriteString(fmt.Sprintf("\n🎁 Jami berilgan (qaytmaydigan): %s", given))
		}
		return sb.String()
	}
	return fmt.Sprintf("👤 <b>%s</b> bo'yicha yozuv yo'q.\n\n/qarzlar - hamma ochiq qarzlar", escapeHTML(name))
}

/* --------------------------------- /hisob --------------------------------- */

type period struct {
	label    string
	from, to time.Time // dates, to exclusive
	compare  *period
}

func (p period) days() int { return int(p.to.Sub(p.from).Hours()/24 + 0.5) }

// reportPeriod reads the /hisob argument. Unknown words fall back to this
// month, which is what the command means with no argument at all.
func reportPeriod(arg string, today time.Time) period {
	arg = financetext.NormalizeWord(strings.Join(strings.Fields(arg), " "))
	tomorrow := today.AddDate(0, 0, 1)
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())

	switch arg {
	case "bugun", "kun":
		return period{label: "Bugun · " + today.Format("02.01"), from: today, to: tomorrow}
	case "kecha":
		y := today.AddDate(0, 0, -1)
		return period{label: "Kecha · " + y.Format("02.01"), from: y, to: today}
	case "hafta":
		from := today.AddDate(0, 0, -6)
		p := period{
			label: fmt.Sprintf("Oxirgi 7 kun · %s-%s", from.Format("02.01"), today.Format("02.01")),
			from:  from, to: tomorrow,
		}
		p.compare = &period{from: from.AddDate(0, 0, -7), to: from}
		return p
	case "yil":
		return period{
			label: fmt.Sprintf("%d yil", today.Year()),
			from:  time.Date(today.Year(), 1, 1, 0, 0, 0, 0, today.Location()), to: tomorrow,
		}
	case "o'tgan", "o'tgan oy", "otgan":
		from := monthStart.AddDate(0, -1, 0)
		p := period{label: fmt.Sprintf("%s %d", financetext.MonthName(from.Month()), from.Year()), from: from, to: monthStart}
		p.compare = &period{from: from.AddDate(0, -1, 0), to: from}
		return p
	}

	if len(arg) >= 3 {
		for i, name := range financetext.UzMonths {
			if strings.HasPrefix(strings.ToLower(name), arg) {
				from := time.Date(today.Year(), time.Month(i+1), 1, 0, 0, 0, 0, today.Location())
				if from.After(today) {
					from = from.AddDate(-1, 0, 0)
				}
				to := from.AddDate(0, 1, 0)
				if to.After(tomorrow) {
					to = tomorrow
				}
				return period{label: fmt.Sprintf("%s %d", financetext.MonthName(from.Month()), from.Year()), from: from, to: to}
			}
		}
	}

	// This month so far, against the same days of last month.
	p := period{
		label: fmt.Sprintf("%s %d · 1-%d", financetext.MonthName(today.Month()), today.Year(), today.Day()),
		from:  monthStart, to: tomorrow,
	}
	prevFrom := monthStart.AddDate(0, -1, 0)
	prevTo := prevFrom.AddDate(0, 0, today.Day())
	if prevTo.After(monthStart) {
		prevTo = monthStart
	}
	p.compare = &period{from: prevFrom, to: prevTo}
	return p
}

func delta(current, previous int64) string {
	if previous <= 0 {
		return ""
	}
	change := float64(current-previous) / float64(previous) * 100
	switch {
	case change >= 0.5:
		return fmt.Sprintf(" <i>(+%.0f%%)</i>", change)
	case change <= -0.5:
		return fmt.Sprintf(" <i>(%.0f%%)</i>", change)
	default:
		return " <i>(≈)</i>"
	}
}

func (b *Bot) financeReport(ctx context.Context, u *user, arg string, today time.Time) string {
	p := reportPeriod(arg, today)
	from, to := p.from.Format("2006-01-02"), p.to.Format("2006-01-02")

	rows, err := b.pool.Query(ctx, `
		SELECT kind, currency, count(*), sum(amount_minor)::bigint
		FROM entries
		WHERE user_id = $1 AND spent_on >= $2::date AND spent_on < $3::date
		GROUP BY kind, currency`, u.ID, from, to)
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}
	byKind := map[string]money{}
	counts := map[string]int64{}
	for rows.Next() {
		var kind, cur string
		var n, sum int64
		if err := rows.Scan(&kind, &cur, &n, &sum); err != nil {
			rows.Close()
			return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
		}
		if byKind[kind] == nil {
			byKind[kind] = money{}
		}
		byKind[kind].Add(cur, sum)
		counts[kind] += n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 <b>%s</b>\n", escapeHTML(p.label)))
	if len(counts) == 0 {
		sb.WriteString("\nBu davrda hech narsa yozilmagan.")
		sb.WriteString(b.footer(ctx, u))
		return sb.String()
	}

	spent := money{}
	for _, k := range []string{finance.KindExpense, finance.KindGift} {
		for cur, v := range byKind[k] {
			spent.Add(cur, v)
		}
	}

	income := byKind[finance.KindIncome]
	if income != nil {
		sb.WriteString(fmt.Sprintf("\n💰 <b>Kirim: %s</b>\n", income))
		sb.WriteString(b.incomeSources(ctx, u, from, to))
	}

	sb.WriteString(fmt.Sprintf("\n💸 <b>Sarflandi: %s</b>\n", spent))
	if v := byKind[finance.KindExpense]; v != nil {
		sb.WriteString(fmt.Sprintf("Xarajat - %s · %d ta\n", v, counts[finance.KindExpense]))
	}
	if v := byKind[finance.KindGift]; v != nil {
		sb.WriteString(fmt.Sprintf("Berildi - %s · %d ta\n", v, counts[finance.KindGift]))
	}
	if days := p.days(); days > 1 && !spent.IsZero() {
		avg := money{}
		for cur, v := range spent {
			per := v / int64(days)
			if cur == "UZS" {
				per = per / 100 * 100
			}
			avg.Add(cur, per)
		}
		sb.WriteString(fmt.Sprintf("Kuniga o'rtacha - %s\n", avg))
	}
	var prevByCat map[string]int64
	if p.compare != nil {
		if prev, _, err := b.spentBetween(ctx, u, p.compare.from, p.compare.to); err == nil && !prev.IsZero() {
			sb.WriteString(fmt.Sprintf("Oldingi shuncha davrda - %s%s\n", prev, delta(spent["UZS"], prev["UZS"])))
			prevByCat = b.spentByCategory(ctx, u, p.compare.from.Format("2006-01-02"), p.compare.to.Format("2006-01-02"))
		}
	}

	if income != nil {
		sb.WriteString(fmt.Sprintf("\n🟰 <b>Qoldi: %s</b>\n", netLine(income, spent)))
	}

	sb.WriteString(b.categorySection(ctx, u, from, to, spent["UZS"], prevByCat))
	sb.WriteString(b.giftsSection(ctx, u, from, to))
	sb.WriteString(b.largestSection(ctx, u, from, to))

	var loanLines []string
	for _, k := range []string{finance.KindLent, finance.KindRepaidIn, finance.KindBorrowed, finance.KindRepaidOut} {
		if v := byKind[k]; v != nil {
			loanLines = append(loanLines, fmt.Sprintf("%s %s - %s", kindOf(k).Emoji, kindOf(k).Label, v))
		}
	}
	if len(loanLines) > 0 {
		sb.WriteString("\n<b>Qarz harakati</b>\n" + strings.Join(loanLines, "\n") + "\n")
	}
	sb.WriteString(b.footer(ctx, u))
	return sb.String()
}

func (b *Bot) spentByCategory(ctx context.Context, u *user, from, to string) map[string]int64 {
	rows, err := b.pool.Query(ctx, `
		SELECT category, sum(amount_minor)::bigint
		FROM entries
		WHERE user_id = $1 AND kind IN ('expense', 'gift') AND currency = 'UZS'
		  AND spent_on >= $2::date AND spent_on < $3::date
		GROUP BY category`, u.ID, from, to)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var cat string
		var sum int64
		if rows.Scan(&cat, &sum) == nil {
			out[cat] = sum
		}
	}
	return out
}

// categorySection: where the money went, largest first. With a previous
// period, each line also says how it moved - the part of a report that is
// an analysis rather than a sum.
func (b *Bot) categorySection(ctx context.Context, u *user, from, to string, totalUZS int64, prev map[string]int64) string {
	rows, err := b.pool.Query(ctx, `
		SELECT category, currency, sum(amount_minor)::bigint
		FROM entries
		WHERE user_id = $1 AND kind IN ('expense', 'gift')
		  AND spent_on >= $2::date AND spent_on < $3::date
		GROUP BY category, currency`, u.ID, from, to)
	if err != nil {
		return ""
	}
	defer rows.Close()

	perCat := map[string]money{}
	var order []string
	for rows.Next() {
		var cat, cur string
		var sum int64
		if err := rows.Scan(&cat, &cur, &sum); err != nil {
			return ""
		}
		if perCat[cat] == nil {
			perCat[cat] = money{}
			order = append(order, cat)
		}
		perCat[cat].Add(cur, sum)
	}
	if len(order) == 0 {
		return ""
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, c := perCat[order[i]], perCat[order[j]]
		if a["UZS"] != c["UZS"] {
			return a["UZS"] > c["UZS"]
		}
		return a["USD"] > c["USD"]
	})

	var sb strings.Builder
	sb.WriteString("\n<b>Kategoriyalar</b>\n")
	for _, cat := range order {
		c := categoryByKey(cat)
		m := perCat[cat]
		line := fmt.Sprintf("%s %s - %s", c.Emoji, c.Label, m)
		if totalUZS > 0 && m["UZS"] > 0 {
			line += fmt.Sprintf(" · %.0f%%", float64(m["UZS"])/float64(totalUZS)*100)
		}
		if prev != nil {
			line += delta(m["UZS"], prev[cat])
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

func (b *Bot) giftsSection(ctx context.Context, u *user, from, to string) string {
	rows, err := b.pool.Query(ctx, `
		SELECT COALESCE(NULLIF((array_agg(person ORDER BY created_at DESC))[1], ''), '-'),
		       currency, sum(amount_minor)::bigint, count(*)
		FROM entries
		WHERE user_id = $1 AND kind = 'gift'
		  AND spent_on >= $2::date AND spent_on < $3::date
		GROUP BY lower(person), currency
		ORDER BY 3 DESC
		LIMIT 10`, u.ID, from, to)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var person, cur string
		var sum, n int64
		if err := rows.Scan(&person, &cur, &sum, &n); err != nil {
			return ""
		}
		line := fmt.Sprintf("%s - %s", escapeHTML(person), formatMoney(sum, cur))
		if n > 1 {
			line += fmt.Sprintf(" · %d marta", n)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n<b>Kimga berildi</b>\n" + strings.Join(lines, "\n") + "\n"
}

func (b *Bot) largestSection(ctx context.Context, u *user, from, to string) string {
	rows, err := b.pool.Query(ctx, `
		SELECT spent_on, amount_minor, currency, note, category
		FROM entries
		WHERE user_id = $1 AND kind IN ('expense', 'gift')
		  AND spent_on >= $2::date AND spent_on < $3::date
		ORDER BY (currency = 'UZS') DESC, amount_minor DESC
		LIMIT 3`, u.ID, from, to)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var day time.Time
		var minor int64
		var cur, note, cat string
		if err := rows.Scan(&day, &minor, &cur, &note, &cat); err != nil {
			return ""
		}
		lines = append(lines, fmt.Sprintf("%s · %s - %s", day.Format("02.01"),
			formatMoney(minor, cur), escapeHTML(truncate(firstNonEmpty(note, categoryByKey(cat).Label), 40))))
	}
	if len(lines) < 2 {
		return ""
	}
	return "\n<b>Eng kattalari</b>\n" + strings.Join(lines, "\n") + "\n"
}

func (b *Bot) incomeSources(ctx context.Context, u *user, from, to string) string {
	rows, err := b.pool.Query(ctx, `
		SELECT category, currency, sum(amount_minor)::bigint
		FROM entries
		WHERE user_id = $1 AND kind = 'income' AND spent_on >= $2::date AND spent_on < $3::date
		GROUP BY category, currency
		ORDER BY 3 DESC`, u.ID, from, to)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var cat, cur string
		var sum int64
		if err := rows.Scan(&cat, &cur, &sum); err != nil {
			return ""
		}
		c := categoryByKey(cat)
		lines = append(lines, fmt.Sprintf("%s %s - %s", c.Emoji, c.Label, formatMoney(sum, cur)))
	}
	if len(lines) < 2 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// footer closes a report with where things stand now: money in hand and
// open debts.
func (b *Bot) footer(ctx context.Context, u *user) string {
	var sb strings.Builder
	if states, err := b.balances(ctx, u); err == nil && len(states) > 0 {
		inHand := money{}
		for _, st := range states {
			inHand.Add(st.currency, st.minor)
		}
		sb.WriteString(fmt.Sprintf("\n💼 Balans: %s - /balans", inHand))
	}
	if balances, err := debtBalances(ctx, b.pool, u.ID, ""); err == nil && len(balances) > 0 {
		owed, owe, _ := finance.Totals(balances)
		sb.WriteString(fmt.Sprintf("\n🤝 Sizga qarzdor: %s · Siz: %s - /qarzlar", money(owed), money(owe)))
	}
	return sb.String()
}

// netLine: "+7 370 000 so'm (74%)" - income minus spending, and the share of
// income kept.
func netLine(income, spent money) string {
	net := money{}
	for cur, v := range income {
		net.Add(cur, v)
	}
	for cur, v := range spent {
		net.Add(cur, -v)
	}
	parts := make([]string, 0, 2)
	for _, cur := range []string{"UZS", "USD"} {
		v, ok := net[cur]
		if !ok {
			continue
		}
		s := formatMoney(v, cur)
		if v > 0 {
			s = "+" + s
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "0 so'm"
	}
	out := strings.Join(parts, " · ")
	if inc := income["UZS"]; inc > 0 {
		out += fmt.Sprintf(" (%d%%)", int(float64(net["UZS"])/float64(inc)*100))
	}
	return out
}

// monthProjection: where the month ends if spending keeps its pace.
func monthProjection(today time.Time, income, spent money) (int64, bool) {
	elapsed := today.Day() - 1
	if elapsed < 5 || income["UZS"] == 0 {
		return 0, false
	}
	daysInMonth := time.Date(today.Year(), today.Month()+1, 0, 0, 0, 0, 0, today.Location()).Day()
	perDay := spent["UZS"] / int64(elapsed)
	remaining := int64(daysInMonth - elapsed)
	// Whole so'm: a projection with tiyin in it reads as false precision.
	return (income["UZS"] - spent["UZS"] - perDay*remaining) / 100 * 100, true
}

/* -------------------------------- balance --------------------------------- */

type balanceState struct {
	currency   string
	checkpoint *finance.Checkpoint
	minor      int64
	in, out    int64
}

type pgxQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func latestCheckpoint(ctx context.Context, q pgxQuerier, userID int64, currency string) (*finance.Checkpoint, error) {
	var cp finance.Checkpoint
	err := q.QueryRow(ctx, `
		SELECT id::text, currency, amount_minor, taken_at, computed_minor, note
		FROM checkpoints
		WHERE user_id = $1 AND currency = $2
		ORDER BY taken_at DESC, created_at DESC
		LIMIT 1`, userID, currency).Scan(&cp.ID, &cp.Currency, &cp.Minor, &cp.TakenAt, &cp.Computed, &cp.Note)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cp, nil
}

// loadFlows reads every row from a day on; Covers decides exactly which
// count after a checkpoint.
func loadFlows(ctx context.Context, q pgxQuerier, userID int64, currency string, from time.Time) ([]finance.Flow, error) {
	rows, err := q.Query(ctx, `
		SELECT currency, kind, amount_minor, spent_on, created_at
		FROM entries
		WHERE user_id = $1 AND ($2 = '' OR currency = $2) AND spent_on >= $3::date`,
		userID, currency, from.In(tashkent).Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []finance.Flow
	for rows.Next() {
		var f finance.Flow
		if err := rows.Scan(&f.Currency, &f.Kind, &f.Minor, &f.Day, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (b *Bot) balanceNow(ctx context.Context, u *user, currency string) (balanceState, error) {
	st := balanceState{currency: currency}
	cp, err := latestCheckpoint(ctx, b.pool, u.ID, currency)
	if err != nil || cp == nil {
		return st, err
	}
	flows, err := loadFlows(ctx, b.pool, u.ID, currency, cp.TakenAt)
	if err != nil {
		return st, err
	}
	st.checkpoint = cp
	st.minor, st.in, st.out = finance.Since(*cp, flows, tashkent)
	return st, nil
}

func (b *Bot) balances(ctx context.Context, u *user) ([]balanceState, error) {
	var out []balanceState
	for _, cur := range []string{"UZS", "USD"} {
		st, err := b.balanceNow(ctx, u, cur)
		if err != nil {
			return nil, err
		}
		if st.checkpoint != nil {
			out = append(out, st)
		}
	}
	return out, nil
}

// balanceCommand is /balans: with no argument a report, with an amount a
// recount ("this is what is really there now").
func (b *Bot) balanceCommand(ctx context.Context, u *user, arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return b.balanceReport(ctx, u)
	}
	negative := strings.HasPrefix(arg, "-")
	minor, currency, ok := financetext.ParseAmount(strings.TrimLeft(arg, "-"))
	if !ok {
		return "Summani tushunmadim. Masalan: <code>/balans 5mln</code>, " +
			"<code>/balans 4 700 000</code>, <code>/balans $300</code>"
	}
	if negative {
		minor = -minor
	}
	before, err := b.balanceNow(ctx, u, currency)
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}
	var computed *int64
	if before.checkpoint != nil {
		v := before.minor
		computed = &v
	}
	if _, err := b.pool.Exec(ctx, `
		INSERT INTO checkpoints (user_id, currency, amount_minor, taken_at, computed_minor, source)
		VALUES ($1, $2, $3, now(), $4, 'bot')`, u.ID, currency, minor, computed); err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✅ <b>Balans yozildi: %s</b>\n", formatMoney(minor, currency)))
	if computed == nil {
		sb.WriteString("\nBoshlang'ich summa. Endi har kirim va xarajat shundan qo'shilib-ayrilib boradi.")
		sb.WriteString("\n<i>Vaqti-vaqti bilan haqiqiy summani shu buyruq bilan yozib turing.</i>")
		return sb.String()
	}
	diff := minor - *computed
	sb.WriteString(fmt.Sprintf("Hisob bo'yicha edi: %s\n", formatMoney(*computed, currency)))
	since := before.checkpoint.TakenAt.In(tashkent).Format("02.01")
	switch {
	case diff == 0:
		sb.WriteString("\nHisob bilan to'liq mos keldi 👌")
	case diff < 0:
		sb.WriteString(fmt.Sprintf("\nFarq: <b>%s</b> - %s dan beri yozilmay qolgan xarajat.",
			formatMoney(diff, currency), since))
	default:
		sb.WriteString(fmt.Sprintf("\nFarq: <b>+%s</b> - %s dan beri yozilmagan kirim.",
			formatMoney(diff, currency), since))
	}
	return sb.String()
}

func (b *Bot) balanceReport(ctx context.Context, u *user) string {
	states, err := b.balances(ctx, u)
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}
	if len(states) == 0 {
		return strings.Join([]string{
			"💼 <b>Balans</b>",
			"",
			"Hali boshlang'ich summa yozilmagan. Hozir qo'lingizda (karta + naqd) qancha bor?",
			"",
			"<code>/balans 5mln</code>",
			"<code>/balans $300</code> - dollar alohida",
			"",
			"Shundan keyin har kirim (<code>+10mln oylik</code>) qo'shiladi, har xarajat ayriladi.",
		}, "\n")
	}
	var sb strings.Builder
	sb.WriteString("💼 <b>Balans</b>\n")
	inHand := money{}
	for _, st := range states {
		inHand.Add(st.currency, st.minor)
		sb.WriteString(fmt.Sprintf("\n<b>%s</b>\n", formatMoney(st.minor, st.currency)))
		sb.WriteString(fmt.Sprintf("Tekshiruv: %s · %s\n",
			st.checkpoint.TakenAt.In(tashkent).Format("02.01.2006"),
			formatMoney(st.checkpoint.Minor, st.currency)))
		if st.in != 0 || st.out != 0 {
			sb.WriteString(fmt.Sprintf("Shundan beri: +%s · -%s\n",
				formatMoney(st.in, st.currency), formatMoney(st.out, st.currency)))
		}
	}
	if debts, err := debtBalances(ctx, b.pool, u.ID, ""); err == nil && len(debts) > 0 {
		owed, owe, _ := finance.Totals(debts)
		total := money{}
		for cur, v := range inHand {
			total.Add(cur, v)
		}
		for cur, v := range owed {
			total.Add(cur, v)
		}
		for cur, v := range owe {
			total.Add(cur, -v)
		}
		sb.WriteString(fmt.Sprintf("\n🤝 Sizga qarzdor: %s · Siz: %s\n", money(owed), money(owe)))
		sb.WriteString(fmt.Sprintf("📊 Umumiy holat: <b>%s</b>\n", total))
	}
	sb.WriteString("\n<i>Haqiqiy summani yozib, hisobni tekshirish: /balans 4 700 000</i>")
	return sb.String()
}

/* ---------------------------- morning summary ----------------------------- */

// dailySummary is the 09:00 message: balance, yesterday, the month so far,
// open debts. Empty for someone who has never written anything.
func (b *Bot) dailySummary(ctx context.Context, u *user, today time.Time) (string, error) {
	var exists bool
	if err := b.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM entries WHERE user_id = $1)`, u.ID).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return "", nil
	}
	yesterday := today.AddDate(0, 0, -1)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("☀️ <b>Xayrli tong!</b> · %s\n\n", today.Format("02.01")))
	if states, err := b.balances(ctx, u); err == nil && len(states) > 0 {
		inHand := money{}
		for _, st := range states {
			inHand.Add(st.currency, st.minor)
		}
		sb.WriteString(fmt.Sprintf("💼 Balans: %s\n", inHand))
	}
	if spent, n, err := b.spentBetween(ctx, u, yesterday, today); err == nil {
		if n == 0 {
			sb.WriteString("💸 Kecha: hech narsa yozilmagan\n")
		} else {
			sb.WriteString(fmt.Sprintf("💸 Kecha: %s · %d ta\n", spent, n))
		}
	}

	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
	from, to, label := monthStart, today.AddDate(0, 0, 1), financetext.MonthName(today.Month())
	if today.Day() == 1 {
		from, to = monthStart.AddDate(0, -1, 0), monthStart
		label = financetext.MonthName(from.Month()) + " jami"
	}
	if spent, _, err := b.spentBetween(ctx, u, from, to); err == nil {
		income, err := b.incomeBetween(ctx, u, from, to)
		if err != nil || income.IsZero() {
			sb.WriteString(fmt.Sprintf("📆 %s: %s - /hisob\n", label, spent))
		} else {
			sb.WriteString(fmt.Sprintf("📆 %s: kirim %s · sarf %s · qoldi %s\n", label, income, spent, netLine(income, spent)))
			if today.Day() != 1 {
				if end, ok := monthProjection(today, income, spent); ok {
					sign := ""
					if end > 0 {
						sign = "+"
					}
					sb.WriteString(fmt.Sprintf("📈 Shu sur'atda oy oxirida: ≈ %s%s\n", sign, formatMoney(end, "UZS")))
				}
			}
		}
	}
	if balances, err := debtBalances(ctx, b.pool, u.ID, ""); err == nil && len(balances) > 0 {
		owed, owe, _ := finance.Totals(balances)
		sb.WriteString(fmt.Sprintf("🤝 Sizga qarzdor: %s · Siz: %s\n", money(owed), money(owe)))
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

func (b *Bot) summarySetting(ctx context.Context, u *user, arg string, today time.Time) string {
	switch financetext.NormalizeWord(arg) {
	case "off", "o'chir", "o'chirish", "ochir", "yo'q", "0":
		if _, err := b.pool.Exec(ctx, `UPDATE users SET daily_summary = false WHERE id = $1`, u.ID); err != nil {
			return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
		}
		return "🔕 Ertalabki xulosa o'chirildi. Qayta yoqish: /xulosa on"
	case "on", "yoqish", "ha", "1":
		if _, err := b.pool.Exec(ctx, `UPDATE users SET daily_summary = true WHERE id = $1`, u.ID); err != nil {
			return "❌ Saqlanmadi: " + escapeHTML(shortError(err))
		}
		return "🔔 Ertalabki xulosa yoqildi: har kuni soat 09:00 da."
	}
	text, err := b.dailySummary(ctx, u, today)
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}
	state := "yoqilgan (09:00) - o'chirish: /xulosa off"
	if !u.DailySummary {
		state = "o'chirilgan - yoqish: /xulosa on"
	}
	if text == "" {
		return "Hali yozuv yo'q, xulosa uchun ma'lumot yo'q.\n\nErtalabki xulosa: " + state
	}
	return text + "\n\nErtalabki xulosa: " + state
}

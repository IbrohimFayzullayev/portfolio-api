package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ibrohimcoder/portfolio-api/internal/finance"
)

// Personal money log.
//
// Any message that is not a command is read as an entry: "tushlik 35k" is
// saved at once and answered with a card showing what was understood, with a
// button for every field that could have been guessed wrong. Writing is one
// line; fixing a wrong guess is one tap. Nothing waits for confirmation,
// because an entry that needs a second message to exist is an entry that
// does not get written.
//
// Reports read the same table: /hisob for where money went, /qarzlar for who
// owes whom, /oxirgi for the latest entries, and a few lines in the morning
// summary. Like the rest of the bot this talks to PostgreSQL directly.

/* -------------------------------- writing --------------------------------- */

func (b *Bot) recordEntry(ctx context.Context, chatID int64, text string) {
	today := dayStart(time.Now())

	// Several lines at once — the end-of-day dump — become several rows.
	parts := splitEntries(text, today)
	if len(parts) > 1 {
		b.recordBatch(ctx, chatID, parts, today)
		return
	}

	p, err := parseEntry(text, today)
	if err != nil {
		b.reply(ctx, chatID, parseErrorHelp(err))
		return
	}

	id, err := b.insertEntry(ctx, p, text)
	if err != nil {
		b.reply(ctx, chatID, "❌ Saqlanmadi: "+escapeHTML(shortError(err)))
		return
	}

	card, markup := b.entryCard(ctx, id)
	if err := b.tg.sendMessageMarkup(ctx, chatID, card, markup); err != nil {
		log.Printf("bot: finance card failed: %v", err)
	}
}

func (b *Bot) insertEntry(ctx context.Context, p parsedEntry, raw string) (string, error) {
	var id string
	err := b.pool.QueryRow(ctx, `
		INSERT INTO finance_entries
		    (kind, amount_minor, currency, category, person, note, raw_text, spent_on)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::date)
		RETURNING id::text`,
		p.kind, p.amountMinor, p.currency, p.category, p.person, p.note, raw,
		p.spentOn.Format("2006-01-02"),
	).Scan(&id)
	return id, err
}

func parseErrorHelp(err error) string {
	switch {
	case errors.Is(err, errNoAmount):
		return strings.Join([]string{
			"Summani topolmadim — hech narsa yozilmadi.",
			"",
			"Masalan:",
			"<code>tushlik 35k</code>",
			"<code>kecha taksi 18000</code>",
			"<code>Sardorga 100k berdim to'yga</code>",
			"<code>Aliga qarz 500 ming</code>",
			"<code>Ali qaytardi 200k</code>",
			"",
			"Bir nechtasini alohida qatorlarda yuborsangiz ham bo'ladi. Buyruqlar: /help",
		}, "\n")
	case errors.Is(err, errNoPerson):
		return strings.Join([]string{
			"Kimligini topolmadim — hech narsa yozilmadi.",
			"",
			"Ismni qo'shimchasi bilan yozing:",
			"<code>Aliga qarz 500k</code> — siz berdingiz",
			"<code>Alidan qarz oldim 500k</code> — siz oldingiz",
			"<code>Ali qaytardi 200k</code> — sizga qaytardi",
			"<code>Aliga qaytardim 200k</code> — siz qaytardingiz",
		}, "\n")
	}
	return "❌ Tushunmadim: " + escapeHTML(err.Error())
}

// "; " and ", " separate entries on one line — but only when every piece has
// an amount, so "kofe, pishiriq 45k" stays one entry. "1,5 mln" has no space
// after the comma and is never split.
var entrySeparator = regexp.MustCompile(`\s*;\s*|,\s+`)

func splitEntries(text string, today time.Time) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pieces := entrySeparator.Split(line, -1)
		if len(pieces) > 1 {
			all := true
			for _, piece := range pieces {
				if _, err := parseEntry(piece, today); errors.Is(err, errNoAmount) {
					all = false
					break
				}
			}
			if all {
				out = append(out, pieces...)
				continue
			}
		}
		out = append(out, line)
	}
	return out
}

// recordBatch saves every line that parses and names the ones that did not,
// rather than refusing the whole message over one typo.
func (b *Bot) recordBatch(ctx context.Context, chatID int64, parts []string, today time.Time) {
	var (
		sb      strings.Builder
		buttons [][]inlineButton
		row     []inlineButton
		failed  []string
		saved   int
		spent   = money{}
	)
	for _, part := range parts {
		p, err := parseEntry(part, today)
		if err != nil {
			failed = append(failed, part)
			continue
		}
		id, err := b.insertEntry(ctx, p, part)
		if err != nil {
			failed = append(failed, part)
			continue
		}
		saved++
		if p.kind == kindExpense || p.kind == kindGift {
			spent.add(p.currency, p.amountMinor)
		}

		label := firstNonEmpty(p.note, p.person, categoryByKey(p.category).Label)
		if isDebtKind(p.kind) {
			label = p.person
		}
		sb.WriteString(fmt.Sprintf("%d. %s <b>%s</b> — %s %s\n", saved, kindOf(p.kind).Emoji,
			formatMoney(p.amountMinor, p.currency), escapeHTML(truncate(label, 40)),
			categoryByKey(p.category).Emoji))
		if !sameDay(p.spentOn, today) {
			sb.WriteString("    📅 " + dayLabel(p.spentOn, today) + "\n")
		}

		row = append(row, inlineButton{Text: fmt.Sprintf("%d", saved), CallbackData: "fn:card:" + id})
		if len(row) == 5 {
			buttons = append(buttons, row)
			row = nil
		}
	}
	if len(row) > 0 {
		buttons = append(buttons, row)
	}

	head := fmt.Sprintf("✅ <b>%d ta yozildi</b>", saved)
	if !spent.isZero() {
		head += " · sarf " + spent.String()
	}
	text := head + "\n\n" + sb.String()
	if len(failed) > 0 {
		text += "\n❌ <b>Yozilmadi</b> — summa yoki ism topilmadi:\n"
		for _, f := range failed {
			text += "<code>" + escapeHTML(truncate(f, 60)) + "</code>\n"
		}
	}
	if saved > 0 {
		text += "\n<i>Tuzatish uchun raqamni bosing.</i>"
	}

	var markup *inlineKeyboard
	if len(buttons) > 0 {
		markup = &inlineKeyboard{InlineKeyboard: buttons}
	}
	if err := b.tg.sendMessageMarkup(ctx, chatID, text, markup); err != nil {
		log.Printf("bot: finance batch reply failed: %v", err)
	}
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	if err := b.tg.sendMessage(ctx, chatID, text); err != nil {
		log.Printf("bot: sendMessage failed: %v", err)
	}
}

/* --------------------------------- card ----------------------------------- */

type financeEntry struct {
	id          string
	kind        string
	amountMinor int64
	currency    string
	category    string
	person      string
	note        string
	spentOn     time.Time
}

func (b *Bot) loadEntry(ctx context.Context, id string) (financeEntry, error) {
	var e financeEntry
	err := b.pool.QueryRow(ctx, `
		SELECT id::text, kind, amount_minor, currency, category, person, note, spent_on
		FROM finance_entries
		WHERE $1 ~ '^[0-9a-f-]{36}$' AND id = $1::uuid`, id,
	).Scan(&e.id, &e.kind, &e.amountMinor, &e.currency, &e.category, &e.person, &e.note, &e.spentOn)
	return e, err
}

// entryCard is what was understood, plus the buttons to correct it. The same
// card is redrawn after every change, so the screen always shows the row as
// it is in the database.
func (b *Bot) entryCard(ctx context.Context, id string) (string, *inlineKeyboard) {
	e, err := b.loadEntry(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "Yozuv topilmadi — o'chirilgan bo'lishi mumkin.\n\n/oxirgi", nil
	}
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err)), nil
	}

	today := dayStart(time.Now())
	k := kindOf(e.kind)
	c := categoryByKey(e.category)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s <b>%s</b> · %s %s\n", k.Emoji, k.Label, c.Emoji, c.Label))
	sb.WriteString("<b>" + formatMoney(e.amountMinor, e.currency) + "</b>")
	if e.note != "" {
		sb.WriteString(" — " + escapeHTML(e.note))
	}
	sb.WriteString("\n")
	if e.person != "" {
		sb.WriteString("👤 " + escapeHTML(e.person) + "\n")
	}
	sb.WriteString("📅 " + dayLabel(e.spentOn, today) + "\n")

	// One line of context: where the person's balance now stands, or what
	// the day has cost so far. It is what you would check next anyway.
	switch {
	case isDebtKind(e.kind) && e.person != "":
		sb.WriteString("\n" + b.personBalanceLine(ctx, e.person))
	case e.kind == kindExpense || e.kind == kindGift:
		if day, _, err := b.spentBetween(ctx, e.spentOn, e.spentOn.AddDate(0, 0, 1)); err == nil {
			label := "Bugun"
			if !sameDay(e.spentOn, today) {
				label = e.spentOn.Format("02.01")
			}
			sb.WriteString(fmt.Sprintf("\n%s jami: %s", label, day))
		}
	}

	return sb.String(), entryKeyboard(e.id)
}

func entryKeyboard(id string) *inlineKeyboard {
	btn := func(text, op string) inlineButton {
		return inlineButton{Text: text, CallbackData: "fn:" + op + ":" + id}
	}
	return &inlineKeyboard{InlineKeyboard: [][]inlineButton{
		{btn("🏷 Kategoriya", "cat"), btn("🔁 Turi", "kind")},
		{btn("💰 Summa", "amt"), btn("👤 Kim", "who")},
		{btn("📅 Sana", "date"), btn("📝 Izoh", "note")},
		{btn("🗑 O'chirish", "del"), btn("📋 Ro'yxat", "list")},
	}}
}

func categoryPicker(e financeEntry) (string, *inlineKeyboard) {
	var rows [][]inlineButton
	var row []inlineButton
	for _, c := range finance.Categories {
		label := c.Emoji + " " + c.Label
		if c.Key == e.category {
			label = "• " + label
		}
		row = append(row, inlineButton{Text: label, CallbackData: "fc:" + c.Key + ":" + e.id})
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []inlineButton{{Text: "⬅️ Orqaga", CallbackData: "fn:card:" + e.id}})

	text := fmt.Sprintf("🏷 <b>Kategoriya</b>\n%s — %s",
		formatMoney(e.amountMinor, e.currency), escapeHTML(firstNonEmpty(e.note, "izohsiz")))
	return text, &inlineKeyboard{InlineKeyboard: rows}
}

func kindPicker(e financeEntry) (string, *inlineKeyboard) {
	var rows [][]inlineButton
	var row []inlineButton
	for _, k := range finance.Kinds {
		label := k.Emoji + " " + k.Label
		if k.Key == e.kind {
			label = "• " + label
		}
		row = append(row, inlineButton{Text: label, CallbackData: "fk:" + k.Code + ":" + e.id})
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []inlineButton{{Text: "⬅️ Orqaga", CallbackData: "fn:card:" + e.id}})

	text := strings.Join([]string{
		"🔁 <b>Turi</b>",
		formatMoney(e.amountMinor, e.currency) + " — " + escapeHTML(firstNonEmpty(e.note, "izohsiz")),
		"",
		"<i>Qarz turlari kishi bo'yicha hisoblanadi: berdim − qaytdi − oldim + qaytardim.</i>",
	}, "\n")
	return text, &inlineKeyboard{InlineKeyboard: rows}
}

/* ------------------------------- callbacks -------------------------------- */

type redrawFunc func(context.Context) (string, *inlineKeyboard)

// financeCallback handles the fn:, fc: and fk: buttons. It returns the toast
// text and, when the message should change, how to redraw it.
func (b *Bot) financeCallback(ctx context.Context, chatID int64, action, arg string) (string, redrawFunc) {
	switch action {
	case "fc":
		key, id, _ := strings.Cut(arg, ":")
		return b.setCategory(ctx, id, key), b.cardRedraw(id)
	case "fk":
		code, id, _ := strings.Cut(arg, ":")
		return b.setKind(ctx, id, finance.KindByCode(code)), b.cardRedraw(id)
	}

	op, id, _ := strings.Cut(arg, ":")
	switch op {
	case "card":
		return "", b.cardRedraw(id)

	case "list":
		return "", func(c context.Context) (string, *inlineKeyboard) { return b.recentList(c) }

	case "cat", "kind":
		e, err := b.loadEntry(ctx, id)
		if err != nil {
			return "Yozuv topilmadi", nil
		}
		return "", func(context.Context) (string, *inlineKeyboard) {
			if op == "cat" {
				return categoryPicker(e)
			}
			return kindPicker(e)
		}

	case "del":
		e, err := b.loadEntry(ctx, id)
		if err != nil {
			return "Yozuv topilmadi", nil
		}
		return "", func(context.Context) (string, *inlineKeyboard) {
			return fmt.Sprintf("🗑 <b>O'chirilsinmi?</b>\n%s — %s · %s",
					formatMoney(e.amountMinor, e.currency),
					escapeHTML(firstNonEmpty(e.note, kindOf(e.kind).Label)),
					e.spentOn.Format("02.01")),
				&inlineKeyboard{InlineKeyboard: [][]inlineButton{{
					{Text: "Ha, o'chirish", CallbackData: "fn:delok:" + id},
					{Text: "Bekor qilish", CallbackData: "fn:card:" + id},
				}}}
		}

	case "delok":
		var amount int64
		var currency, note string
		err := b.pool.QueryRow(ctx, `
			DELETE FROM finance_entries
			WHERE $1 ~ '^[0-9a-f-]{36}$' AND id = $1::uuid
			RETURNING amount_minor, currency, note`, id).Scan(&amount, &currency, &note)
		if errors.Is(err, pgx.ErrNoRows) {
			return "Allaqachon o'chirilgan", nil
		}
		if err != nil {
			return "Xato: " + shortError(err), nil
		}
		text := fmt.Sprintf("🗑 <s>%s — %s</s>\nO'chirildi.",
			formatMoney(amount, currency), escapeHTML(firstNonEmpty(note, "izohsiz")))
		return "O'chirildi", func(context.Context) (string, *inlineKeyboard) { return text, nil }

	case "amt", "who", "date", "note":
		return b.askFinanceField(ctx, chatID, op, id), nil
	}

	return "Noma'lum tugma", nil
}

func (b *Bot) cardRedraw(id string) redrawFunc {
	return func(c context.Context) (string, *inlineKeyboard) { return b.entryCard(c, id) }
}

func (b *Bot) setCategory(ctx context.Context, id, key string) string {
	c := categoryByKey(key)
	if c.Key != key {
		return "Noma'lum kategoriya"
	}
	tag, err := b.pool.Exec(ctx, `
		UPDATE finance_entries SET category = $2, updated_at = now()
		WHERE $1 ~ '^[0-9a-f-]{36}$' AND id = $1::uuid`, id, key)
	if err != nil {
		return "Xato: " + shortError(err)
	}
	if tag.RowsAffected() == 0 {
		return "Yozuv topilmadi"
	}
	return c.Label
}

func (b *Bot) setKind(ctx context.Context, id, kind string) string {
	if _, ok := finance.KindOf(kind); !ok {
		return "Noma'lum tur"
	}
	e, err := b.loadEntry(ctx, id)
	if err != nil {
		return "Yozuv topilmadi"
	}

	// Debt rows live in the "qarz" category; a row leaving debt gets its
	// category guessed again from its note rather than keeping "qarz".
	category := e.category
	switch {
	case isDebtKind(kind):
		category = "qarz"
	case category == "qarz":
		category = "boshqa"
		for _, w := range strings.Fields(e.note) {
			if c := categoryOf(normalizeWord(w)); c != "" {
				category = c
				break
			}
		}
	}

	if _, err := b.pool.Exec(ctx, `
		UPDATE finance_entries SET kind = $2, category = $3, updated_at = now()
		WHERE id = $1::uuid`, id, kind, category); err != nil {
		return "Xato: " + shortError(err)
	}
	if needsPerson(kind) && e.person == "" {
		return "Endi 👤 Kim tugmasi bilan ismni yozing"
	}
	return kindOf(kind).Label
}

/* ------------------------- edits by question/answer ------------------------ */

var financeFields = map[string]struct {
	intent      string
	question    string
	placeholder string
}{
	"amt":  {"fin.amount", "💰 <b>Summa</b> — yangi summani javob qilib yuboring.\n\n<i>35k, 1.5 mln, 250 000, $20</i>", "35k"},
	"who":  {"fin.person", "👤 <b>Kim</b> — ismni javob qilib yuboring.\n\n<i>O'chirish uchun <code>-</code> yuboring.</i>", "Ali"},
	"date": {"fin.date", "📅 <b>Sana</b> — javob qilib yuboring.\n\n<i><code>bugun</code>, <code>kecha</code>, <code>12.10</code> yoki <code>12.10.2026</code></i>", "kecha"},
	"note": {"fin.note", "📝 <b>Izoh</b> — yangi izohni javob qilib yuboring.\n\n<i>O'chirish uchun <code>-</code> yuboring.</i>", "tushlik"},
}

func (b *Bot) askFinanceField(ctx context.Context, chatID int64, op, id string) string {
	f, ok := financeFields[op]
	if !ok {
		return "Noma'lum maydon"
	}
	e, err := b.loadEntry(ctx, id)
	if err != nil {
		return "Yozuv topilmadi"
	}

	current := ""
	switch op {
	case "amt":
		current = formatMoney(e.amountMinor, e.currency)
	case "who":
		current = e.person
	case "date":
		current = e.spentOn.Format("02.01.2006")
	case "note":
		current = e.note
	}
	text := f.question
	if current != "" {
		text += "\n\nHozir: <code>" + escapeHTML(current) + "</code>"
	}
	b.ask(ctx, chatID, f.intent, id, text, f.placeholder)
	return "Javob yozing"
}

// applyFinanceEdit is the answer to one of the questions above.
func (b *Bot) applyFinanceEdit(ctx context.Context, intent, id, value string) (string, *inlineKeyboard) {
	var (
		tag interface{ RowsAffected() int64 }
		err error
	)
	clear := value == "-"

	switch intent {
	case "fin.amount":
		minor, currency, ok := parseAmount(value)
		if !ok {
			return "Summani tushunmadim — o'zgarmadi. Masalan: <code>35k</code>, <code>1.5 mln</code>, <code>$20</code>", nil
		}
		tag, err = b.pool.Exec(ctx, `
			UPDATE finance_entries SET amount_minor = $2, currency = $3, updated_at = now()
			WHERE id = $1::uuid`, id, minor, currency)

	case "fin.person":
		person := ""
		if !clear {
			person = capitalise(cleanOriginal(value))
		}
		tag, err = b.pool.Exec(ctx, `
			UPDATE finance_entries SET person = $2, updated_at = now()
			WHERE id = $1::uuid`, id, person)

	case "fin.date":
		day, ok := parseDay(value, dayStart(time.Now()))
		if !ok {
			return "Sanani tushunmadim — o'zgarmadi. Masalan: <code>kecha</code> yoki <code>12.10</code>", nil
		}
		tag, err = b.pool.Exec(ctx, `
			UPDATE finance_entries SET spent_on = $2::date, updated_at = now()
			WHERE id = $1::uuid`, id, day.Format("2006-01-02"))

	case "fin.note":
		note := value
		if clear {
			note = ""
		}
		tag, err = b.pool.Exec(ctx, `
			UPDATE finance_entries SET note = $2, updated_at = now()
			WHERE id = $1::uuid`, id, note)

	default:
		return "Noma'lum maydon", nil
	}

	if err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err)), nil
	}
	if tag.RowsAffected() == 0 {
		return "Yozuv topilmadi — o'chirilgan bo'lishi mumkin.", nil
	}

	card, markup := b.entryCard(ctx, id)
	return "✏️ <i>Saqlandi</i>\n\n" + card, markup
}

/* -------------------------------- /oxirgi --------------------------------- */

func (b *Bot) recentList(ctx context.Context) (string, *inlineKeyboard) {
	rows, err := b.pool.Query(ctx, `
		SELECT id::text, kind, amount_minor, currency, category, person, note, spent_on
		FROM finance_entries
		ORDER BY created_at DESC
		LIMIT 10`)
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err)), nil
	}
	defer rows.Close()

	var entries []financeEntry
	for rows.Next() {
		var e financeEntry
		if err := rows.Scan(&e.id, &e.kind, &e.amountMinor, &e.currency, &e.category,
			&e.person, &e.note, &e.spentOn); err != nil {
			return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err)), nil
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err)), nil
	}
	if len(entries) == 0 {
		return "Hali hech narsa yozilmagan.\n\nShunchaki yozing: <code>tushlik 35k</code>", nil
	}

	var sb strings.Builder
	sb.WriteString("📋 <b>Oxirgi yozuvlar</b>\n\n")
	var buttons [][]inlineButton
	var row []inlineButton
	for i, e := range entries {
		label := firstNonEmpty(e.note, e.person, categoryByKey(e.category).Label)
		if e.person != "" && e.note != "" && isDebtKind(e.kind) {
			label = e.person
		}
		sb.WriteString(fmt.Sprintf("%d. %s %s <b>%s</b> — %s\n",
			i+1, e.spentOn.Format("02.01"), kindOf(e.kind).Emoji,
			formatMoney(e.amountMinor, e.currency), escapeHTML(truncate(label, 40))))

		row = append(row, inlineButton{
			Text:         fmt.Sprintf("%d", i+1),
			CallbackData: "fn:card:" + e.id,
		})
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

func (b *Bot) sendRecentList(ctx context.Context, chatID int64) {
	text, markup := b.recentList(ctx)
	if err := b.tg.sendMessageMarkup(ctx, chatID, text, markup); err != nil {
		log.Printf("bot: recent list failed: %v", err)
	}
}

/* --------------------------------- sums ----------------------------------- */

// spentBetween: expenses and gifts on days in [from, to) — money that left
// and is not coming back. Loans are deliberately not "spent".
func (b *Bot) spentBetween(ctx context.Context, from, to time.Time) (money, int64, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT currency, count(*), COALESCE(sum(amount_minor), 0)::bigint
		FROM finance_entries
		WHERE kind IN ('expense', 'gift')
		  AND spent_on >= $1::date AND spent_on < $2::date
		GROUP BY currency`, from.Format("2006-01-02"), to.Format("2006-01-02"))
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
		m.add(cur, sum)
		count += n
	}
	return m, count, rows.Err()
}

/* ------------------------------- balances --------------------------------- */

func (b *Bot) debtBalances(ctx context.Context, person string) ([]finance.Balance, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT person, currency, kind, amount_minor, spent_on
		FROM finance_entries
		WHERE kind IN ('lent', 'repaid_in', 'borrowed', 'repaid_out')
		  AND person <> ''
		  AND ($1 = '' OR lower(person) = lower($1))
		ORDER BY spent_on, created_at`, person)
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

// debtTotals splits balances into what I am owed and what I owe.
func debtTotals(balances []finance.Balance) (owedToMe, iOwe money, people int) {
	owedToMe, iOwe = money{}, money{}
	seen := map[string]bool{}
	for _, d := range balances {
		if d.Minor > 0 {
			owedToMe.add(d.Currency, d.Minor)
		} else {
			iOwe.add(d.Currency, -d.Minor)
		}
		seen[strings.ToLower(d.Person)] = true
	}
	return owedToMe, iOwe, len(seen)
}

func (b *Bot) personBalanceLine(ctx context.Context, person string) string {
	balances, err := b.debtBalances(ctx, person)
	if err != nil {
		return ""
	}
	owedToMe, iOwe, _ := debtTotals(balances)
	name := escapeHTML(person)
	switch {
	case owedToMe.isZero() && iOwe.isZero():
		return "📊 " + name + " bilan hisob teng"
	case iOwe.isZero():
		return fmt.Sprintf("📊 %s sizga qarzdor: <b>%s</b>", name, owedToMe)
	case owedToMe.isZero():
		return fmt.Sprintf("📊 Siz qarzdorsiz (%s): <b>%s</b>", name, iOwe)
	default:
		return fmt.Sprintf("📊 %s sizga: %s · siz unga: %s", name, owedToMe, iOwe)
	}
}

/* -------------------------------- /qarzlar -------------------------------- */

func (b *Bot) debtsReport(ctx context.Context, arg string) string {
	if arg = strings.TrimSpace(arg); arg != "" {
		return b.personHistory(ctx, arg)
	}

	balances, err := b.debtBalances(ctx, "")
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
	}
	if len(balances) == 0 {
		return "🤝 Ochiq qarz yo'q.\n\n<i>Yozish: <code>Aliga qarz 500k</code>, <code>Ali qaytardi 200k</code></i>"
	}

	today := dayStart(time.Now())
	owedToMe, iOwe, _ := debtTotals(balances)

	var sb strings.Builder
	sb.WriteString("🤝 <b>Qarzlar</b>\n")

	section := func(title string, total money, positive bool) {
		if total.isZero() {
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
			sb.WriteString(fmt.Sprintf("• %s — %s · %s\n",
				escapeHTML(d.Person), formatMoney(finance.Abs(d.Minor), d.Currency), age))
		}
	}
	section("Sizga qarzdor", owedToMe, true)
	section("Siz qarzdorsiz", iOwe, false)

	sb.WriteString("\n<i>Bitta odam tarixi: <code>/qarzlar Ali</code></i>")
	return sb.String()
}

// personHistory: every row with one person — loans and gifts alike, because
// "how much have I given Sardor this year" is the same question.
func (b *Bot) personHistory(ctx context.Context, name string) string {
	name = cleanOriginal(name)
	candidates := []string{name}
	if s, ok := dativeStem(name); ok {
		candidates = append(candidates, s)
	}
	if s, ok := ablativeStem(name); ok {
		candidates = append(candidates, s)
	}

	for _, who := range candidates {
		rows, err := b.pool.Query(ctx, `
			SELECT kind, amount_minor, currency, note, spent_on, person
			FROM finance_entries
			WHERE lower(person) = lower($1)
			ORDER BY spent_on, created_at`, who)
		if err != nil {
			return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err))
		}

		var (
			lines   []string
			display string
			debt    = money{}
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
			debt.add(currency, finance.Signed(kind, minor))
			if kind == kindGift {
				given.add(currency, minor)
			}
			line := fmt.Sprintf("%s %s %s", day.Format("02.01.06"), kindOf(kind).Emoji,
				formatMoney(minor, currency))
			if note != "" {
				line += " — " + escapeHTML(truncate(note, 40))
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

		// Long histories keep the most recent part; the balance below is
		// computed from all of it either way.
		if len(lines) > 25 {
			lines = append([]string{fmt.Sprintf("<i>… yana %d ta oldinroq</i>", len(lines)-25)},
				lines[len(lines)-25:]...)
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("👤 <b>%s</b>\n\n", escapeHTML(display)))
		sb.WriteString(strings.Join(lines, "\n"))
		sb.WriteString("\n\n" + b.personBalanceLine(ctx, display))
		if !given.isZero() {
			sb.WriteString(fmt.Sprintf("\n🎁 Jami berilgan (qaytmaydigan): %s", given))
		}
		return sb.String()
	}

	return fmt.Sprintf("👤 <b>%s</b> bo'yicha yozuv yo'q.\n\n/qarzlar — hamma ochiq qarzlar",
		escapeHTML(name))
}

/* --------------------------------- /hisob --------------------------------- */

type finPeriod struct {
	label    string
	from, to time.Time // dates, to exclusive
	// The same stretch one month earlier, when comparing makes sense.
	compare *finPeriod
}

func (p finPeriod) days() int { return int(p.to.Sub(p.from).Hours()/24 + 0.5) }

// financePeriod reads the /hisob argument. Unknown words fall back to this
// month, which is what the command means with no argument at all.
func financePeriod(arg string, today time.Time) finPeriod {
	arg = normalizeWord(strings.Join(strings.Fields(arg), " "))
	tomorrow := today.AddDate(0, 0, 1)
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())

	switch arg {
	case "bugun", "kun":
		return finPeriod{label: "Bugun · " + today.Format("02.01"), from: today, to: tomorrow}
	case "kecha":
		y := today.AddDate(0, 0, -1)
		return finPeriod{label: "Kecha · " + y.Format("02.01"), from: y, to: today}
	case "hafta":
		from := today.AddDate(0, 0, -6)
		return finPeriod{
			label: fmt.Sprintf("Oxirgi 7 kun · %s–%s", from.Format("02.01"), today.Format("02.01")),
			from:  from, to: tomorrow,
		}
	case "yil":
		return finPeriod{
			label: fmt.Sprintf("%d yil", today.Year()),
			from:  time.Date(today.Year(), 1, 1, 0, 0, 0, 0, today.Location()), to: tomorrow,
		}
	case "o'tgan", "o'tgan oy", "otgan":
		from := monthStart.AddDate(0, -1, 0)
		return finPeriod{
			label: fmt.Sprintf("%s %d", monthName(from.Month()), from.Year()),
			from:  from, to: monthStart,
		}
	}

	// A month by name: "/hisob sentabr".
	if len(arg) >= 3 {
		for i, name := range uzMonths {
			if strings.HasPrefix(strings.ToLower(name), arg) {
				from := time.Date(today.Year(), time.Month(i+1), 1, 0, 0, 0, 0, today.Location())
				if from.After(today) {
					from = from.AddDate(-1, 0, 0)
				}
				to := from.AddDate(0, 1, 0)
				if to.After(tomorrow) {
					to = tomorrow
				}
				return finPeriod{label: fmt.Sprintf("%s %d", monthName(from.Month()), from.Year()), from: from, to: to}
			}
		}
	}

	// This month so far, against the same days of last month.
	p := finPeriod{
		label: fmt.Sprintf("%s %d · 1–%d", monthName(today.Month()), today.Year(), today.Day()),
		from:  monthStart, to: tomorrow,
	}
	prevFrom := monthStart.AddDate(0, -1, 0)
	prevTo := prevFrom.AddDate(0, 0, today.Day())
	if prevTo.After(monthStart) {
		prevTo = monthStart // 31 March against a 28-day February
	}
	p.compare = &finPeriod{from: prevFrom, to: prevTo}
	return p
}

func (b *Bot) financeReport(ctx context.Context, arg string) string {
	today := dayStart(time.Now())
	p := financePeriod(arg, today)
	from, to := p.from.Format("2006-01-02"), p.to.Format("2006-01-02")

	// Totals per kind.
	rows, err := b.pool.Query(ctx, `
		SELECT kind, currency, count(*), sum(amount_minor)::bigint
		FROM finance_entries
		WHERE spent_on >= $1::date AND spent_on < $2::date
		GROUP BY kind, currency`, from, to)
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
		byKind[kind].add(cur, sum)
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
		sb.WriteString(b.debtFooter(ctx))
		return sb.String()
	}

	spent := money{}
	for _, k := range []string{kindExpense, kindGift} {
		for cur, v := range byKind[k] {
			spent.add(cur, v)
		}
	}

	sb.WriteString(fmt.Sprintf("\n💸 <b>Sarflandi: %s</b>\n", spent))
	if v := byKind[kindExpense]; v != nil {
		sb.WriteString(fmt.Sprintf("Xarajat — %s · %d ta\n", v, counts[kindExpense]))
	}
	if v := byKind[kindGift]; v != nil {
		sb.WriteString(fmt.Sprintf("Berildi — %s · %d ta\n", v, counts[kindGift]))
	}
	if days := p.days(); days > 1 && !spent.isZero() {
		avg := money{}
		for cur, v := range spent {
			avg.add(cur, v/int64(days))
		}
		sb.WriteString(fmt.Sprintf("Kuniga o'rtacha — %s\n", avg))
	}
	if p.compare != nil {
		if prev, _, err := b.spentBetween(ctx, p.compare.from, p.compare.to); err == nil && !prev.isZero() {
			sb.WriteString(fmt.Sprintf("O'tgan oy shu kunlarda — %s%s\n",
				prev, delta(spent["UZS"], prev["UZS"])))
		}
	}

	sb.WriteString(b.categorySection(ctx, from, to, spent["UZS"]))
	sb.WriteString(b.giftsSection(ctx, from, to))
	sb.WriteString(b.largestSection(ctx, from, to))

	// Loans that moved in the period — not spending, but still money that
	// left or came back.
	var loanLines []string
	for _, k := range []string{kindLent, kindRepaidIn, kindBorrowed, kindRepaidOut} {
		if v := byKind[k]; v != nil {
			loanLines = append(loanLines, fmt.Sprintf("%s %s — %s", kindOf(k).Emoji, kindOf(k).Label, v))
		}
	}
	if len(loanLines) > 0 {
		sb.WriteString("\n<b>Qarz harakati</b>\n" + strings.Join(loanLines, "\n") + "\n")
	}

	sb.WriteString(b.debtFooter(ctx))
	return sb.String()
}

func (b *Bot) categorySection(ctx context.Context, from, to string, totalUZS int64) string {
	rows, err := b.pool.Query(ctx, `
		SELECT category, currency, sum(amount_minor)::bigint
		FROM finance_entries
		WHERE kind IN ('expense', 'gift')
		  AND spent_on >= $1::date AND spent_on < $2::date
		GROUP BY category, currency`, from, to)
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
		perCat[cat].add(cur, sum)
	}
	if len(order) == 0 {
		return ""
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := perCat[order[i]], perCat[order[j]]
		if a["UZS"] != b["UZS"] {
			return a["UZS"] > b["UZS"]
		}
		return a["USD"] > b["USD"]
	})

	var sb strings.Builder
	sb.WriteString("\n<b>Kategoriyalar</b>\n")
	for _, cat := range order {
		c := categoryByKey(cat)
		m := perCat[cat]
		line := fmt.Sprintf("%s %s — %s", c.Emoji, c.Label, m)
		if totalUZS > 0 && m["UZS"] > 0 {
			line += fmt.Sprintf(" · %.0f%%", float64(m["UZS"])/float64(totalUZS)*100)
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

func (b *Bot) giftsSection(ctx context.Context, from, to string) string {
	rows, err := b.pool.Query(ctx, `
		SELECT COALESCE(NULLIF((array_agg(person ORDER BY created_at DESC))[1], ''), '—'),
		       currency, sum(amount_minor)::bigint, count(*)
		FROM finance_entries
		WHERE kind = 'gift'
		  AND spent_on >= $1::date AND spent_on < $2::date
		GROUP BY lower(person), currency
		ORDER BY 3 DESC
		LIMIT 10`, from, to)
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
		line := fmt.Sprintf("%s — %s", escapeHTML(person), formatMoney(sum, cur))
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

func (b *Bot) largestSection(ctx context.Context, from, to string) string {
	rows, err := b.pool.Query(ctx, `
		SELECT spent_on, amount_minor, currency, note, category
		FROM finance_entries
		WHERE kind IN ('expense', 'gift')
		  AND spent_on >= $1::date AND spent_on < $2::date
		ORDER BY (currency = 'UZS') DESC, amount_minor DESC
		LIMIT 3`, from, to)
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
		lines = append(lines, fmt.Sprintf("%s · %s — %s", day.Format("02.01"),
			formatMoney(minor, cur), escapeHTML(truncate(firstNonEmpty(note, categoryByKey(cat).Label), 40))))
	}
	if len(lines) < 2 {
		return "" // one entry is not a ranking
	}
	return "\n<b>Eng kattalari</b>\n" + strings.Join(lines, "\n") + "\n"
}

func (b *Bot) debtFooter(ctx context.Context) string {
	balances, err := b.debtBalances(ctx, "")
	if err != nil || len(balances) == 0 {
		return ""
	}
	owedToMe, iOwe, _ := debtTotals(balances)
	return fmt.Sprintf("\n🤝 Sizga qarzdor: %s · Siz: %s — /qarzlar", owedToMe, iOwe)
}

/* ---------------------------- daily summary ------------------------------- */

// financeDailySection is the money part of the 09:00 message: yesterday,
// the month so far, and open debts. Empty until the first entry exists.
func (b *Bot) financeDailySection(ctx context.Context) string {
	var exists bool
	if err := b.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM finance_entries)`).Scan(&exists); err != nil || !exists {
		return ""
	}

	today := dayStart(time.Now())
	yesterday := today.AddDate(0, 0, -1)

	var sb strings.Builder
	sb.WriteString("\n")

	if spent, n, err := b.spentBetween(ctx, yesterday, today); err == nil {
		if n == 0 {
			sb.WriteString("💸 Kecha: hech narsa yozilmagan\n")
		} else {
			sb.WriteString(fmt.Sprintf("💸 Kecha: %s · %d ta\n", spent, n))
		}
	}

	// On the 1st the month so far is empty; the month just finished is the
	// number worth seeing.
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
	from, to, label := monthStart, today.AddDate(0, 0, 1), monthName(today.Month())
	if today.Day() == 1 {
		from, to = monthStart.AddDate(0, -1, 0), monthStart
		label = monthName(from.Month()) + " jami"
	}
	if spent, _, err := b.spentBetween(ctx, from, to); err == nil {
		sb.WriteString(fmt.Sprintf("📆 %s: %s — /hisob\n", label, spent))
	}

	if balances, err := b.debtBalances(ctx, ""); err == nil && len(balances) > 0 {
		owedToMe, iOwe, _ := debtTotals(balances)
		sb.WriteString(fmt.Sprintf("🤝 Sizga qarzdor: %s · Siz: %s\n", owedToMe, iOwe))
	}

	return sb.String()
}

/* -------------------------------- Mini App -------------------------------- */

// webAppKeyboard is the button that opens the reports as a Mini App. The
// page lives in the dashboard app under /tg and signs in with Telegram's
// init data, so pressing it needs no login. Nil when the admin URL is not
// https — Telegram refuses Mini Apps anywhere else.
func (b *Bot) webAppKeyboard() *inlineKeyboard {
	url := b.webAppURL()
	if url == "" {
		return nil
	}
	return &inlineKeyboard{InlineKeyboard: [][]inlineButton{{
		{Text: "📊 Grafiklarni ochish", WebApp: &webAppInfo{URL: url}},
	}}}
}

func (b *Bot) webAppURL() string {
	base := strings.TrimSuffix(b.cfg.AdminSiteURL, "/")
	if !strings.HasPrefix(base, "https://") {
		return ""
	}
	return base + "/tg"
}

package finbot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ibrohimcoder/portfolio-api/internal/finance"
	"github.com/ibrohimcoder/portfolio-api/internal/financetext"
)

// Writing entries, and fixing them.
//
// Any message that is not a command is an entry: "tushlik 35k" is saved at
// once and answered with a card showing what was understood, with a button
// for every field that could have been guessed wrong. Nothing waits for
// confirmation - an entry that needs a second message to exist is an entry
// that does not get written.
//
// Every statement here carries "user_id = $n". Callback data holds row ids,
// and a row id from someone else's chat is simply not found.

type entry struct {
	id          string
	kind        string
	amountMinor int64
	currency    string
	category    string
	person      string
	note        string
	spentOn     time.Time
}

func kindOf(key string) finance.Kind {
	k, _ := finance.KindOf(key)
	return k
}

func categoryByKey(key string) finance.Category { return finance.CategoryByKey(key) }

func formatMoney(minor int64, currency string) string {
	return financetext.FormatMoney(minor, currency)
}

type money = financetext.Money

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func shortError(err error) string {
	s := err.Error()
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

/* -------------------------------- writing --------------------------------- */

func (b *Bot) recordEntry(ctx context.Context, u *user, chatID int64, text string, today time.Time) {
	// Several lines at once - the end-of-day dump - become several rows.
	parts := financetext.Split(text, today)
	if len(parts) > 1 {
		b.recordBatch(ctx, u, chatID, parts, today)
		return
	}

	p, err := financetext.Parse(text, today)
	if err != nil {
		b.noteFailure(ctx, u, text, err)
		b.send(ctx, chatID, parseErrorHelp(err), nil)
		return
	}
	id, err := insertEntry(ctx, b.pool, u.ID, p, text, "bot")
	if err != nil {
		b.send(ctx, chatID, "❌ Saqlanmadi: "+escapeHTML(shortError(err)), nil)
		return
	}
	card, markup := b.entryCard(ctx, u, id)
	b.send(ctx, chatID, card, markup)
}

type execer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func insertEntry(ctx context.Context, q execer, userID int64, p financetext.Entry, raw, source string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `
		INSERT INTO entries
		    (user_id, kind, amount_minor, currency, category, person, note, raw_text, spent_on, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, $10)
		RETURNING id::text`,
		userID, p.Kind, p.AmountMinor, p.Currency, p.Category, p.Person, p.Note, raw,
		p.SpentOn.Format("2006-01-02"), source,
	).Scan(&id)
	return id, err
}

// noteFailure keeps a message the parser could not read, for the admin's
// view of how well people get on with the bot.
func (b *Bot) noteFailure(ctx context.Context, u *user, text string, err error) {
	reason := "other"
	switch {
	case errors.Is(err, financetext.ErrNoAmount):
		reason = "no_amount"
	case errors.Is(err, financetext.ErrNoPerson):
		reason = "no_person"
	}
	if _, xerr := b.pool.Exec(ctx, `INSERT INTO parse_failures (user_id, text, reason) VALUES ($1, $2, $3)`,
		u.ID, truncate(text, 300), reason); xerr != nil {
		log.Printf("finbot: could not record parse failure: %v", xerr)
	}
}

func parseErrorHelp(err error) string {
	switch {
	case errors.Is(err, financetext.ErrNoAmount):
		return strings.Join([]string{
			"Summani topolmadim - hech narsa yozilmadi.",
			"",
			"Masalan:",
			"<code>tushlik 35k</code>",
			"<code>kecha taksi 18000</code>",
			"<code>+10mln oylik</code>",
			"<code>Aliga qarz 500 ming</code>",
			"<code>Ali qaytardi 200k</code>",
			"",
			"Buyruqlar: /help",
		}, "\n")
	case errors.Is(err, financetext.ErrNoPerson):
		return strings.Join([]string{
			"Kimligini topolmadim - hech narsa yozilmadi.",
			"",
			"Ismni qo'shimchasi bilan yozing:",
			"<code>Aliga qarz 500k</code> - siz berdingiz",
			"<code>Alidan qarz oldim 500k</code> - siz oldingiz",
			"<code>Ali qaytardi 200k</code> - sizga qaytardi",
			"<code>Aliga qaytardim 200k</code> - siz qaytardingiz",
		}, "\n")
	}
	return "❌ Tushunmadim: " + escapeHTML(err.Error())
}

func (b *Bot) recordBatch(ctx context.Context, u *user, chatID int64, parts []string, today time.Time) {
	var (
		sb      strings.Builder
		buttons [][]inlineButton
		row     []inlineButton
		failed  []string
		saved   int
		spent   = money{}
	)
	for _, part := range parts {
		p, err := financetext.Parse(part, today)
		if err != nil {
			b.noteFailure(ctx, u, part, err)
			failed = append(failed, part)
			continue
		}
		id, err := insertEntry(ctx, b.pool, u.ID, p, part, "bot")
		if err != nil {
			failed = append(failed, part)
			continue
		}
		saved++
		if finance.IsSpending(p.Kind) {
			spent.Add(p.Currency, p.AmountMinor)
		}
		label := firstNonEmpty(p.Note, p.Person, categoryByKey(p.Category).Label)
		if finance.IsDebt(p.Kind) {
			label = p.Person
		}
		sb.WriteString(fmt.Sprintf("%d. %s <b>%s</b> - %s %s\n", saved, kindOf(p.Kind).Emoji,
			formatMoney(p.AmountMinor, p.Currency), escapeHTML(truncate(label, 40)),
			categoryByKey(p.Category).Emoji))
		if !financetext.SameDay(p.SpentOn, today) {
			sb.WriteString("    📅 " + financetext.DayLabel(p.SpentOn, today) + "\n")
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
	if !spent.IsZero() {
		head += " · sarf " + spent.String()
	}
	text := head + "\n\n" + sb.String()
	if len(failed) > 0 {
		text += "\n❌ <b>Yozilmadi</b> - summa yoki ism topilmadi:\n"
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
	b.send(ctx, chatID, text, markup)
}

/* --------------------------------- card ----------------------------------- */

func validUUID(id string) bool {
	return len(id) == 36 && strings.Trim(strings.ToLower(id), "0123456789abcdef-") == ""
}

func (b *Bot) loadEntry(ctx context.Context, u *user, id string) (entry, error) {
	var e entry
	if !validUUID(id) {
		return e, pgx.ErrNoRows
	}
	err := b.pool.QueryRow(ctx, `
		SELECT id::text, kind, amount_minor, currency, category, person, note, spent_on
		FROM entries
		WHERE id = $1::uuid AND user_id = $2`, id, u.ID,
	).Scan(&e.id, &e.kind, &e.amountMinor, &e.currency, &e.category, &e.person, &e.note, &e.spentOn)
	return e, err
}

// entryCard is the row as it is in the database, plus the buttons to correct
// it. The same card is redrawn after every change.
func (b *Bot) entryCard(ctx context.Context, u *user, id string) (string, *inlineKeyboard) {
	e, err := b.loadEntry(ctx, u, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "Yozuv topilmadi - o'chirilgan bo'lishi mumkin.\n\n/oxirgi", nil
	}
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(shortError(err)), nil
	}

	today := financetext.DayStart(b.now())
	k := kindOf(e.kind)
	c := categoryByKey(e.category)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s <b>%s</b> · %s %s\n", k.Emoji, k.Label, c.Emoji, c.Label))
	sb.WriteString("<b>" + formatMoney(e.amountMinor, e.currency) + "</b>")
	if e.note != "" {
		sb.WriteString(" - " + escapeHTML(e.note))
	}
	sb.WriteString("\n")
	if e.person != "" {
		sb.WriteString("👤 " + escapeHTML(e.person) + "\n")
	}
	sb.WriteString("📅 " + financetext.DayLabel(e.spentOn, today) + "\n")

	switch {
	case finance.IsDebt(e.kind) && e.person != "":
		sb.WriteString("\n" + b.personBalanceLine(ctx, u, e.person))
	case finance.IsSpending(e.kind):
		if day, _, err := b.spentBetween(ctx, u, e.spentOn, e.spentOn.AddDate(0, 0, 1)); err == nil {
			label := "Bugun"
			if !financetext.SameDay(e.spentOn, today) {
				label = e.spentOn.Format("02.01")
			}
			sb.WriteString(fmt.Sprintf("\n%s jami: %s", label, day))
		}
	}

	if bal, err := b.balanceNow(ctx, u, e.currency); err == nil {
		if bal.checkpoint == nil {
			sb.WriteString("\n<i>Balansni boshlash: /balans 5mln</i>")
		} else {
			sb.WriteString(fmt.Sprintf("\n💼 Qoldi: <b>%s</b>", formatMoney(bal.minor, e.currency)))
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

func categoryPicker(e entry) (string, *inlineKeyboard) {
	var rows [][]inlineButton
	var row []inlineButton
	for _, c := range finance.CategoriesFor(e.kind) {
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
	text := fmt.Sprintf("🏷 <b>Kategoriya</b>\n%s - %s",
		formatMoney(e.amountMinor, e.currency), escapeHTML(firstNonEmpty(e.note, "izohsiz")))
	return text, &inlineKeyboard{InlineKeyboard: rows}
}

func kindPicker(e entry) (string, *inlineKeyboard) {
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
		formatMoney(e.amountMinor, e.currency) + " - " + escapeHTML(firstNonEmpty(e.note, "izohsiz")),
		"",
		"<i>Qarz turlari kishi bo'yicha hisoblanadi: berdim - qaytdi - oldim + qaytardim.</i>",
	}, "\n")
	return text, &inlineKeyboard{InlineKeyboard: rows}
}

/* ------------------------------- callbacks -------------------------------- */

func (b *Bot) financeCallback(ctx context.Context, u *user, chatID int64, action, arg string) (string, redrawFunc) {
	switch action {
	case "fc":
		key, id, _ := strings.Cut(arg, ":")
		return b.setCategory(ctx, u, id, key), b.cardRedraw(u, id)
	case "fk":
		code, id, _ := strings.Cut(arg, ":")
		return b.setKind(ctx, u, id, finance.KindByCode(code)), b.cardRedraw(u, id)
	}

	op, id, _ := strings.Cut(arg, ":")
	switch op {
	case "card":
		return "", b.cardRedraw(u, id)
	case "list":
		return "", func(c context.Context) (string, *inlineKeyboard) { return b.recentList(c, u) }
	case "cat", "kind":
		e, err := b.loadEntry(ctx, u, id)
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
		e, err := b.loadEntry(ctx, u, id)
		if err != nil {
			return "Yozuv topilmadi", nil
		}
		return "", func(context.Context) (string, *inlineKeyboard) {
			return fmt.Sprintf("🗑 <b>O'chirilsinmi?</b>\n%s - %s · %s",
					formatMoney(e.amountMinor, e.currency),
					escapeHTML(firstNonEmpty(e.note, kindOf(e.kind).Label)),
					e.spentOn.Format("02.01")),
				&inlineKeyboard{InlineKeyboard: [][]inlineButton{{
					{Text: "Ha, o'chirish", CallbackData: "fn:delok:" + id},
					{Text: "Bekor qilish", CallbackData: "fn:card:" + id},
				}}}
		}
	case "delok":
		if !validUUID(id) {
			return "Yozuv topilmadi", nil
		}
		var amount int64
		var currency, note string
		err := b.pool.QueryRow(ctx, `
			DELETE FROM entries WHERE id = $1::uuid AND user_id = $2
			RETURNING amount_minor, currency, note`, id, u.ID).Scan(&amount, &currency, &note)
		if errors.Is(err, pgx.ErrNoRows) {
			return "Allaqachon o'chirilgan", nil
		}
		if err != nil {
			return "Xato: " + shortError(err), nil
		}
		text := fmt.Sprintf("🗑 <s>%s - %s</s>\nO'chirildi.",
			formatMoney(amount, currency), escapeHTML(firstNonEmpty(note, "izohsiz")))
		return "O'chirildi", func(context.Context) (string, *inlineKeyboard) { return text, nil }
	case "amt", "who", "date", "note":
		return b.askField(ctx, u, chatID, op, id), nil
	}
	return "Noma'lum tugma", nil
}

func (b *Bot) cardRedraw(u *user, id string) redrawFunc {
	return func(c context.Context) (string, *inlineKeyboard) { return b.entryCard(c, u, id) }
}

func (b *Bot) setCategory(ctx context.Context, u *user, id, key string) string {
	c := categoryByKey(key)
	if c.Key != key {
		return "Noma'lum kategoriya"
	}
	e, err := b.loadEntry(ctx, u, id)
	if err != nil {
		return "Yozuv topilmadi"
	}
	if !finance.CategoryFits(e.kind, key) {
		return "Bu turga mos emas"
	}
	if _, err := b.pool.Exec(ctx, `
		UPDATE entries SET category = $3, updated_at = now()
		WHERE id = $1::uuid AND user_id = $2`, id, u.ID, key); err != nil {
		return "Xato: " + shortError(err)
	}
	return c.Label
}

func (b *Bot) setKind(ctx context.Context, u *user, id, kind string) string {
	if _, ok := finance.KindOf(kind); !ok {
		return "Noma'lum tur"
	}
	e, err := b.loadEntry(ctx, u, id)
	if err != nil {
		return "Yozuv topilmadi"
	}
	// A row leaving debt gets its category guessed again from its note
	// rather than keeping "qarz".
	category := e.category
	if !finance.CategoryFits(kind, category) {
		category = financetext.GuessCategoryFromNote(kind, e.note)
	}
	if _, err := b.pool.Exec(ctx, `
		UPDATE entries SET kind = $3, category = $4, updated_at = now()
		WHERE id = $1::uuid AND user_id = $2`, id, u.ID, kind, category); err != nil {
		return "Xato: " + shortError(err)
	}
	if finance.IsDebt(kind) && e.person == "" {
		return "Endi 👤 Kim tugmasi bilan ismni yozing"
	}
	return kindOf(kind).Label
}

/* ------------------------- edits by question/answer ------------------------ */

// How long an unanswered question stays open.
const promptTTL = 2 * time.Hour

var editFields = map[string]struct {
	intent, question, placeholder string
}{
	"amt":  {"fin.amount", "💰 <b>Summa</b> - yangi summani javob qilib yuboring.\n\n<i>35k, 1.5 mln, 250 000, $20</i>", "35k"},
	"who":  {"fin.person", "👤 <b>Kim</b> - ismni javob qilib yuboring.\n\n<i>O'chirish uchun <code>-</code> yuboring.</i>", "Ali"},
	"date": {"fin.date", "📅 <b>Sana</b> - javob qilib yuboring.\n\n<i><code>bugun</code>, <code>kecha</code>, <code>12.10</code> yoki <code>12.10.2026</code></i>", "kecha"},
	"note": {"fin.note", "📝 <b>Izoh</b> - yangi izohni javob qilib yuboring.\n\n<i>O'chirish uchun <code>-</code> yuboring.</i>", "tushlik"},
}

func (b *Bot) askField(ctx context.Context, u *user, chatID int64, op, id string) string {
	f, ok := editFields[op]
	if !ok {
		return "Noma'lum maydon"
	}
	e, err := b.loadEntry(ctx, u, id)
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
	b.ask(ctx, u, chatID, f.intent, id, text, f.placeholder)
	return "Javob yozing"
}

func (b *Bot) ask(ctx context.Context, u *user, chatID int64, intent, targetID, text, placeholder string) {
	messageID, err := b.tg.sendPrompt(ctx, chatID, text, placeholder)
	if err != nil {
		log.Printf("finbot: could not send prompt (%s): %v", intent, err)
		return
	}
	if _, err := b.pool.Exec(ctx, `
		INSERT INTO prompts (chat_id, message_id, user_id, intent, target_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + make_interval(secs => $6))
		ON CONFLICT (chat_id, message_id) DO UPDATE
		SET intent = EXCLUDED.intent, target_id = EXCLUDED.target_id,
		    user_id = EXCLUDED.user_id, expires_at = EXCLUDED.expires_at`,
		chatID, messageID, u.ID, intent, targetID, promptTTL.Seconds()); err != nil {
		log.Printf("finbot: could not record prompt (%s): %v", intent, err)
	}
}

// answerPrompt applies a reply to the question it answers. ok is false when
// the reply was not to one of the bot's questions; the message is then an
// ordinary one.
func (b *Bot) answerPrompt(ctx context.Context, u *user, chatID, replyTo int64, text string, today time.Time) (string, *inlineKeyboard, bool) {
	var intent, targetID string
	err := b.pool.QueryRow(ctx, `
		DELETE FROM prompts
		WHERE chat_id = $1 AND message_id = $2 AND user_id = $3 AND expires_at > now()
		RETURNING intent, target_id`, chatID, replyTo, u.ID).Scan(&intent, &targetID)
	if err != nil {
		return "", nil, false
	}
	if text == "" {
		return "Bo'sh javob - hech narsa o'zgarmadi.", nil, true
	}
	if intent == "bal.set" {
		// The first-run balance question: the same as typing /balans 5mln.
		return b.balanceCommand(ctx, u, text), b.webAppKeyboard("📊 Hisobotni ochish"), true
	}
	reply, markup := b.applyEdit(ctx, u, intent, targetID, text, today)
	return reply, markup, true
}

func (b *Bot) applyEdit(ctx context.Context, u *user, intent, id, value string, today time.Time) (string, *inlineKeyboard) {
	if !validUUID(id) {
		return "Yozuv topilmadi.", nil
	}
	var (
		sql  string
		args []any
	)
	clear := value == "-"
	switch intent {
	case "fin.amount":
		minor, currency, ok := financetext.ParseAmount(value)
		if !ok {
			return "Summani tushunmadim - o'zgarmadi. Masalan: <code>35k</code>, <code>1.5 mln</code>, <code>$20</code>", nil
		}
		sql, args = `UPDATE entries SET amount_minor = $3, currency = $4, updated_at = now()`, []any{minor, currency}
	case "fin.person":
		person := ""
		if !clear {
			person = truncate(financetext.Capitalise(financetext.CleanOriginal(value)), maxPerson)
		}
		sql, args = `UPDATE entries SET person = $3, updated_at = now()`, []any{person}
	case "fin.date":
		day, ok := financetext.ParseDay(value, today)
		if !ok {
			return "Sanani tushunmadim - o'zgarmadi. Masalan: <code>kecha</code> yoki <code>12.10</code>", nil
		}
		sql, args = `UPDATE entries SET spent_on = $3::date, updated_at = now()`, []any{day.Format("2006-01-02")}
	case "fin.note":
		note := ""
		if !clear {
			note = truncate(value, maxNote)
		}
		sql, args = `UPDATE entries SET note = $3, updated_at = now()`, []any{note}
	default:
		return "Noma'lum maydon", nil
	}

	tag, err := b.pool.Exec(ctx, sql+` WHERE id = $1::uuid AND user_id = $2`, append([]any{id, u.ID}, args...)...)
	if err != nil {
		return "❌ Saqlanmadi: " + escapeHTML(shortError(err)), nil
	}
	if tag.RowsAffected() == 0 {
		return "Yozuv topilmadi - o'chirilgan bo'lishi mumkin.", nil
	}
	card, markup := b.entryCard(ctx, u, id)
	return "✏️ <i>Saqlandi</i>\n\n" + card, markup
}

/* ------------------------------ delete my data ----------------------------- */

func (b *Bot) askDeleteAll(ctx context.Context, u *user, chatID int64) {
	var entries, checkpoints int64
	_ = b.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM entries WHERE user_id = $1),
		       (SELECT count(*) FROM checkpoints WHERE user_id = $1)`, u.ID).Scan(&entries, &checkpoints)
	b.send(ctx, chatID, fmt.Sprintf(strings.Join([]string{
		"⚠️ <b>Barcha ma'lumotlaringiz o'chirilsinmi?</b>",
		"",
		"%d ta yozuv va %d ta balans tekshiruvi butunlay o'chadi. Buni qaytarib bo'lmaydi.",
	}, "\n"), entries, checkpoints), &inlineKeyboard{InlineKeyboard: [][]inlineButton{{
		{Text: "Ha, hammasini o'chirish", CallbackData: "del:all"},
		{Text: "Bekor qilish", CallbackData: "del:no"},
	}}})
}

func (b *Bot) deleteAllCallback(ctx context.Context, u *user, arg string) (string, redrawFunc) {
	if arg != "all" {
		return "Bekor qilindi", func(context.Context) (string, *inlineKeyboard) {
			return "Hech narsa o'chirilmadi.", nil
		}
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return "Xato: " + shortError(err), nil
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	tag, err := tx.Exec(ctx, `DELETE FROM entries WHERE user_id = $1`, u.ID)
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM checkpoints WHERE user_id = $1`, u.ID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM prompts WHERE user_id = $1`, u.ID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM parse_failures WHERE user_id = $1`, u.ID)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return "Xato: " + shortError(err), nil
	}
	n := tag.RowsAffected()
	return "O'chirildi", func(context.Context) (string, *inlineKeyboard) {
		return fmt.Sprintf("🗑 %d ta yozuv va barcha balans tekshiruvlari o'chirildi.\n\nYangidan boshlash: <code>/balans 5mln</code>", n), nil
	}
}

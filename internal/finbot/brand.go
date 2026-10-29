package finbot

import (
	"context"
	"log"
	"strings"
)

// How the bot presents itself: its name and descriptions in Telegram, the
// menu under the input box, the first-run walkthrough and the help text.
//
// The profile is set from code on every start (like the command menu), so
// what people see always matches the build. A change made by hand in
// BotFather is overwritten at the next restart - change it here instead.
// The profile picture is the one thing the Bot API cannot set: upload
// deploy/finbot/avatar.png with BotFather -> /setuserpic.

const (
	brandName = "Hamyon · pul hisobi"

	// Profile page and share card: 120 characters at most.
	brandShortDescription = "Xarajat, kirim va qarzlar - bitta xabar bilan. Hisobot va grafiklar o'zi tayyor bo'ladi."

	// The empty chat, before the first /start: 512 characters at most.
	brandDescription = `👛 Hamyon - pulingiz qayerga ketayotganini ko'rsatadigan shaxsiy hisobchi.

Shunchaki yozing:
☕ kofe 25k
🚕 kecha taksi 18 000
💰 +12mln oylik
🤝 Aliga qarz 500k

Bot o'zi tushunadi, kategoriyaga ajratadi va hisoblab boradi:
📊 oylik hisobot va grafiklar
💼 qo'ldagi pul
🤝 kim kimga qarzdor
☀️ har tongda qisqa xulosa`
)

// syncProfile makes the name and descriptions match the constants above.
func (b *Bot) syncProfile(ctx context.Context) {
	for _, f := range []struct{ get, set, field, value string }{
		{"getMyName", "setMyName", "name", brandName},
		{"getMyShortDescription", "setMyShortDescription", "short_description", brandShortDescription},
		{"getMyDescription", "setMyDescription", "description", brandDescription},
	} {
		changed, err := b.tg.syncField(ctx, f.get, f.set, f.field, f.value)
		switch {
		case err != nil:
			log.Printf("finbot: could not set %s: %v", f.field, err)
		case changed:
			log.Printf("finbot: %s updated", f.field)
		}
	}
}

/* ---------------------------------- menu ---------------------------------- */

// The buttons under the input box. Each sends its label as a message, which
// handleMessage turns into the command beside it - so a person who never
// learns a single "/" command still reaches every report.
var menuButtons = [][]struct{ label, command string }{
	{{"📊 Hisobot", "/hisob"}, {"💼 Balans", "/balans"}},
	{{"🤝 Qarzlar", "/qarzlar"}, {"📋 Oxirgi yozuvlar", "/oxirgi"}},
	{{"❓ Qanday yozaman", "/help"}},
}

func menuCommand(text string) (string, bool) {
	for _, row := range menuButtons {
		for _, btn := range row {
			if text == btn.label {
				return btn.command, true
			}
		}
	}
	return "", false
}

func mainKeyboard() *replyKeyboard {
	kb := &replyKeyboard{
		IsPersistent:          true,
		ResizeKeyboard:        true,
		InputFieldPlaceholder: "Masalan: tushlik 35k",
	}
	for _, row := range menuButtons {
		var r []keyboardButton
		for _, btn := range row {
			r = append(r, keyboardButton{Text: btn.label})
		}
		kb.Keyboard = append(kb.Keyboard, r)
	}
	return kb
}

func (b *Bot) sendWithMenu(ctx context.Context, chatID int64, text string) {
	if err := b.tg.sendMessageWithKeyboard(ctx, chatID, text, mainKeyboard()); err != nil {
		log.Printf("finbot: sendMessage (menu) failed: %v", err)
	}
}

/* ------------------------------- first run -------------------------------- */

// sendWelcome greets someone who has just been let in (first) or presses
// /start again. Two messages: the welcome carries the menu keyboard, and
// the second carries the one step worth doing first - a message can hold
// only one kind of keyboard.
func (b *Bot) sendWelcome(ctx context.Context, chatID int64, isAdmin, first bool) {
	if !first {
		b.sendWithMenu(ctx, chatID, strings.Join([]string{
			"👛 <b>Hamyon</b> shu yerda.",
			"",
			"Yozishda davom eting yoki pastdagi menyudan hisobotni oching.",
		}, "\n"))
		return
	}

	text := strings.Join([]string{
		"👋 <b>Hamyon'ga xush kelibsiz!</b>",
		"",
		"Pul harakatini oddiy xabar bilan yozasiz - qolganini men qilaman: kategoriyaga ajrataman, hisoblayman, grafik chizaman.",
		"",
		"<blockquote><code>tushlik 35k</code>\n<code>+12mln oylik</code>\n<code>Aliga qarz 500k</code></blockquote>",
		"",
		b.privacyLine(),
	}, "\n")
	if isAdmin {
		text += "\n\n<i>Admin: /taklif - do'stingizga havola, /foydalanuvchilar - ro'yxat.</i>"
	}
	b.sendWithMenu(ctx, chatID, text)

	b.send(ctx, chatID, strings.Join([]string{
		"<b>1-qadam · balans</b>",
		"",
		"Hozir qo'lingizda (karta + naqd) qancha pul bor? Bir marta yozsangiz, shundan keyin har kirim qo'shiladi, har xarajat ayriladi va <b>qoldig'ini doim ko'rib turasiz</b>.",
	}, "\n"), &inlineKeyboard{InlineKeyboard: [][]inlineButton{
		{{Text: "💼 Balansni kiritish", CallbackData: "ob:bal"}},
		{{Text: "📖 Misollar", CallbackData: "ob:ex"}, {Text: "⏭ Keyinroq", CallbackData: "ob:skip"}},
	}})
}

// onboardingCallback handles the first-run buttons.
func (b *Bot) onboardingCallback(ctx context.Context, u *user, chatID int64, arg string) (string, redrawFunc) {
	switch arg {
	case "bal":
		b.ask(ctx, u, chatID, "bal.set", "", strings.Join([]string{
			"💼 <b>Qo'lingizda qancha bor?</b>",
			"",
			"Shu xabarga javob qilib summani yozing:",
			"<code>5mln</code> · <code>4 700 000</code> · <code>$300</code>",
		}, "\n"), "5mln")
		return "Summani yozing", nil
	case "ex":
		return "", func(context.Context) (string, *inlineKeyboard) {
			return examplesText(), &inlineKeyboard{InlineKeyboard: [][]inlineButton{
				{{Text: "💼 Balansni kiritish", CallbackData: "ob:bal"}},
			}}
		}
	case "skip":
		return "", func(context.Context) (string, *inlineKeyboard) {
			return "Mayli! Balansni istalgan payt yozish mumkin: <code>/balans 5mln</code>\n\nHozir birinchi xarajatingizni yozib ko'ring 👇", nil
		}
	}
	return "Eski tugma", nil
}

func examplesText() string {
	return strings.Join([]string{
		"📖 <b>Qanday yoziladi</b>",
		"",
		"<b>💸 Xarajat</b>",
		"<blockquote><code>tushlik 35k</code>\n<code>korzinka 245 000</code>\n<code>kecha taksi 18k</code></blockquote>",
		"<b>💰 Kirim</b> - oldiga <b>+</b>",
		"<blockquote><code>+12mln oylik</code>\n<code>+500$ freelance</code></blockquote>",
		"<b>🤝 Qarz</b> - ismni qo'shimchasi bilan",
		"<blockquote><code>Aliga qarz 500k</code> - siz berdingiz\n<code>Alidan qarz oldim 1mln</code> - siz oldingiz\n<code>Ali qaytardi 200k</code>\n<code>Aliga qaytardim 200k</code></blockquote>",
		"<b>🎁 Sovg'a, yordam</b>",
		"<blockquote><code>Sardorga 100k berdim to'yga</code></blockquote>",
		"Bir nechtasini alohida qatorlarda bitta xabarda yuborsa ham bo'ladi. Sana: <code>kecha</code>, <code>o'tgan kuni</code>, <code>12.10</code>.",
		"",
		"Noto'g'ri tushunsam - yozuv kartasidagi tugma bilan bir bosishda tuzatasiz.",
	}, "\n")
}

/* ---------------------------------- help ---------------------------------- */

func (b *Bot) helpText(isAdmin bool) string {
	lines := []string{
		examplesText(),
		"",
		"<b>⌨️ Buyruqlar</b> - ular pastdagi menyuda ham bor",
		"<blockquote expandable>/hisob - shu oy · <code>/hisob hafta</code> · <code>o'tgan</code> · <code>yil</code> · <code>sentabr</code>",
		"/balans - qo'ldagi pul · <code>/balans 5mln</code> - haqiqiy summani yozish",
		"/qarzlar - ochiq qarzlar · <code>/qarzlar Ali</code> - bitta odam tarixi",
		"/oxirgi - oxirgi 10 ta yozuv, tuzatish va o'chirish",
		"/app - grafiklar, filtr va tahrirlash (Telegram ichida)",
		"/xulosa off - ertalabki xabarni o'chirish",
		"/ochirish - barcha ma'lumotlarimni o'chirish</blockquote>",
		"",
		b.privacyLine(),
	}
	if isAdmin {
		lines = append(lines, "",
			"<b>Admin</b>",
			"<blockquote>/taklif [izoh] - bir martalik havola (7 kun)",
			"/foydalanuvchilar - ro'yxat va kutayotganlar",
			"/ruxsat &lt;telegram id&gt; · /bloklash &lt;telegram id&gt;</blockquote>",
		)
	}
	return strings.Join(lines, "\n")
}

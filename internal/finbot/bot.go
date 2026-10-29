// Package finbot is a stand-alone money log for several people: a Telegram
// bot that turns "tushlik 35k" into a row, and a small web API behind the
// Telegram Mini App that draws the same rows as charts.
//
// It is a different product from the portfolio's ops bot (internal/bot) and
// is kept apart from it on every axis that matters for privacy:
//
//   - its own Telegram bot and token (FINBOT_TOKEN);
//   - its own database (FINBOT_DATABASE_URL) with its own migrations
//     (db/finbot), so the portfolio API and dashboard cannot read it;
//   - every row carries user_id, and every query filters on it.
//
// What it shares with the ops bot is code that has no data in it: the
// catalogue of kinds and categories (internal/finance), the line parser
// (internal/financetext), and the Telegram init-data check (internal/tgauth).
//
// Access is by invitation. The admin (FINBOT_ADMIN_ID) sends /taklif and gets
// a one-time link; whoever opens it is in. Someone who finds the bot on their
// own can ask with /start, and the admin gets Allow / Refuse buttons. The
// admin approves people; the admin does not see their money through the bot.
package finbot

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	Token string
	// The one Telegram account that approves people and makes invite links.
	AdminID int64
	// Where the Mini App lives (https only, Telegram refuses anything else),
	// e.g. https://hisob.fayzullayev.uz/pul. Empty: no web app buttons.
	WebAppURL string
	// Address of the web API, e.g. ":8090". Empty: no HTTP server.
	HTTPAddr string
	// Browser origins allowed to call the web API directly (local
	// development). In production the page and the API share an origin
	// through Caddy and this stays empty.
	CORSOrigins []string

	// The admin view in the portfolio dashboard. AdminKey is the shared
	// secret the portfolio API sends on /internal/admin/* (over the Docker
	// network only; Caddy never routes /internal). Empty: no admin API.
	AdminKey string
	// Whether that view includes money: amounts, reports, entries. When
	// false the admin sees who uses the bot and how often, never how much.
	// Users are told which of the two it is (privacyLine).
	AdminSeesMoney bool
}

type Bot struct {
	cfg      Config
	tg       *telegramClient
	pool     *pgxpool.Pool
	username string // the bot's @name, for invite links
	now      func() time.Time
}

func New(cfg Config, pool *pgxpool.Pool) *Bot {
	return &Bot{cfg: cfg, tg: newTelegramClient(cfg.Token), pool: pool, now: time.Now}
}

// Run long-polls Telegram until ctx is cancelled. The web API and the daily
// summary run beside it.
func (b *Bot) Run(ctx context.Context) error {
	me, err := b.tg.getMe(ctx)
	if err != nil {
		return err
	}
	b.username = me.Username
	log.Printf("finbot: connected as @%s (admin %d)", me.Username, b.cfg.AdminID)

	// Menus are set from code on every start, so they always match the build.
	if err := b.tg.setMyCommands(ctx, userCommands(), 0); err != nil {
		log.Printf("finbot: could not set the command menu: %v", err)
	}
	if b.cfg.AdminID != 0 {
		if err := b.tg.setMyCommands(ctx, append(userCommands(), adminCommands()...), b.cfg.AdminID); err != nil {
			log.Printf("finbot: could not set the admin command menu: %v", err)
		}
	}
	if err := b.tg.setChatMenuButton(ctx, b.menuButton()); err != nil {
		log.Printf("finbot: could not set the menu button: %v", err)
	}

	go b.runScheduler(ctx)

	offset := b.loadOffset(ctx)
	var backoff time.Duration
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		updates, err := b.tg.getUpdates(ctx, offset, 30)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return ctx.Err()
			}
			backoff = nextBackoff(backoff)
			log.Printf("finbot: getUpdates failed (%v), retrying in %s", err, backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			continue
		}
		backoff = 0

		for _, u := range updates {
			b.handle(ctx, u)
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
				// Saved after every update: messages sent while the bot was
				// down are money too, so on restart they are read, not
				// skipped - and nothing already read is read twice.
				b.saveOffset(ctx, offset)
			}
		}
	}
}

func nextBackoff(d time.Duration) time.Duration {
	switch {
	case d == 0:
		return 2 * time.Second
	case d >= 60*time.Second:
		return 60 * time.Second
	default:
		return d * 2
	}
}

func (b *Bot) loadOffset(ctx context.Context) int64 {
	var v int64
	err := b.pool.QueryRow(ctx, `SELECT value FROM bot_state WHERE key = 'offset'`).Scan(&v)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		log.Printf("finbot: could not read the update offset: %v", err)
	}
	return v
}

func (b *Bot) saveOffset(ctx context.Context, offset int64) {
	if _, err := b.pool.Exec(ctx, `
		INSERT INTO bot_state (key, value) VALUES ('offset', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, offset); err != nil {
		log.Printf("finbot: could not save the update offset: %v", err)
	}
}

/* -------------------------------- dispatch -------------------------------- */

func (b *Bot) handle(ctx context.Context, u Update) {
	defer func() {
		// One bad message must not stop the bot for everyone else.
		if r := recover(); r != nil {
			log.Printf("finbot: panic while handling update %d: %v", u.UpdateID, r)
		}
	}()
	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	if u.CallbackQuery != nil {
		b.handleCallback(cctx, u.CallbackQuery)
		return
	}
	msg := u.Message
	if msg == nil || msg.From == nil || msg.Chat == nil || msg.From.IsBot {
		return
	}
	// Private chats only: in a group, a money report would be read by
	// everyone in it.
	if msg.Chat.Type != "" && msg.Chat.Type != "private" {
		return
	}
	b.handleMessage(cctx, msg)
}

func (b *Bot) handleMessage(ctx context.Context, msg *Message) {
	chatID := msg.Chat.ID
	text := strings.TrimSpace(msg.Text)
	fields := strings.Fields(text)
	cmd := ""
	if len(fields) > 0 && strings.HasPrefix(fields[0], "/") {
		cmd = strings.ToLower(fields[0])
		if i := strings.Index(cmd, "@"); i != -1 {
			cmd = cmd[:i]
		}
	}
	arg := ""
	if cmd != "" {
		arg = strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	}

	if cmd == "/start" {
		b.handleStart(ctx, msg, arg)
		return
	}

	u, err := b.userByTelegram(ctx, msg.From.ID)
	if err != nil {
		log.Printf("finbot: user lookup failed: %v", err)
		b.send(ctx, chatID, "❌ Hozir javob bera olmayman, birozdan keyin urinib ko'ring.", nil)
		return
	}
	switch {
	case u == nil:
		b.send(ctx, chatID, "Assalomu alaykum! Boshlash uchun /start bosing.", nil)
		return
	case u.Status == statusBlocked:
		return // no answer at all
	case u.Status == statusPending:
		b.send(ctx, chatID, "⏳ So'rovingiz hali ko'rib chiqilmoqda. Ruxsat berilgach shu yerga xabar keladi.", nil)
		return
	}
	b.touch(ctx, u)

	// The day the message was written, not the day it is read: after a
	// restart the bot reads what was sent while it was down, and
	// "tushlik 35k" from last night belongs to last night.
	today := b.dayOf(msg.Date)

	if msg.ReplyToMessage != nil {
		if reply, markup, ok := b.answerPrompt(ctx, u, chatID, msg.ReplyToMessage.MessageID, text, today); ok {
			b.send(ctx, chatID, reply, markup)
			return
		}
	}
	if len(fields) == 0 {
		return // a sticker, a photo
	}

	if cmd == "" {
		b.recordEntry(ctx, u, chatID, text, today)
		return
	}

	isAdmin := msg.From.ID == b.cfg.AdminID
	switch cmd {
	case "/help", "/yordam":
		b.send(ctx, chatID, b.helpText(isAdmin), nil)
	case "/oxirgi":
		t, m := b.recentList(ctx, u)
		b.send(ctx, chatID, t, m)
	case "/hisob":
		b.send(ctx, chatID, b.financeReport(ctx, u, arg, today), b.webAppKeyboard("📊 Grafiklarni ochish"))
	case "/qarzlar", "/qarz":
		b.send(ctx, chatID, b.debtsReport(ctx, u, arg, today), b.webAppKeyboard("🤝 Qarzlarni ochish"))
	case "/balans":
		b.send(ctx, chatID, b.balanceCommand(ctx, u, arg), b.webAppKeyboard("💼 Balans tarixi"))
	case "/app", "/hisobot":
		b.send(ctx, chatID, "📊 <b>Hisobot</b> - grafiklar, kategoriyalar, qarzlar va barcha yozuvlar.",
			b.webAppKeyboard("📊 Ochish"))
	case "/xulosa":
		b.send(ctx, chatID, b.summarySetting(ctx, u, arg, today), nil)
	case "/ochirish":
		b.askDeleteAll(ctx, u, chatID)
	case "/taklif", "/foydalanuvchilar", "/ruxsat", "/bloklash":
		if !isAdmin {
			b.send(ctx, chatID, "Bunday buyruq yo'q. /help", nil)
			return
		}
		b.adminCommand(ctx, chatID, cmd, arg)
	default:
		b.send(ctx, chatID, "Bunday buyruq yo'q. /help", nil)
	}
}

func (b *Bot) handleCallback(ctx context.Context, q *CallbackQuery) {
	if q.From == nil {
		return
	}
	chatID := q.From.ID
	if q.Message != nil && q.Message.Chat != nil {
		chatID = q.Message.Chat.ID
	}
	action, arg, _ := strings.Cut(q.Data, ":")

	var (
		notice string
		redraw redrawFunc
	)
	if action == "adm" {
		if q.From.ID != b.cfg.AdminID {
			return
		}
		notice, redraw = b.adminCallback(ctx, arg)
	} else {
		u, err := b.userByTelegram(ctx, q.From.ID)
		if err != nil || u == nil || u.Status != statusActive {
			_ = b.tg.answerCallback(ctx, q.ID, "Ruxsat yo'q")
			return
		}
		b.touch(ctx, u)
		switch action {
		case "fn", "fc", "fk":
			notice, redraw = b.financeCallback(ctx, u, chatID, action, arg)
		case "del":
			notice, redraw = b.deleteAllCallback(ctx, u, arg)
		default:
			notice = "Eski tugma"
		}
	}

	// Always answer, even with nothing to say: an unanswered callback leaves
	// the button spinning.
	if err := b.tg.answerCallback(ctx, q.ID, notice); err != nil {
		log.Printf("finbot: answerCallback failed: %v", err)
	}
	if redraw != nil && q.Message != nil && q.Message.Chat != nil {
		text, markup := redraw(ctx)
		if err := b.tg.editMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, markup); err != nil {
			log.Printf("finbot: editMessageText failed: %v", err)
		}
	}
}

type redrawFunc func(context.Context) (string, *inlineKeyboard)

func (b *Bot) send(ctx context.Context, chatID int64, text string, markup *inlineKeyboard) {
	if err := b.tg.sendMessage(ctx, chatID, text, markup); err != nil {
		log.Printf("finbot: sendMessage failed: %v", err)
	}
}

/* ---------------------------------- menus --------------------------------- */

func userCommands() []botCommand {
	return []botCommand{
		{Command: "hisob", Description: "Shu oy: kirim, sarf, qoldi, kategoriyalar"},
		{Command: "balans", Description: "Qo'ldagi pul; /balans 5mln - tekshirish"},
		{Command: "qarzlar", Description: "Kim kimga qarzdor"},
		{Command: "oxirgi", Description: "Oxirgi yozuvlar, tuzatish tugmalari bilan"},
		{Command: "app", Description: "Hisobotni grafiklar bilan ochish"},
		{Command: "xulosa", Description: "Ertalabki xulosani yoqish/o'chirish"},
		{Command: "help", Description: "Qanday yozish va buyruqlar"},
	}
}

func adminCommands() []botCommand {
	return []botCommand{
		{Command: "taklif", Description: "Admin: bir martalik taklif havolasi"},
		{Command: "foydalanuvchilar", Description: "Admin: kim foydalanadi"},
	}
}

func (b *Bot) menuButton() menuButton {
	if !strings.HasPrefix(b.cfg.WebAppURL, "https://") {
		return menuButton{Type: "commands"}
	}
	return menuButton{Type: "web_app", Text: "Hisobot", WebApp: &webAppInfo{URL: b.cfg.WebAppURL}}
}

// webAppKeyboard is one button that opens the Mini App; nil without one.
func (b *Bot) webAppKeyboard(label string) *inlineKeyboard {
	if !strings.HasPrefix(b.cfg.WebAppURL, "https://") {
		return nil
	}
	return &inlineKeyboard{InlineKeyboard: [][]inlineButton{{
		{Text: label, WebApp: &webAppInfo{URL: b.cfg.WebAppURL}},
	}}}
}

// privacyLine tells people who can see their records. It follows
// AdminSeesMoney and never promises more than is true: while the admin can
// see amounts it says only that other users cannot; the stronger "the
// owner cannot either" is said only when FINBOT_ADMIN_SEES_MONEY=false.
func (b *Bot) privacyLine() string {
	if b.cfg.AdminSeesMoney {
		return "🔒 Yozuvlaringizni boshqa foydalanuvchilar ko'rmaydi."
	}
	return "🔒 Yozuvlaringizni boshqa foydalanuvchilar ham, bot egasi ham bot orqali ko'rmaydi - admin faqat kim qachon foydalanganini ko'radi."
}

func (b *Bot) helpText(isAdmin bool) string {
	lines := []string{
		"<b>Hisob-kitob boti</b>",
		"",
		"Buyruqsiz, shunchaki yozing - har xabar bitta yozuv:",
		"<code>tushlik 35k</code> · <code>kecha taksi 18000</code>",
		"<code>korzinka 245 000</code> · <code>vps $12</code>",
		"<code>+10mln oylik</code> · <code>+500$ freelance</code> - kirim (<b>+</b> bilan)",
		"<code>Sardorga 100k berdim to'yga</code> - qaytmaydigan berish",
		"",
		"<b>Qarzlar</b>",
		"<code>Aliga qarz 500k</code> - siz berdingiz",
		"<code>Alidan qarz oldim 1mln</code> - siz oldingiz",
		"<code>Ali qaytardi 200k</code> - sizga qaytardi",
		"<code>Aliga qaytardim 200k</code> - siz qaytardingiz",
		"",
		"Bir nechtasini alohida qatorlarda bitta xabarda yuborsa ham bo'ladi.",
		"Sana: <code>kecha</code>, <code>o'tgan kuni</code>, <code>12.10</code>.",
		"Har yozuvdan keyin karta chiqadi - noto'g'ri tushungan bo'lsam, tugma bilan tuzating.",
		"",
		"<b>Hisobotlar</b>",
		"/hisob - shu oy · /hisob hafta · o'tgan · yil · sentabr",
		"/balans - qo'ldagi pul · <code>/balans 5mln</code> - haqiqiy summani yozish",
		"/qarzlar - ochiq qarzlar · <code>/qarzlar Ali</code> - bitta odam tarixi",
		"/oxirgi - oxirgi 10 ta, tuzatish va o'chirish",
		"/app - grafiklar, filtr va tahrirlash (Telegram ichida)",
		"/xulosa off - ertalabki xabarni o'chirish",
		"/ochirish - barcha ma'lumotlarimni o'chirish",
		"",
		b.privacyLine(),
	}
	if isAdmin {
		lines = append(lines, "",
			"<b>Admin</b>",
			"/taklif [izoh] - bir martalik havola (7 kun)",
			"/foydalanuvchilar - ro'yxat va kutayotganlar",
			"/ruxsat &lt;telegram id&gt; · /bloklash &lt;telegram id&gt;",
		)
	}
	return strings.Join(lines, "\n")
}

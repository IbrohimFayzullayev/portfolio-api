// Package bot is an operations bot for this stack: it answers questions about
// the platform from Telegram.
//
// It runs as its own container from the same image as the API. That split is
// the point — a monitor that lives inside the thing it monitors goes quiet
// exactly when you need it. For the same reason /invitations reads PostgreSQL
// directly instead of going through the API: the bot should still answer when
// the API is the thing that is broken.
package bot

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	Token string

	// Everyone allowed to talk to the bot. The FIRST is also where every
	// notification is delivered: a second entry is a backup account, not a
	// second recipient, and sending each alert twice would only make the one
	// person reading them read them twice.
	AllowedUserIDs []int64

	// Reached over the internal Docker network — bypasses Caddy entirely.
	InternalAPIURL string

	// Reached the way a visitor would, through Caddy and TLS.
	PublicSiteURL string
	AdminSiteURL  string
	PublicAPIURL  string

	// Optional: a URL to ping every health cycle, so something outside this
	// server can notice when the bot stops. Empty means no external pinger —
	// see deploy/cron/bot-watchdog.sh for the on-host half.
	HeartbeatPingURL string

	// Umami. All optional: without them the bot simply never prints a traffic
	// section. Either an API key or a username/password pair works.
	UmamiURL       string
	UmamiWebsiteID string
	UmamiUsername  string
	UmamiPassword  string
	UmamiAPIKey    string
}

type Bot struct {
	cfg   Config
	tg    *telegramClient
	pool  *pgxpool.Pool
	http  *http.Client
	umami *umamiClient
}

func New(cfg Config, pool *pgxpool.Pool) *Bot {
	return &Bot{
		cfg:   cfg,
		tg:    newTelegramClient(cfg.Token),
		pool:  pool,
		http:  &http.Client{Timeout: 8 * time.Second},
		umami: newUmamiClient(cfg),
	}
}

// Run long-polls Telegram until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	me, err := b.tg.getMe(ctx)
	if err != nil {
		return err
	}
	log.Printf("bot: connected as @%s (authorised users: %v)", me.Username, b.cfg.AllowedUserIDs)

	// The command menu is set on every start, so it always matches the build
	// that is running rather than whatever was registered months ago.
	if err := b.tg.setMyCommands(ctx, commandMenu()); err != nil {
		log.Printf("bot: could not set the command menu: %v", err)
	}

	// Skip anything queued while the bot was down: on restart we want the
	// current state, not a replay of yesterday's commands.
	offset, err := b.drainBacklog(ctx)
	if err != nil {
		log.Printf("bot: could not drain backlog: %v", err)
	}

	// The outbox is the opposite: notifications queued while the bot was down
	// ARE still wanted, and go out on the first tick. It runs beside the poll
	// loop rather than inside it — a 30-second long-poll must not delay a
	// deploy or health message by 30 seconds.
	go b.runOutbox(ctx)

	// Watches the same endpoints /status reports on, but notifies only when the
	// verdict changes: a service that has been down for an hour should produce
	// one message, not one every two minutes.
	go b.runHealthWatch(ctx)

	// One message each morning at 09:00 Asia/Tashkent.
	go b.runDailySummary(ctx)

	// And one on Sunday evening, with the week against the week before it.
	go b.runWeeklyReport(ctx)

	// Twice a day: certificates that are getting close to expiry, which means
	// automatic renewal has stopped working.
	go b.runCertWatch(ctx)

	// Turns "due" into "queued" once a minute: invitation reminders, manual
	// reminders, posts whose publish time has arrived. It never sends anything
	// itself — everything goes out through the outbox above.
	go b.runScheduler(ctx)

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
			// Telegram or the network is unhappy; back off instead of
			// hammering, but never give up — this process is the only way in.
			backoff = nextBackoff(backoff)
			log.Printf("bot: getUpdates failed (%v), retrying in %s", err, backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			continue
		}
		backoff = 0

		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			b.handle(ctx, u)
		}
	}
}

// allowed reports whether this user may talk to the bot.
func (c Config) allowed(userID int64) bool {
	for _, id := range c.AllowedUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}

// notifyTarget is where queued messages go: the first configured user.
func (c Config) notifyTarget() int64 {
	if len(c.AllowedUserIDs) == 0 {
		return 0
	}
	return c.AllowedUserIDs[0]
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

// drainBacklog returns the offset just past whatever is already queued.
func (b *Bot) drainBacklog(ctx context.Context) (int64, error) {
	updates, err := b.tg.getUpdates(ctx, 0, 0)
	if err != nil || len(updates) == 0 {
		return 0, err
	}
	last := updates[len(updates)-1].UpdateID
	log.Printf("bot: skipped %d queued update(s) from before startup", len(updates))
	return last + 1, nil
}

func (b *Bot) handle(ctx context.Context, u Update) {
	if u.CallbackQuery != nil {
		b.handleCallback(ctx, u.CallbackQuery)
		return
	}

	msg := u.Message
	if msg == nil || msg.From == nil || msg.Chat == nil {
		return
	}

	// The bot can read the database. Anyone who is not the owner gets no
	// answer at all — not even a refusal, which would confirm it exists.
	if !b.cfg.allowed(msg.From.ID) {
		log.Printf("bot: ignoring message from unauthorised user %d (@%s)",
			msg.From.ID, msg.From.Username)
		return
	}

	// Each command gets its own deadline so a stuck check cannot wedge the loop.
	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	started := time.Now()

	// A file is a post. Checked before the text path, because a document
	// message carries no text at all and would otherwise be dropped below.
	if msg.Document != nil {
		reply := b.importDocument(cctx, msg.Document, msg.Caption)
		if err := b.tg.sendMessage(cctx, msg.Chat.ID, reply); err != nil {
			log.Printf("bot: sendMessage failed: %v", err)
		}
		return
	}

	// An answer to something the bot asked. The question is identified by the
	// message it replies to, so no state had to survive in this process.
	if msg.ReplyToMessage != nil {
		if reply := b.answerPrompt(cctx, msg.Chat.ID, msg.ReplyToMessage.MessageID, msg.Text); reply != "" {
			if err := b.tg.sendMessage(cctx, msg.Chat.ID, reply); err != nil {
				log.Printf("bot: sendMessage failed: %v", err)
			}
			return
		}
		// Not one of ours: fall through and treat it as an ordinary message.
	}

	// A photo or sticker arrives with no text at all — Fields would be empty.
	fields := strings.Fields(msg.Text)
	if len(fields) == 0 {
		return
	}
	cmd := strings.ToLower(fields[0])
	if i := strings.Index(cmd, "@"); i != -1 {
		cmd = cmd[:i] // "/status@my_bot" in groups
	}

	switch cmd {
	case "/posts":
		// Sent with its own keyboard, so it does not go through the plain
		// reply path below.
		b.sendPostsList(cctx, msg.Chat.ID)
		return
	case "/xatolar":
		// Same: each group carries its own mute button.
		b.sendErrorsList(cctx, msg.Chat.ID)
		return
	case "/tahrir":
		b.sendEditCard(cctx, msg.Chat.ID,
			strings.TrimSpace(strings.TrimPrefix(msg.Text, fields[0])))
		return
	case "/qoralama":
		reply := b.createDraft(cctx, strings.TrimSpace(strings.TrimPrefix(msg.Text, fields[0])))
		if err := b.tg.sendMessage(cctx, msg.Chat.ID, reply); err != nil {
			log.Printf("bot: sendMessage failed: %v", err)
		}
		return
	}

	var reply string
	switch cmd {
	case "/start", "/help":
		reply = helpText()
	case "/status":
		reply = b.statusReport(cctx)
	case "/invitations":
		reply = b.invitationsReport(cctx)
	case "/kunlik":
		reply = b.dailySummary(cctx)
	case "/stat":
		reply = b.statsReport(cctx, strings.TrimSpace(strings.TrimPrefix(msg.Text, fields[0])))
	case "/xato":
		reply = b.errorDetail(cctx, strings.TrimSpace(strings.TrimPrefix(msg.Text, fields[0])))
	case "/eslatma":
		reply = b.createReminder(cctx, strings.TrimSpace(strings.TrimPrefix(msg.Text, fields[0])))
	case "/rejalash":
		reply = b.schedulePost(cctx, strings.TrimSpace(strings.TrimPrefix(msg.Text, fields[0])))
	default:
		reply = "Bunday buyruq yo'q. /help"
	}

	if err := b.tg.sendMessage(cctx, msg.Chat.ID, reply); err != nil {
		log.Printf("bot: sendMessage failed: %v", err)
	}

	// Which command is getting slow is not obvious from the outside: every one
	// of them answers eventually, and "eventually" is the whole question.
	log.Printf("bot: %s took %s", cmd, time.Since(started).Round(time.Millisecond))
}

// commandMenu is what Telegram shows in the menu button. Kept beside helpText
// on purpose — two lists that describe the same commands drift apart the first
// time only one of them is updated.
func commandMenu() []botCommand {
	return []botCommand{
		{Command: "status", Description: "Stack holati va uptime"},
		{Command: "posts", Description: "Oxirgi postlar, nashr tugmalari bilan"},
		{Command: "tahrir", Description: "Postni tahrirlash kartasi"},
		{Command: "qoralama", Description: "Matndan qoralama yaratish"},
		{Command: "rejalash", Description: "Postni belgilangan vaqtda nashr qilish"},
		{Command: "eslatma", Description: "O'zingizga eslatma qo'yish"},
		{Command: "stat", Description: "Trafik va kontent statistikasi"},
		{Command: "kunlik", Description: "Bugungi qisqacha hisobot"},
		{Command: "xatolar", Description: "Oxirgi 24 soat xatolari"},
		{Command: "invitations", Description: "Oxirgi taklifnomalar"},
		{Command: "help", Description: "Buyruqlar ro'yxati"},
	}
}

// handleCallback processes an inline button press.
func (b *Bot) handleCallback(ctx context.Context, q *CallbackQuery) {
	if q.From == nil {
		return
	}
	if !b.cfg.allowed(q.From.ID) {
		log.Printf("bot: ignoring callback from unauthorised user %d", q.From.ID)
		return
	}

	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	// Where to send a follow-up question, if the button asks one. In a private
	// chat this is the same as the user id; taking it from the message keeps
	// that an assumption the code does not depend on.
	chatID := q.From.ID
	if q.Message != nil && q.Message.Chat != nil {
		chatID = q.Message.Chat.ID
	}

	// The prefix says which list the button came from, and therefore which one
	// has to be redrawn afterwards. Redrawing the posts list after a mute
	// press would replace the message the button was attached to.
	action, arg, _ := strings.Cut(q.Data, ":")

	var (
		notice string
		redraw func(context.Context) (string, *inlineKeyboard)
	)
	switch action {
	case "mute":
		notice = b.muteError(cctx, arg)
		redraw = b.errorsList

	case "ed":
		// "ed:<field>:<post id>" — asks a question instead of changing
		// anything, so there is nothing to redraw yet.
		key, postID, _ := strings.Cut(arg, ":")
		notice = b.startEdit(cctx, chatID, key, postID)

	case "tr":
		notice = b.createTranslationPair(cctx, arg)
		redraw = func(c context.Context) (string, *inlineKeyboard) {
			return b.editCard(c, arg)
		}

	case "epub", "edrf":
		// The same publish toggle as the list, pressed from the edit card —
		// which is therefore what gets redrawn, not the list.
		notice = b.applyPostAction(cctx, strings.TrimPrefix(action, "e")+":"+arg)
		redraw = func(c context.Context) (string, *inlineKeyboard) {
			return b.editCard(c, arg)
		}

	default:
		notice = b.applyPostAction(cctx, q.Data)
		redraw = b.postsList
	}

	// Always answer, even on failure: an unanswered callback leaves the button
	// spinning and the bot looking dead.
	if err := b.tg.answerCallback(cctx, q.ID, notice); err != nil {
		log.Printf("bot: answerCallback failed: %v", err)
	}

	// Redraw in place so the new state is visible immediately. Some actions
	// have nothing to redraw — asking a question leaves the card as it was.
	if redraw != nil && q.Message != nil && q.Message.Chat != nil {
		text, markup := redraw(cctx)
		if err := b.tg.editMessageText(
			cctx, q.Message.Chat.ID, q.Message.MessageID, text, markup,
		); err != nil {
			log.Printf("bot: editMessageText failed: %v", err)
		}
	}
}

func helpText() string {
	return strings.Join([]string{
		"<b>Platforma boti</b>",
		"",
		"<b>Kontent</b>",
		"/posts — oxirgi postlar, nashr tugmalari bilan",
		"/tahrir &lt;slug&gt; — tahrirlash kartasi (yoki /tahrir oxirgi)",
		"/qoralama &lt;matn&gt; — birinchi qator sarlavha, qolgani matn",
		".md fayl yuborsangiz — post yaratiladi yoki yangilanadi",
		"",
		"<b>Vaqt bo'yicha</b>",
		"/rejalash &lt;slug&gt; 20.09 10:00 — belgilangan vaqtda nashr",
		"/rejalash — rejalashtirilganlar ro'yxati",
		"/eslatma 20.09 14:00 &lt;matn&gt; — o'zingizga eslatma",
		"",
		"<b>Ma'lumot</b>",
		"/status — stack holati, uptime, oxirgi uzilish",
		"/stat [kun|hafta|oy] — trafik va kontent",
		"/kunlik — bugungi qisqacha hisobot",
		"/invitations — oxirgi taklifnomalar",
		"",
		"<b>Xatolar</b>",
		"/xatolar — 24 soat, guruhlangan, jimlatish tugmasi bilan",
		"/xato &lt;id&gt; — bitta guruh: stack va statistika",
		"",
		"/help — shu ro'yxat",
	}, "\n")
}

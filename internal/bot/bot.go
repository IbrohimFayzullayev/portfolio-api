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
	Token         string
	AllowedUserID int64

	// Reached over the internal Docker network — bypasses Caddy entirely.
	InternalAPIURL string

	// Reached the way a visitor would, through Caddy and TLS.
	PublicSiteURL string
	AdminSiteURL  string
	PublicAPIURL  string
}

type Bot struct {
	cfg  Config
	tg   *telegramClient
	pool *pgxpool.Pool
	http *http.Client
}

func New(cfg Config, pool *pgxpool.Pool) *Bot {
	return &Bot{
		cfg:  cfg,
		tg:   newTelegramClient(cfg.Token),
		pool: pool,
		http: &http.Client{Timeout: 8 * time.Second},
	}
}

// Run long-polls Telegram until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	me, err := b.tg.getMe(ctx)
	if err != nil {
		return err
	}
	log.Printf("bot: connected as @%s (authorised user: %d)", me.Username, b.cfg.AllowedUserID)

	// Skip anything queued while the bot was down: on restart we want the
	// current state, not a replay of yesterday's commands.
	offset, err := b.drainBacklog(ctx)
	if err != nil {
		log.Printf("bot: could not drain backlog: %v", err)
	}

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
	msg := u.Message
	if msg == nil || msg.From == nil || msg.Chat == nil {
		return
	}

	// The bot can read the database. Anyone who is not the owner gets no
	// answer at all — not even a refusal, which would confirm it exists.
	if msg.From.ID != b.cfg.AllowedUserID {
		log.Printf("bot: ignoring message from unauthorised user %d (@%s)",
			msg.From.ID, msg.From.Username)
		return
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

	// Each command gets its own deadline so a stuck check cannot wedge the loop.
	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	var reply string
	switch cmd {
	case "/start", "/help":
		reply = helpText()
	case "/status":
		reply = b.statusReport(cctx)
	case "/invitations":
		reply = b.invitationsReport(cctx)
	default:
		reply = "Bunday buyruq yo'q. /help"
	}

	if err := b.tg.sendMessage(cctx, msg.Chat.ID, reply); err != nil {
		log.Printf("bot: sendMessage failed: %v", err)
	}
}

func helpText() string {
	return strings.Join([]string{
		"<b>Platforma boti</b>",
		"",
		"/status — stack holati (tashqi + ichki)",
		"/invitations — oxirgi taklifnomalar",
		"/help — shu ro'yxat",
	}, "\n")
}

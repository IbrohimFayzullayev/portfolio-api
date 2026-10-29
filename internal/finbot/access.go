package finbot

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ibrohimcoder/portfolio-api/internal/financetext"
)

// Who may use the bot, and how they get in.

const (
	statusPending = "pending"
	statusActive  = "active"
	statusBlocked = "blocked"
)

// How long an invite link works. Long enough to forward it and for the
// other person to get round to opening it; short enough that a link left in
// an old chat does not stay a key forever.
const inviteTTL = 7 * 24 * time.Hour

type user struct {
	ID           int64
	TelegramID   int64
	FirstName    string
	Username     string
	Status       string
	DailySummary bool
	JoinedVia    string
	CreatedAt    time.Time
	LastSeenAt   *time.Time
}

const userColumns = `id, telegram_id, first_name, username, status, daily_summary, joined_via, created_at, last_seen_at`

func scanUser(row pgx.Row) (*user, error) {
	var u user
	err := row.Scan(&u.ID, &u.TelegramID, &u.FirstName, &u.Username, &u.Status,
		&u.DailySummary, &u.JoinedVia, &u.CreatedAt, &u.LastSeenAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// userByTelegram returns nil, nil for someone who has never pressed /start.
func (b *Bot) userByTelegram(ctx context.Context, telegramID int64) (*user, error) {
	return userByTelegram(ctx, b.pool, telegramID)
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func userByTelegram(ctx context.Context, q querier, telegramID int64) (*user, error) {
	u, err := scanUser(q.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE telegram_id = $1`, telegramID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// touch records that the user was here. Only what the admin list shows;
// nothing about what they wrote.
func (b *Bot) touch(ctx context.Context, u *user) {
	if u.LastSeenAt != nil && b.now().Sub(*u.LastSeenAt) < time.Hour {
		return
	}
	if _, err := b.pool.Exec(ctx, `UPDATE users SET last_seen_at = now() WHERE id = $1`, u.ID); err != nil {
		log.Printf("finbot: touch failed: %v", err)
	}
}

func (b *Bot) dayOf(unix int64) time.Time {
	t := b.now()
	if unix > 0 {
		t = time.Unix(unix, 0)
	}
	return financetext.DayStart(t)
}

func displayName(first, username string) string {
	name := strings.TrimSpace(first)
	if name == "" {
		name = "Ismsiz"
	}
	if username != "" {
		name += " (@" + username + ")"
	}
	return name
}

/* --------------------------------- /start --------------------------------- */

func (b *Bot) handleStart(ctx context.Context, msg *Message, code string) {
	from := msg.From
	chatID := msg.Chat.ID

	u, err := b.userByTelegram(ctx, from.ID)
	if err != nil {
		log.Printf("finbot: /start lookup failed: %v", err)
		b.send(ctx, chatID, "❌ Hozir javob bera olmayman, birozdan keyin urinib ko'ring.", nil)
		return
	}
	if u != nil && u.Status == statusBlocked {
		return
	}
	if u != nil && u.Status == statusActive {
		b.sendWelcome(ctx, chatID, from.ID == b.cfg.AdminID, false)
		return
	}

	// The admin is let in by their own /start: nobody to ask.
	if from.ID == b.cfg.AdminID {
		if _, err := b.activate(ctx, from, "admin"); err != nil {
			log.Printf("finbot: admin activation failed: %v", err)
			return
		}
		b.sendWelcome(ctx, chatID, true, true)
		return
	}

	if code = strings.TrimSpace(code); code != "" {
		ok, err := b.redeemInvite(ctx, from, code)
		if err != nil {
			log.Printf("finbot: invite redeem failed: %v", err)
		}
		if ok {
			b.sendWelcome(ctx, chatID, false, true)
			b.send(ctx, b.cfg.AdminID, fmt.Sprintf("✅ <b>%s</b> taklif havolasi orqali qo'shildi.",
				escapeHTML(displayName(from.FirstName, from.Username))), nil)
			return
		}
		// A spent or expired link falls through to asking, which still works.
	}

	if u != nil && u.Status == statusPending {
		b.send(ctx, chatID, "⏳ So'rovingiz ko'rib chiqilmoqda. Ruxsat berilishi bilan shu yerga xabar keladi.", nil)
		return
	}

	if _, err := b.pool.Exec(ctx, `
		INSERT INTO users (telegram_id, first_name, username, status)
		VALUES ($1, $2, $3, 'pending')
		ON CONFLICT (telegram_id) DO NOTHING`, from.ID, from.FirstName, from.Username); err != nil {
		log.Printf("finbot: could not record access request: %v", err)
		b.send(ctx, chatID, "❌ Hozir javob bera olmayman, birozdan keyin urinib ko'ring.", nil)
		return
	}
	b.send(ctx, chatID, strings.Join([]string{
		"👋 <b>Assalomu alaykum!</b>",
		"",
		"Men <b>Hamyon</b> - shaxsiy pul hisobchingiz. Xarajat, kirim va qarzlarni oddiy xabar bilan yozasiz, hisobot va grafiklarni men tayyorlayman.",
		"",
		"Hozircha taklif bilan ishlayman. ✉️ So'rovingiz yuborildi - ruxsat berilishi bilan shu yerga xabar keladi.",
	}, "\n"), nil)

	if b.cfg.AdminID != 0 {
		b.send(ctx, b.cfg.AdminID, fmt.Sprintf("🙋 <b>Yangi so'rov</b>\n%s\nTelegram ID: <code>%d</code>",
			escapeHTML(displayName(from.FirstName, from.Username)), from.ID),
			&inlineKeyboard{InlineKeyboard: [][]inlineButton{{
				{Text: "✅ Ruxsat berish", CallbackData: "adm:ok:" + strconv.FormatInt(from.ID, 10)},
				{Text: "🚫 Rad etish", CallbackData: "adm:no:" + strconv.FormatInt(from.ID, 10)},
			}}})
	}
}

// activate makes an account active, creating it if needed.
func (b *Bot) activate(ctx context.Context, from *User, via string) (*user, error) {
	return scanUser(b.pool.QueryRow(ctx, `
		INSERT INTO users (telegram_id, first_name, username, status, joined_via, approved_at)
		VALUES ($1, $2, $3, 'active', $4, now())
		ON CONFLICT (telegram_id) DO UPDATE
		SET status = 'active', joined_via = EXCLUDED.joined_via, approved_at = now(),
		    first_name = EXCLUDED.first_name, username = EXCLUDED.username
		RETURNING `+userColumns, from.ID, from.FirstName, from.Username, via))
}

// redeemInvite spends a code. One statement claims it, so two people
// opening the same link at once cannot both get in.
func (b *Bot) redeemInvite(ctx context.Context, from *User, code string) (bool, error) {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	var claimed string
	err = tx.QueryRow(ctx, `
		UPDATE invites SET used_at = now()
		WHERE code = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING code`, code).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO users (telegram_id, first_name, username, status, joined_via, approved_at)
		VALUES ($1, $2, $3, 'active', $4, now())
		ON CONFLICT (telegram_id) DO UPDATE
		SET status = 'active', joined_via = EXCLUDED.joined_via, approved_at = now()
		WHERE users.status <> 'blocked'
		RETURNING id`, from.ID, from.FirstName, from.Username, "invite:"+code).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil // blocked: the link does not unblock
		}
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE invites SET used_by = $2 WHERE code = $1`, code, id); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

/* ---------------------------------- admin --------------------------------- */

func newInviteCode() (string, error) {
	raw := make([]byte, 10)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	// Base32 without padding: letters and digits only, which is what the
	// /start payload allows.
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)), nil
}

func (b *Bot) adminCommand(ctx context.Context, chatID int64, cmd, arg string) {
	switch cmd {
	case "/taklif":
		code, err := newInviteCode()
		if err == nil {
			_, err = b.pool.Exec(ctx, `
				INSERT INTO invites (code, note, expires_at) VALUES ($1, $2, now() + make_interval(secs => $3))`,
				code, arg, inviteTTL.Seconds())
		}
		if err != nil {
			b.send(ctx, chatID, "❌ Havola yaratilmadi: "+escapeHTML(err.Error()), nil)
			return
		}
		link := fmt.Sprintf("https://t.me/%s?start=%s", b.username, code)
		note := ""
		if arg != "" {
			note = " (" + escapeHTML(arg) + ")"
		}
		b.send(ctx, chatID, fmt.Sprintf(
			"🔗 <b>Taklif havolasi</b>%s\n\n%s\n\nBir marta ishlaydi, 7 kun amal qiladi. Ochgan odam darhol foydalana boshlaydi.",
			note, link), nil)

	case "/foydalanuvchilar":
		text, markup := b.usersList(ctx)
		b.send(ctx, chatID, text, markup)

	case "/ruxsat", "/bloklash":
		id, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
		if err != nil {
			b.send(ctx, chatID, "Telegram ID kerak: <code>"+cmd+" 123456789</code>\n/foydalanuvchilar ro'yxatida bor.", nil)
			return
		}
		notice, _ := b.setStatus(ctx, id, map[string]string{"/ruxsat": statusActive, "/bloklash": statusBlocked}[cmd])
		b.send(ctx, chatID, notice, nil)
	}
}

func (b *Bot) adminCallback(ctx context.Context, arg string) (string, redrawFunc) {
	op, rawID, _ := strings.Cut(arg, ":")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return "Noto'g'ri tugma", nil
	}
	status := statusActive
	if op == "no" {
		status = statusBlocked
	}
	notice, name := b.setStatus(ctx, id, status)
	return notice, func(context.Context) (string, *inlineKeyboard) {
		if status == statusActive {
			return "✅ <b>" + escapeHTML(name) + "</b> - ruxsat berildi.", nil
		}
		return "🚫 <b>" + escapeHTML(name) + "</b> - rad etildi.", nil
	}
}

// setStatus allows or blocks an account and tells its owner when they are
// let in. A refusal is said once, plainly; a later block is silent.
func (b *Bot) setStatus(ctx context.Context, telegramID int64, status string) (string, string) {
	var name, username, before string
	err := b.pool.QueryRow(ctx, `
		WITH old AS (SELECT status FROM users WHERE telegram_id = $1)
		UPDATE users SET status = $2,
		       approved_at = CASE WHEN $2 = 'active' THEN now() ELSE approved_at END,
		       joined_via  = CASE WHEN $2 = 'active' AND joined_via = '' THEN 'approved' ELSE joined_via END
		WHERE telegram_id = $1
		RETURNING first_name, username, (SELECT status FROM old)`, telegramID, status).Scan(&name, &username, &before)
	if errors.Is(err, pgx.ErrNoRows) {
		return "Bunday foydalanuvchi yo'q - avval u botda /start bosishi kerak.", ""
	}
	if err != nil {
		return "❌ Xato: " + escapeHTML(err.Error()), ""
	}
	display := displayName(name, username)

	switch {
	case status == statusActive && before != statusActive:
		b.sendWelcome(ctx, telegramID, telegramID == b.cfg.AdminID, true)
		return "Ruxsat berildi: " + display, display
	case status == statusBlocked && before == statusPending:
		b.send(ctx, telegramID, "Kechirasiz, hozircha ruxsat berilmadi.", nil)
		return "Rad etildi: " + display, display
	case status == statusBlocked:
		return "Bloklandi: " + display, display
	}
	return "O'zgarish yo'q: " + display, display
}

// usersList is for the admin: who uses the bot and how much, never what
// they wrote or how much money moved.
func (b *Bot) usersList(ctx context.Context) (string, *inlineKeyboard) {
	rows, err := b.pool.Query(ctx, `
		SELECT u.telegram_id, u.first_name, u.username, u.status, u.created_at, u.last_seen_at,
		       (SELECT count(*) FROM entries e WHERE e.user_id = u.id)
		FROM users u
		ORDER BY (u.status = 'pending') DESC, u.created_at`)
	if err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(err.Error()), nil
	}
	defer rows.Close()

	var (
		sb      strings.Builder
		buttons [][]inlineButton
		counts  = map[string]int{}
	)
	sb.WriteString("👥 <b>Foydalanuvchilar</b>\n")
	for rows.Next() {
		var (
			tgID         int64
			first, uname string
			status       string
			created      time.Time
			lastSeen     *time.Time
			entries      int64
		)
		if err := rows.Scan(&tgID, &first, &uname, &status, &created, &lastSeen, &entries); err != nil {
			return "❌ O'qib bo'lmadi: " + escapeHTML(err.Error()), nil
		}
		counts[status]++
		icon := map[string]string{statusActive: "🟢", statusPending: "⏳", statusBlocked: "⛔"}[status]
		line := fmt.Sprintf("\n%s %s · <code>%d</code>\n    qo'shildi %s", icon,
			escapeHTML(displayName(first, uname)), tgID, created.In(financetext.Tashkent).Format("02.01.2006"))
		if status == statusActive {
			seen := "-"
			if lastSeen != nil {
				seen = lastSeen.In(financetext.Tashkent).Format("02.01 15:04")
			}
			line += fmt.Sprintf(" · yozuvlar %d · oxirgi %s", entries, seen)
		}
		sb.WriteString(line + "\n")
		if status == statusPending {
			id := strconv.FormatInt(tgID, 10)
			label := first
			if label == "" {
				label = id
			}
			buttons = append(buttons, []inlineButton{
				{Text: "✅ " + truncate(label, 20), CallbackData: "adm:ok:" + id},
				{Text: "🚫", CallbackData: "adm:no:" + id},
			})
		}
	}
	if err := rows.Err(); err != nil {
		return "❌ O'qib bo'lmadi: " + escapeHTML(err.Error()), nil
	}
	if counts[statusActive]+counts[statusPending]+counts[statusBlocked] == 0 {
		return "👥 Hali hech kim yo'q. Havola: /taklif", nil
	}
	sb.WriteString(fmt.Sprintf("\nFaol: %d · kutmoqda: %d · bloklangan: %d",
		counts[statusActive], counts[statusPending], counts[statusBlocked]))
	sb.WriteString("\n<i>Batafsil va hisobotlar: dashboard'dagi \"Moliya boti\" sahifasi.</i>")

	var markup *inlineKeyboard
	if len(buttons) > 0 {
		markup = &inlineKeyboard{InlineKeyboard: buttons}
	}
	return sb.String(), markup
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

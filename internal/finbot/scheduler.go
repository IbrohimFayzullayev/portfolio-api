package finbot

import (
	"context"
	"log"
	"time"

	"github.com/ibrohimcoder/portfolio-api/internal/financetext"
)

// The clock side of the bot: the morning summary, and housekeeping.

// When the morning summary goes out, Tashkent time.
const summaryHour = 9

// Telegram allows about thirty messages a second across chats; a pause
// between summaries keeps a growing user list far below that.
const summaryPause = 100 * time.Millisecond

func (b *Bot) runScheduler(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		b.schedulerTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (b *Bot) schedulerTick(ctx context.Context) {
	now := b.now().In(financetext.Tashkent)
	if now.Hour() >= summaryHour {
		b.sendSummaries(ctx, financetext.DayStart(now))
	}
	if now.Minute() == 0 {
		b.housekeeping(ctx)
	}
}

// sendSummaries sends today's summary to everyone who wants one and has not
// had it. The day is claimed in summaries_sent BEFORE the message goes out:
// a crash in between costs one person one summary, never a second copy.
// Running late (the bot was down at 09:00) still sends, the same day.
func (b *Bot) sendSummaries(ctx context.Context, today time.Time) {
	rows, err := b.pool.Query(ctx, `
		SELECT `+userColumns+`
		FROM users u
		WHERE u.status = 'active' AND u.daily_summary
		  AND NOT EXISTS (SELECT 1 FROM summaries_sent s WHERE s.user_id = u.id AND s.day = $1::date)`,
		today.Format("2006-01-02"))
	if err != nil {
		log.Printf("finbot: summary query failed: %v", err)
		return
	}
	var due []*user
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			rows.Close()
			log.Printf("finbot: summary scan failed: %v", err)
			return
		}
		due = append(due, u)
	}
	rows.Close()

	for _, u := range due {
		if ctx.Err() != nil {
			return
		}
		tag, err := b.pool.Exec(ctx, `
			INSERT INTO summaries_sent (user_id, day) VALUES ($1, $2::date)
			ON CONFLICT DO NOTHING`, u.ID, today.Format("2006-01-02"))
		if err != nil || tag.RowsAffected() == 0 {
			continue
		}
		text, err := b.dailySummary(ctx, u, today)
		if err != nil {
			log.Printf("finbot: summary for user %d failed: %v", u.ID, err)
			continue
		}
		if text == "" {
			continue // nothing written yet: nothing to summarise
		}
		b.send(ctx, u.TelegramID, text+"\n\n<i>O'chirish: /xulosa off</i>", b.webAppKeyboard("📊 Hisobot"))
		time.Sleep(summaryPause)
	}
}

func (b *Bot) housekeeping(ctx context.Context) {
	for _, q := range []string{
		`DELETE FROM prompts WHERE expires_at < now() - interval '1 day'`,
		`DELETE FROM summaries_sent WHERE day < now() - interval '60 days'`,
		`DELETE FROM parse_failures WHERE created_at < now() - interval '60 days'`,
		`DELETE FROM invites WHERE used_at IS NULL AND expires_at < now() - interval '30 days'`,
	} {
		if _, err := b.pool.Exec(ctx, q); err != nil {
			log.Printf("finbot: housekeeping failed: %v", err)
		}
	}
}

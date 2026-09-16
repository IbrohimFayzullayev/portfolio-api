package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// Asking a question and recognising the answer.
//
// The bot has no conversation state in memory. It asks with force_reply,
// writes down which question that message was, and when a reply comes back it
// looks the question up by the id the reply points at. A deploy in the middle
// of an edit therefore costs nothing: the question is still on screen in
// Telegram, and the answer still lands where it was meant to.

// How long an unanswered question stays open. Long enough to finish a thought,
// short enough that replying to something from yesterday does not silently
// rewrite a post.
const promptTTL = 2 * time.Hour

// ask sends a question with force_reply and remembers what it was about.
func (b *Bot) ask(ctx context.Context, chatID int64, intent, targetID, text, placeholder string) {
	messageID, err := b.tg.sendPrompt(ctx, chatID, text, placeholder)
	if err != nil {
		log.Printf("bot: could not send prompt (%s): %v", intent, err)
		return
	}

	_, err = b.pool.Exec(ctx, `
		INSERT INTO bot_prompts (chat_id, message_id, intent, target_id, expires_at)
		VALUES ($1, $2, $3, $4, now() + make_interval(secs => $5))
		ON CONFLICT (chat_id, message_id) DO UPDATE
		SET intent = EXCLUDED.intent,
		    target_id = EXCLUDED.target_id,
		    expires_at = EXCLUDED.expires_at`,
		chatID, messageID, intent, targetID, promptTTL.Seconds())
	if err != nil {
		// The question is already on screen; without the row the answer will
		// simply not be recognised, which is a confusing but harmless outcome.
		log.Printf("bot: could not record prompt (%s): %v", intent, err)
	}
}

// answerPrompt applies a reply to whatever question it answers. The empty
// string means "this reply was not to one of ours" — the caller then treats
// the message normally.
func (b *Bot) answerPrompt(ctx context.Context, chatID, replyTo int64, text string) string {
	var intent, targetID string

	// DELETE ... RETURNING: a question is answered once. Doing it in one
	// statement also means two replies racing cannot both be applied.
	err := b.pool.QueryRow(ctx, `
		DELETE FROM bot_prompts
		WHERE chat_id = $1 AND message_id = $2 AND expires_at > now()
		RETURNING intent, target_id`, chatID, replyTo).Scan(&intent, &targetID)
	if err != nil {
		return "" // no such prompt, or it has expired
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return "Bo'sh javob — hech narsa o'zgarmadi."
	}

	return b.applyEdit(ctx, intent, targetID, text)
}

// forgetExpiredPrompts keeps the table from growing without bound. Run from
// the scheduler; nothing depends on it being timely.
func (b *Bot) forgetExpiredPrompts(ctx context.Context) {
	if _, err := b.pool.Exec(ctx,
		`DELETE FROM bot_prompts WHERE expires_at < now() - interval '1 day'`); err != nil {
		log.Printf("bot: could not clean up prompts: %v", err)
	}
}

// promptForField is the question text for each editable field.
func promptForField(intent, current string) (string, string) {
	f, ok := editableFields[intent]
	if !ok {
		return "Yangi qiymatni yuboring", ""
	}

	text := fmt.Sprintf("✏️ <b>%s</b> — yangi qiymatni javob qilib yuboring.", f.label)
	if current != "" {
		text += fmt.Sprintf("\n\nHozir:\n<code>%s</code>", escapeHTML(truncate(current, 300)))
	}
	if f.hint != "" {
		text += "\n\n<i>" + f.hint + "</i>"
	}
	return text, f.placeholder
}

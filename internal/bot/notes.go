package bot

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/ibrohimcoder/portfolio-api/notes"
)

// Reading the personal notes (plans, resources, the command guide) from the
// chat. The Mini App renders them properly — tables, headings — so /hujjat is
// a list of buttons that open each one there. "/hujjat reja" without a Mini
// App sends the text itself, which is worse to read but always works.

// tabURL is the Mini App opened on a given tab, optionally on one document.
func (b *Bot) tabURL(tab, doc string) string {
	base := b.webAppURL()
	if base == "" {
		return ""
	}
	q := url.Values{"tab": {tab}}
	if doc != "" {
		q.Set("doc", doc)
	}
	return base + "?" + q.Encode()
}

// tabKeyboard is one button that opens the Mini App on a tab. Nil when there
// is no Mini App, so the reply simply goes out without it.
func (b *Bot) tabKeyboard(tab, label string) *inlineKeyboard {
	u := b.tabURL(tab, "")
	if u == "" {
		return nil
	}
	return &inlineKeyboard{InlineKeyboard: [][]inlineButton{{
		{Text: label, WebApp: &webAppInfo{URL: u}},
	}}}
}

func (b *Bot) sendNotes(ctx context.Context, chatID int64, arg string) {
	arg = strings.TrimSpace(strings.ToLower(arg))
	if arg != "" {
		b.sendNoteText(ctx, chatID, arg)
		return
	}

	list := notes.List()
	var sb strings.Builder
	sb.WriteString("📄 <b>Hujjatlar</b>\n\n")
	var rows [][]inlineButton
	for _, n := range list {
		sb.WriteString(fmt.Sprintf("• %s — <code>/hujjat %s</code>\n", escapeHTML(n.Title), n.Slug))
		if u := b.tabURL("notes", n.Slug); u != "" {
			rows = append(rows, []inlineButton{{Text: "📄 " + truncate(n.Title, 40), WebApp: &webAppInfo{URL: u}}})
		}
	}
	var markup *inlineKeyboard
	if len(rows) > 0 {
		sb.WriteString("\nTugmani bosing — Mini App'da ochiladi.")
		markup = &inlineKeyboard{InlineKeyboard: rows}
	}
	if err := b.tg.sendMessageMarkup(ctx, chatID, sb.String(), markup); err != nil {
		log.Printf("bot: sendMessage failed: %v", err)
	}
}

// telegramLimit leaves room under Telegram's 4096 characters for the escaping.
const telegramLimit = 3500

func (b *Bot) sendNoteText(ctx context.Context, chatID int64, slug string) {
	n, ok := notes.Get(slug)
	if !ok {
		b.reply(ctx, chatID, "Bunday hujjat yo'q. /hujjat")
		return
	}
	for i, chunk := range splitText(n.Body, telegramLimit) {
		text := escapeHTML(chunk)
		if i == 0 {
			if u := b.tabURL("notes", n.Slug); u != "" {
				_ = b.tg.sendMessageMarkup(ctx, chatID, text, &inlineKeyboard{InlineKeyboard: [][]inlineButton{{
					{Text: "📄 Chiroyli ko'rinishda ochish", WebApp: &webAppInfo{URL: u}},
				}}})
				continue
			}
		}
		b.reply(ctx, chatID, text)
	}
}

// splitText cuts on blank lines, then on line ends, so a chunk never stops in
// the middle of a table row.
func splitText(s string, limit int) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		if cur.Len()+len(line) > limit && cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
		cur.WriteString(line)
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

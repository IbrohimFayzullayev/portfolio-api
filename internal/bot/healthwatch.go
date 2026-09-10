package bot

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"
)

// Health watching.
//
// /status answers "is it up right now?" when asked. This asks the same question
// on a timer and speaks up only when the answer CHANGES — the difference
// between a monitor and a nuisance. A service down for an hour produces one
// red message and, later, one green one.
//
// It writes into the same notifications table the API uses rather than calling
// sendMessage directly, so every message the bot sends leaves the same trail
// and follows the same retry rules.

const healthInterval = 2 * time.Minute

func (b *Bot) runHealthWatch(ctx context.Context) {
	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()

	// Whether each endpoint was healthy last time we looked. Absent means "not
	// checked yet": the first round records the state without announcing it, so
	// a restart during an outage does not replay the alert.
	seen := map[string]bool{}
	first := true

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.checkHealth(ctx, seen, first)
			first = false
		}
	}
}

func (b *Bot) checkHealth(ctx context.Context, seen map[string]bool, first bool) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	targets := []struct{ label, url string }{
		{"Sayt", strings.TrimSuffix(b.cfg.PublicSiteURL, "/")},
		{"API", strings.TrimSuffix(b.cfg.PublicAPIURL, "/") + "/healthz"},
		{"Admin", strings.TrimSuffix(b.cfg.AdminSiteURL, "/")},
	}

	for _, t := range targets {
		r := b.probe(cctx, t.label, t.url)
		was, known := seen[t.label]
		seen[t.label] = r.ok

		if first || !known || was == r.ok {
			continue
		}

		kind := "health.recovered"
		payload := map[string]any{"label": t.label}
		if !r.ok {
			kind = "health.down"
			payload["detail"] = r.detail
		}
		b.queue(cctx, kind, payload)
	}
}

// queue writes a row the outbox will pick up on its next tick.
func (b *Bot) queue(ctx context.Context, kind string, payload map[string]any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		log.Printf("bot: could not marshal %s payload: %v", kind, err)
		return
	}
	_, err = b.pool.Exec(ctx,
		`INSERT INTO notifications (kind, payload) VALUES ($1, $2)`, kind, raw)
	if err != nil {
		log.Printf("bot: could not queue %s: %v", kind, err)
	}
}

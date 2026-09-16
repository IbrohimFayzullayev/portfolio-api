package bot

import (
	"context"
	"log"
	"net/http"
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

const (
	healthInterval = 2 * time.Minute

	// A verdict has to repeat before it is announced. One failed probe is
	// usually a dropped packet or a container restarting, and a monitor that
	// reports those teaches you to ignore it — which is the only real way a
	// monitor can fail.
	healthConfirmations = 2

	// Health rows are kept for a month: long enough for "how was last week",
	// short enough that nobody has to think about the table.
	healthRetention = 30 * 24 * time.Hour
)

// watchState is what the watcher remembers between rounds.
type watchState struct {
	// The announced verdict — what Telegram currently believes.
	announced map[string]bool
	// Consecutive probes disagreeing with it.
	pending map[string]int
	started bool
}

func newWatchState() *watchState {
	return &watchState{
		announced: map[string]bool{},
		pending:   map[string]int{},
	}
}

func (b *Bot) runHealthWatch(ctx context.Context) {
	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()

	state := newWatchState()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.checkHealth(ctx, state)
		}
	}
}

func (b *Bot) checkHealth(ctx context.Context, state *watchState) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	targets := []struct{ label, url string }{
		{"Sayt", strings.TrimSuffix(b.cfg.PublicSiteURL, "/")},
		{"API", strings.TrimSuffix(b.cfg.PublicAPIURL, "/") + "/healthz"},
		{"Admin", strings.TrimSuffix(b.cfg.AdminSiteURL, "/")},
	}

	for _, t := range targets {
		r := b.probe(cctx, t.label, t.url)
		b.recordCheck(cctx, r)

		if change, ok := state.observe(t.label, r.ok); ok {
			kind := "health.recovered"
			payload := map[string]any{"label": t.label}
			if !change {
				kind = "health.down"
				payload["detail"] = r.detail
			}
			b.queue(cctx, kind, payload)
		}
	}

	// The pulse goes out after the round, so it means "a full cycle completed",
	// not merely "the goroutine is scheduled".
	b.beat(cctx)
}

// observe folds one probe into the remembered state and reports whether the
// verdict should be announced.
//
// The first round is recorded silently: a bot restarted during an outage
// should not replay the alert, and a bot restarted after one should not
// announce a recovery it never saw break.
func (s *watchState) observe(label string, ok bool) (verdict bool, announce bool) {
	current, known := s.announced[label]
	if !known {
		s.announced[label] = ok
		return ok, false
	}

	if ok == current {
		s.pending[label] = 0
		return ok, false
	}

	s.pending[label]++
	if s.pending[label] < healthConfirmations {
		return ok, false
	}

	s.pending[label] = 0
	s.announced[label] = ok
	return ok, true
}

/* ------------------------------- recording -------------------------------- */

func (b *Bot) recordCheck(ctx context.Context, r checkResult) {
	_, err := b.pool.Exec(ctx, `
		INSERT INTO health_checks (target, ok, latency_ms, detail)
		VALUES ($1, $2, $3, $4)`,
		r.label, r.ok, r.latency.Milliseconds(), truncate(r.detail, 200))
	if err != nil {
		// Losing a history row is not worth a message; the check itself
		// already happened and any state change is announced regardless.
		log.Printf("bot: could not record health check: %v", err)
	}
}

// beat updates the bot's pulse. The watchdog outside reads nothing else.
func (b *Bot) beat(ctx context.Context) {
	if _, err := b.pool.Exec(ctx,
		`UPDATE bot_heartbeat SET beat_at = now(), note = $1 WHERE id = 1`,
		time.Now().In(tashkent).Format("02.01 15:04")); err != nil {
		log.Printf("bot: could not write heartbeat: %v", err)
	}

	b.pingExternal(ctx)
}

// pingExternal tells a service somewhere else that the bot is alive. Off
// unless HEARTBEAT_PING_URL is set — and the only thing that closes the gap
// left by running the watchdog on the same machine as the bot, because a dead
// server stops sending pings and cannot report itself.
func (b *Bot) pingExternal(ctx context.Context) {
	if b.cfg.HeartbeatPingURL == "" {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.cfg.HeartbeatPingURL, nil)
	if err != nil {
		return
	}
	res, err := b.http.Do(req)
	if err != nil {
		log.Printf("bot: heartbeat ping failed: %v", err)
		return
	}
	_ = res.Body.Close()
}

// prunes old rows. Called from the scheduler; nothing depends on its timing.
func (b *Bot) pruneHealthHistory(ctx context.Context) {
	if _, err := b.pool.Exec(ctx,
		`DELETE FROM health_checks WHERE checked_at < now() - make_interval(secs => $1)`,
		healthRetention.Seconds()); err != nil {
		log.Printf("bot: could not prune health history: %v", err)
	}
}

/* --------------------------------- queue ---------------------------------- */

// queue writes a row the outbox will pick up on its next tick: deliver as soon
// as possible, no de-duplication.
//
// Severity travels with the row rather than being re-derived at delivery time,
// so whoever reads the table later can see how loud the message was meant to
// be.
func (b *Bot) queue(ctx context.Context, kind string, payload map[string]any) {
	b.queueScheduled(ctx, kind, payload, defaultSeverity(kind), "", time.Time{})
}

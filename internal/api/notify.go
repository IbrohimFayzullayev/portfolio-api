package api

import (
	"context"
	"encoding/json"
	"log"
	"time"
)

// notify queues a message for the operations bot to deliver.
//
// Deliberately fire-and-forget with its own context: a request must not fail,
// slow down, or be cancelled because a notification could not be queued. The
// caller keeps its own error path; this one only logs.
//
// Detached from the request context on purpose — the handler usually returns
// before this goroutine runs, and cancelling the write when the client
// disconnects would drop notifications for exactly the requests that mattered.
//
// Raw SQL rather than sqlc: this is one INSERT into a table the bot owns the
// reading half of, and keeping both halves written the same way makes the pair
// easier to follow than splitting it across two generators.
func (s *Server) notify(kind string, payload map[string]any) {
	s.queueNotice(notice{kind: kind, payload: payload})
}

// notice is one queued message. Everything past kind and payload is optional,
// and the zero value means what the outbox has always done: deliver as soon as
// possible, at info severity, without de-duplication.
type notice struct {
	kind    string
	payload map[string]any

	// 0 info, 1 warning, 2 critical. Only critical escapes quiet hours.
	severity int

	// At most one PENDING row per key. The bot's outbox relies on a partial
	// unique index for this, so a duplicate INSERT is not an error here — it
	// is the mechanism.
	dedupeKey string

	// Zero means "as soon as possible".
	deliverAfter time.Time
}

func (s *Server) queueNotice(n notice) {
	raw, err := json.Marshal(n.payload)
	if err != nil {
		log.Printf("notify: marshal %s: %v", n.kind, err)
		return
	}

	var (
		key   *string
		after *time.Time
	)
	if n.dedupeKey != "" {
		key = &n.dedupeKey
	}
	if !n.deliverAfter.IsZero() {
		after = &n.deliverAfter
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// ON CONFLICT DO NOTHING is the de-duplication: while an identical
		// message is still waiting to go out, a second one is silently
		// dropped rather than queued behind it.
		_, err := s.pool.Exec(ctx, `
			INSERT INTO notifications (kind, payload, severity, dedupe_key, deliver_after)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT DO NOTHING`,
			n.kind, raw, n.severity, key, after)
		if err != nil {
			log.Printf("notify: queue %s: %v", n.kind, err)
		}
	}()
}

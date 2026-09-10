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
	raw, err := json.Marshal(payload)
	if err != nil {
		log.Printf("notify: marshal %s: %v", kind, err)
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := s.pool.Exec(ctx,
			`INSERT INTO notifications (kind, payload) VALUES ($1, $2)`,
			kind, raw)
		if err != nil {
			log.Printf("notify: queue %s: %v", kind, err)
		}
	}()
}

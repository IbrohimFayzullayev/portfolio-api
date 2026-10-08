package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// One read-only snapshot of how the stack is doing, for the dashboard.
//
// Everything here already existed — the bot writes health checks and a
// heartbeat, the API groups its errors, CI and cron leave rows in the outbox —
// but the only window onto it was Telegram, one command at a time. This
// endpoint puts the same facts on one page.
//
// Raw SQL rather than sqlc, like notify.go and for the same reason: these are
// the bot's tables, read here the way the bot reads them, and none of the
// queries is reused anywhere else.

// Mirrors maxAttempts in internal/bot/outbox.go: a row that has failed this
// many times is parked rather than retried.
const outboxMaxAttempts = 5

type systemErrorGroup struct {
	Fingerprint string     `json:"fingerprint"`
	Service     string     `json:"service"`
	Route       string     `json:"route"`
	Severity    int        `json:"severity"`
	Message     string     `json:"message"`
	Count       int64      `json:"count"`
	FirstSeen   time.Time  `json:"first_seen"`
	LastSeen    time.Time  `json:"last_seen"`
	MutedUntil  *time.Time `json:"muted_until"`
}

type systemHealthTarget struct {
	Target      string     `json:"target"`
	OK          bool       `json:"ok"`
	LatencyMS   int        `json:"latency_ms"`
	Detail      string     `json:"detail"`
	CheckedAt   time.Time  `json:"checked_at"`
	Checks7d    int64      `json:"checks_7d"`
	Uptime7d    float64    `json:"uptime_7d"`
	LastFailure *time.Time `json:"last_failure"`
}

type systemDeploy struct {
	Service string    `json:"service"`
	SHA     string    `json:"sha"`
	At      time.Time `json:"at"`
}

type systemOutbox struct {
	Pending    int64      `json:"pending"`
	Parked     int64      `json:"parked"`
	LastSentAt *time.Time `json:"last_sent_at"`
}

type systemBot struct {
	BeatAt time.Time `json:"beat_at"`
	Note   string    `json:"note"`
}

type systemBackup struct {
	At   time.Time `json:"at"`
	Size string    `json:"size"`
	File string    `json:"file"`
}

type systemOverviewResponse struct {
	GeneratedAt time.Time            `json:"generated_at"`
	Errors      []systemErrorGroup   `json:"errors"`
	Health      []systemHealthTarget `json:"health"`
	Deploys     []systemDeploy       `json:"deploys"`
	Outbox      systemOutbox         `json:"outbox"`
	// Null until the bot has run at least once / a backup has ever finished.
	// Absence is information here, so it is not papered over with a zero time.
	Bot    *systemBot    `json:"bot"`
	Backup *systemBackup `json:"backup"`
}

func (s *Server) handleSystemOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := systemOverviewResponse{
		GeneratedAt: time.Now().UTC(),
		Errors:      []systemErrorGroup{},
		Health:      []systemHealthTarget{},
		Deploys:     []systemDeploy{},
	}

	var err error
	if out.Errors, err = s.systemErrors(ctx); err != nil {
		s.serverError(w, r, "failed to load error groups", err)
		return
	}
	if out.Health, err = s.systemHealth(ctx); err != nil {
		s.serverError(w, r, "failed to load health history", err)
		return
	}
	if out.Deploys, err = s.systemDeploys(ctx); err != nil {
		s.serverError(w, r, "failed to load deploys", err)
		return
	}
	if out.Outbox, err = s.systemOutbox(ctx); err != nil {
		s.serverError(w, r, "failed to load outbox state", err)
		return
	}
	if out.Bot, err = s.systemBot(ctx); err != nil {
		s.serverError(w, r, "failed to load bot heartbeat", err)
		return
	}
	if out.Backup, err = s.systemBackup(ctx); err != nil {
		s.serverError(w, r, "failed to load backup state", err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (s *Server) systemErrors(ctx context.Context) ([]systemErrorGroup, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT fingerprint, service, route, severity, message, count,
		       first_seen, last_seen, muted_until
		FROM error_groups
		WHERE last_seen > now() - interval '7 days'
		ORDER BY last_seen DESC
		LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []systemErrorGroup{}
	for rows.Next() {
		var g systemErrorGroup
		if err := rows.Scan(&g.Fingerprint, &g.Service, &g.Route, &g.Severity, &g.Message,
			&g.Count, &g.FirstSeen, &g.LastSeen, &g.MutedUntil); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Server) systemHealth(ctx context.Context) ([]systemHealthTarget, error) {
	// The aggregate says how the week went; the lateral join adds how things
	// are right now. Two different questions, and the page asks both.
	rows, err := s.pool.Query(ctx, `
		SELECT a.target, a.total, a.good, a.last_failure,
		       l.ok, l.latency_ms, l.detail, l.checked_at
		FROM (
			SELECT target,
			       count(*)                              AS total,
			       count(*) FILTER (WHERE ok)            AS good,
			       max(checked_at) FILTER (WHERE NOT ok) AS last_failure
			FROM health_checks
			WHERE checked_at > now() - interval '7 days'
			GROUP BY target
		) a
		JOIN LATERAL (
			SELECT ok, latency_ms, detail, checked_at
			FROM health_checks h
			WHERE h.target = a.target
			ORDER BY checked_at DESC
			LIMIT 1
		) l ON true
		ORDER BY a.target`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []systemHealthTarget{}
	for rows.Next() {
		var (
			t    systemHealthTarget
			good int64
		)
		if err := rows.Scan(&t.Target, &t.Checks7d, &good, &t.LastFailure,
			&t.OK, &t.LatencyMS, &t.Detail, &t.CheckedAt); err != nil {
			return nil, err
		}
		if t.Checks7d > 0 {
			t.Uptime7d = float64(good) / float64(t.Checks7d) * 100
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Server) systemDeploys(ctx context.Context) ([]systemDeploy, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(payload->>'service', ''), COALESCE(payload->>'sha', ''), created_at
		FROM notifications
		WHERE kind = 'deploy.finished'
		ORDER BY created_at DESC
		LIMIT 12`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []systemDeploy{}
	for rows.Next() {
		var d systemDeploy
		if err := rows.Scan(&d.Service, &d.SHA, &d.At); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Server) systemOutbox(ctx context.Context) (systemOutbox, error) {
	var o systemOutbox
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE sent_at IS NULL AND attempts <  $1),
		       count(*) FILTER (WHERE sent_at IS NULL AND attempts >= $1),
		       max(sent_at)
		FROM notifications`, outboxMaxAttempts,
	).Scan(&o.Pending, &o.Parked, &o.LastSentAt)
	return o, err
}

func (s *Server) systemBot(ctx context.Context) (*systemBot, error) {
	var b systemBot
	err := s.pool.QueryRow(ctx,
		`SELECT beat_at, note FROM bot_heartbeat WHERE id = 1`,
	).Scan(&b.BeatAt, &b.Note)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Server) systemBackup(ctx context.Context) (*systemBackup, error) {
	var b systemBackup
	err := s.pool.QueryRow(ctx, `
		SELECT created_at, COALESCE(payload->>'size', ''), COALESCE(payload->>'file', '')
		FROM notifications
		WHERE kind = 'backup.finished'
		ORDER BY created_at DESC
		LIMIT 1`,
	).Scan(&b.At, &b.Size, &b.File)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

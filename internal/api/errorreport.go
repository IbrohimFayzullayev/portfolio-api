package api

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// Server-side errors, on their way to Telegram.
//
// A 500 that only ever reaches the container log is a 500 nobody knows about
// until someone complains. This file is the other half: every failure a
// handler reports is grouped, counted, and — at most once per window per
// distinct error — queued for the bot.
//
// The window is the whole design. Alerting on every occurrence sounds more
// thorough and is strictly worse: one failing query at request rate produces a
// hundred messages a minute, and a channel that behaves like that gets muted
// on the first bad night, which means the next real outage arrives in silence.

const (
	// One message per distinct error per window, however often it happens in
	// between. The count of what was skipped travels in the message.
	errorNotifyWindow = 15 * time.Minute

	// A burst of DIFFERENT errors never trips the per-error window — each one
	// is only seen once or twice — so the overall 5xx rate is watched too.
	errorRateWindow    = 5 * time.Minute
	errorRateThreshold = 10

	// Telegram's limit is 4096 characters for the whole message; a stack that
	// long is unreadable on a phone anyway. The full one stays in the table.
	maxStackBytes   = 1500
	maxMessageChars = 400
)

// Severity, matching notifications.severity on the bot's side: only critical
// is allowed through quiet hours.
const (
	severityInfo     = 0
	severityWarning  = 1
	severityCritical = 2
)

var (
	uuidPattern   = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	numberPattern = regexp.MustCompile(`\d+`)
	hexPattern    = regexp.MustCompile(`0x[0-9a-fA-F]+`)
)

// normalizeErrorText strips the parts that differ between two occurrences of
// the same failure. Order matters: UUIDs before bare numbers, or the number
// pass would shred the UUID into pieces first.
func normalizeErrorText(msg string) string {
	msg = uuidPattern.ReplaceAllString(msg, "?")
	msg = hexPattern.ReplaceAllString(msg, "?")
	msg = numberPattern.ReplaceAllString(msg, "?")
	return msg
}

// fingerprintError identifies the GROUP an error belongs to. Twelve hex
// characters: short enough to type back as /xato <fp>, wide enough that two
// unrelated errors colliding is not a practical concern at this volume.
func fingerprintError(service, route, msg string) string {
	sum := sha1.Sum([]byte(service + "|" + route + "|" + normalizeErrorText(msg)))
	return hex.EncodeToString(sum[:])[:12]
}

// routePattern prefers chi's pattern ("/api/v1/posts/{id}") over the concrete
// path, so every id does not become its own error group.
func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return r.Method + " " + p
		}
	}
	return r.Method + " " + r.URL.Path
}

/* ------------------------------ reporting -------------------------------- */

// reportError records one failure. Fire-and-forget by design: a request that
// has already failed must not also wait on the bookkeeping about it.
func (s *Server) reportError(r *http.Request, kind string, severity int, msg, stack string) {
	route := routePattern(r)
	fp := fingerprintError("api", route, msg)

	// Everything needed is copied out of the request here, because the request
	// is usually finished by the time the goroutine runs.
	go s.recordError(fp, kind, route, severity, truncateChars(msg, maxMessageChars), stack)
}

func (s *Server) recordError(fp, kind, route string, severity int, msg, stack string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		total      int64
		notified   int64
		notifiedAt *time.Time
		mutedUntil *time.Time
	)

	// One statement does the whole thing: insert the group or bump it, and
	// return what is needed to decide whether to speak. A read-then-write
	// would race with itself under exactly the load that matters here.
	err := s.pool.QueryRow(ctx, `
		INSERT INTO error_groups
			(fingerprint, service, route, severity, message, sample_stack, count)
		VALUES ($1, 'api', $2, $3, $4, $5, 1)
		ON CONFLICT (fingerprint) DO UPDATE
		SET count        = error_groups.count + 1,
		    last_seen    = now(),
		    severity     = GREATEST(error_groups.severity, EXCLUDED.severity),
		    message      = EXCLUDED.message,
		    sample_stack = CASE WHEN EXCLUDED.sample_stack <> ''
		                       THEN EXCLUDED.sample_stack
		                       ELSE error_groups.sample_stack END
		RETURNING count, notified_count, notified_at, muted_until`,
		fp, route, severity, msg, stack,
	).Scan(&total, &notified, &notifiedAt, &mutedUntil)
	if err != nil {
		log.Printf("errors: could not record %s: %v", fp, err)
		return
	}

	now := time.Now()
	switch {
	case mutedUntil != nil && mutedUntil.After(now):
		return // silenced from Telegram, on purpose
	case notifiedAt != nil && now.Sub(*notifiedAt) < errorNotifyWindow:
		return // already said this recently
	}

	if _, err := s.pool.Exec(ctx, `
		UPDATE error_groups
		SET notified_at = now(), notified_count = count
		WHERE fingerprint = $1`, fp); err != nil {
		log.Printf("errors: could not mark %s notified: %v", fp, err)
	}

	since := total - notified
	if since < 1 {
		since = 1
	}

	s.queueNotice(notice{
		kind:     kind,
		severity: severity,
		// The window index makes the key change every 15 minutes, so a second
		// notification about the same error is possible later but a duplicate
		// within one window is not.
		dedupeKey: fmt.Sprintf("err:%s:%d", fp, now.Unix()/int64(errorNotifyWindow/time.Second)),
		payload: map[string]any{
			"fingerprint": fp,
			"route":       route,
			"message":     msg,
			"count":       since,
			"total":       total,
		},
	})
}

// serverError is what a handler calls instead of writing a 500 itself. The
// client sees the same generic message as before; the difference is that the
// bot now finds out. Keeping the two in one call is the point — a 500 written
// by hand is a 500 nobody hears about.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, msg string, cause error) {
	detail := msg
	if cause != nil {
		detail = msg + ": " + cause.Error()
	}
	log.Printf("api: %s %s: %s", r.Method, r.URL.Path, detail)
	s.reportError(r, "error.api", severityWarning, detail, "")
	writeError(w, http.StatusInternalServerError, msg)
}

/* ------------------------------ middleware ------------------------------- */

// recoverPanic replaces chi's Recoverer rather than sitting beside it: two
// recoveries in one chain means the inner one swallows the panic and the outer
// one never learns it happened.
//
// A panic is the one error class that is always critical — the handler did not
// merely fail, it left the process in a state nobody designed.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// Go's convention for "the client went away"; re-panicking is how
			// the server is told to drop the connection quietly.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}

			stack := string(debug.Stack())
			msg := fmt.Sprintf("panic: %v", rec)
			log.Printf("api: %s %s: %s\n%s", r.Method, r.URL.Path, msg, stack)

			s.reportError(r, "error.panic", severityCritical, msg, truncateBytes(stack, maxStackBytes))
			writeError(w, http.StatusInternalServerError, "internal error")
		}()

		next.ServeHTTP(w, r)
	})
}

// statusRecorder remembers what status was written, so the rate watch can see
// a 5xx without the handler having to report it.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusRecorder) WriteHeader(code int) {
	if !w.written {
		w.status = code
		w.written = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if !w.written {
		w.status = http.StatusOK
		w.written = true
	}
	return w.ResponseWriter.Write(b)
}

// watchErrorRate catches what per-error grouping cannot: twenty DIFFERENT
// failures in five minutes, each one seen once. Individually none of them is
// worth waking up for; together they are the shape of an outage.
func (s *Server) watchErrorRate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		if rec.status < 500 {
			return
		}
		if count, alert := s.errRate.record(time.Now()); alert {
			s.queueNotice(notice{
				kind:      "error.rate",
				severity:  severityCritical,
				dedupeKey: fmt.Sprintf("err-rate:%d", time.Now().Unix()/int64(errorRateWindow/time.Second)),
				payload: map[string]any{
					"count":   count,
					"minutes": int(errorRateWindow / time.Minute),
				},
			})
		}
	})
}

// errorRate is a sliding window of recent 5xx responses.
type errorRate struct {
	mu        sync.Mutex
	stamps    []time.Time
	lastAlert time.Time
}

// record adds one failure and reports whether it crosses the threshold. It
// alerts at most once per window: past the threshold every further request
// would otherwise re-trigger it, which is the flood the threshold exists to
// prevent.
func (e *errorRate) record(now time.Time) (int, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	cutoff := now.Add(-errorRateWindow)
	kept := e.stamps[:0]
	for _, t := range e.stamps {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	e.stamps = append(kept, now)

	if len(e.stamps) < errorRateThreshold {
		return len(e.stamps), false
	}
	if !e.lastAlert.IsZero() && now.Sub(e.lastAlert) < errorRateWindow {
		return len(e.stamps), false
	}
	e.lastAlert = now
	return len(e.stamps), true
}

/* -------------------------------- helpers -------------------------------- */

func truncateChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut at a line boundary when there is one nearby: half a stack frame is
	// harder to read than one frame fewer.
	cut := s[:n]
	if i := strings.LastIndex(cut, "\n"); i > n/2 {
		cut = cut[:i]
	}
	return cut + "\n…"
}

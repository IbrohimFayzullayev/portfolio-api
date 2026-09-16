package bot

import (
	"testing"
	"time"
)

// The outbox's decisions about WHEN and HOW LOUD are pure arithmetic over a
// clock, so they are tested without a database or a Telegram token. The parts
// that genuinely need either are exercised through the transport test in
// telegram_test.go and the integration suite.

func TestQuietHoursDefer(t *testing.T) {
	// A fixed evening in Tashkent. Only the hour matters to the function.
	at := func(hour int) time.Time {
		return time.Date(2026, 9, 15, hour, 30, 0, 0, tashkent)
	}

	tests := []struct {
		name      string
		now       time.Time
		severity  int
		wantQuiet bool
		wantHour  int
		wantDay   int
	}{
		{"midday info goes out", at(13), sevInfo, false, 0, 0},
		{"22:30 still goes out", at(22), sevInfo, false, 0, 0},
		{"23:30 waits for morning", at(23), sevInfo, true, 9, 16},
		{"03:30 waits for this morning", at(3), sevInfo, true, 9, 15},
		{"07:30 waits for this morning", at(7), sevInfo, true, 9, 15},
		{"08:30 goes out", at(8), sevInfo, false, 0, 0},
		{"warning still waits", at(2), sevWarning, true, 9, 15},
		{"critical never waits", at(2), sevCritical, false, 0, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			until, quiet := quietHoursDefer(tc.now, tc.severity)
			if quiet != tc.wantQuiet {
				t.Fatalf("quiet = %v, want %v", quiet, tc.wantQuiet)
			}
			if !quiet {
				return
			}
			if until.Hour() != tc.wantHour || until.Day() != tc.wantDay {
				t.Fatalf("release at %s, want day %d hour %d",
					until.Format("02 15:04"), tc.wantDay, tc.wantHour)
			}
			if !until.After(tc.now) {
				t.Fatalf("release %s is not after %s", until, tc.now)
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, time.Minute}, // guarded, not a real case
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 16 * time.Minute},
		{99, maxRetryDelay},
	}

	for _, tc := range tests {
		if got := retryDelay(tc.attempt); got != tc.want {
			t.Errorf("retryDelay(%d) = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}

func TestSeverityOf(t *testing.T) {
	tests := []struct {
		name string
		n    notification
		want int
	}{
		{"row wins", notification{kind: "post.published", severity: sevCritical}, sevCritical},
		{"health.down defaults to critical", notification{kind: "health.down"}, sevCritical},
		{"recovery pairs with the outage", notification{kind: "health.recovered"}, sevCritical},
		{"everything else is info", notification{kind: "deploy.finished"}, sevInfo},
		{"unknown kind is info", notification{kind: "something.new"}, sevInfo},
	}

	for _, tc := range tests {
		if got := severityOf(tc.n); got != tc.want {
			t.Errorf("%s: severityOf = %d, want %d", tc.name, got, tc.want)
		}
	}
}

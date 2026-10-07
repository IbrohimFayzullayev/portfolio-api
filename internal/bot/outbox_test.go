package bot

import (
	"strings"
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

// The two kinds the public site can cause. Both carry text a stranger typed,
// so the thing worth pinning down is that it arrives escaped: Telegram parses
// these messages as HTML, and an unescaped "<" is a rejected message at best.
func TestRenderVisitorSuppliedKinds(t *testing.T) {
	b := &Bot{cfg: Config{AdminSiteURL: "https://admin.example.com/"}}

	contact := b.renderNotification(notification{
		kind: "contact.created",
		payload: map[string]any{
			"name":    "Ali <b>Valiyev</b>",
			"email":   "ali@example.com",
			"locale":  "uz",
			"message": "Salom & rahmat <script>",
		},
	})
	for _, want := range []string{
		"Ali &lt;b&gt;Valiyev&lt;/b&gt;",
		"ali@example.com · uz",
		"Salom &amp; rahmat &lt;script&gt;",
		"https://admin.example.com/messages",
	} {
		if !strings.Contains(contact, want) {
			t.Errorf("contact.created is missing %q:\n%s", want, contact)
		}
	}

	web := b.renderNotification(notification{
		kind: "error.web",
		payload: map[string]any{
			"fingerprint": "abcdef123456",
			"route":       "/uz/blog",
			"message":     "x is not a function <anonymous>",
			"count":       float64(3),
		},
	})
	for _, want := range []string{"×3", "/uz/blog", "&lt;anonymous&gt;", "/xato abcdef123456"} {
		if !strings.Contains(web, want) {
			t.Errorf("error.web is missing %q:\n%s", want, web)
		}
	}

	// A browser error is a warning: seen in the morning, not at 03:00.
	if got := defaultSeverity("error.web"); got != sevWarning {
		t.Errorf("defaultSeverity(error.web) = %d, want %d", got, sevWarning)
	}
	if got := defaultSeverity("contact.created"); got != sevInfo {
		t.Errorf("defaultSeverity(contact.created) = %d, want %d", got, sevInfo)
	}
}

// Every kind that is treated as critical has to render to something. A kind
// with a severity but no message is the worst combination there is: the row is
// marked delivered, so nothing retries it and nothing reports it missing.
// host.alert was exactly that until this test existed.
func TestCriticalKindsRender(t *testing.T) {
	b := &Bot{}
	kinds := []string{
		"health.down", "health.recovered", "error.panic", "error.rate",
		"cert.expiring", "backup.missing", "backup.offsite_failed", "host.alert",
	}
	for _, kind := range kinds {
		if defaultSeverity(kind) != sevCritical {
			t.Errorf("%s is expected to be critical", kind)
		}
		if b.renderNotification(notification{kind: kind, payload: map[string]any{}}) == "" {
			t.Errorf("%s is critical but renders to nothing — it would be silently dropped", kind)
		}
	}
}

func TestBackupMessageSaysWhereTheCopyIs(t *testing.T) {
	b := &Bot{}
	render := func(offsite any) string {
		p := map[string]any{"size": "1.2M", "duration": "3s"}
		if offsite != nil {
			p["offsite"] = offsite
		}
		return b.renderNotification(notification{kind: "backup.finished", payload: p})
	}

	if got := render("ok"); !strings.Contains(got, "Tashqi nusxa yuborildi") {
		t.Errorf("offsite ok: %q", got)
	}
	if got := render("off"); !strings.Contains(got, "Faqat shu serverda") {
		t.Errorf("offsite off: %q", got)
	}
	// A row from before the field existed keeps its old two-line shape.
	if got := render(nil); strings.Count(got, "\n") != 2 {
		t.Errorf("legacy row changed shape: %q", got)
	}
}

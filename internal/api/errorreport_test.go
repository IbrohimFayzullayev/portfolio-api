package api

import (
	"strings"
	"testing"
	"time"
)

// Grouping is the whole value of the error channel, and grouping is entirely
// decided by these two functions: if the fingerprint varies with the id in the
// message, every occurrence becomes its own group and the bot floods.

func TestNormalizeErrorText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"uuid is replaced whole",
			`post 8f3c1e2a-54d1-4a7b-9f0e-2b1c6d7e8a90 not found`,
			`post ? not found`,
		},
		{
			"numbers collapse",
			`connection refused on port 5432 after 3 attempts`,
			`connection refused on port ? after ? attempts`,
		},
		{
			"pointers collapse",
			`invalid memory address 0x1f4c2a`,
			`invalid memory address ?`,
		},
		{
			"text without variables is untouched",
			`failed to list posts: context deadline exceeded`,
			`failed to list posts: context deadline exceeded`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeErrorText(tc.in); got != tc.want {
				t.Errorf("normalizeErrorText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFingerprintGroupsSameFailure(t *testing.T) {
	route := "GET /api/v1/posts/{id}"

	a := fingerprintError("api", route, "failed to load post: no rows in result set (id 8f3c1e2a-54d1-4a7b-9f0e-2b1c6d7e8a90)")
	b := fingerprintError("api", route, "failed to load post: no rows in result set (id a91b7c33-11d4-4bbb-8a01-99c0d1e2f3a4)")
	if a != b {
		t.Errorf("same failure with different ids fingerprinted differently: %s vs %s", a, b)
	}

	other := fingerprintError("api", route, "failed to update post: deadlock detected")
	if a == other {
		t.Error("different failures share a fingerprint")
	}

	elsewhere := fingerprintError("api", "DELETE /api/v1/posts/{id}", "failed to load post: no rows in result set (id 8f3c1e2a-54d1-4a7b-9f0e-2b1c6d7e8a90)")
	if a == elsewhere {
		t.Error("same message on a different route shares a fingerprint")
	}

	if len(a) != 12 {
		t.Errorf("fingerprint length = %d, want 12", len(a))
	}
}

func TestErrorRateAlertsOncePerWindow(t *testing.T) {
	var e errorRate
	now := time.Now()

	// Below the threshold: silence.
	for i := 1; i < errorRateThreshold; i++ {
		if _, alert := e.record(now); alert {
			t.Fatalf("alerted after %d errors, threshold is %d", i, errorRateThreshold)
		}
	}

	count, alert := e.record(now)
	if !alert {
		t.Fatal("no alert at the threshold")
	}
	if count != errorRateThreshold {
		t.Errorf("count = %d, want %d", count, errorRateThreshold)
	}

	// Every further failure inside the window must stay quiet — re-alerting on
	// each one is the flood the threshold exists to prevent.
	if _, alert := e.record(now.Add(time.Second)); alert {
		t.Error("alerted twice within one window")
	}

	// A later burst, after the window, is worth hearing about again.
	later := now.Add(errorRateWindow + time.Minute)
	for i := 1; i < errorRateThreshold; i++ {
		e.record(later)
	}
	if _, alert := e.record(later); !alert {
		t.Error("no alert for a fresh burst in a new window")
	}
}

func TestErrorRateForgetsOldFailures(t *testing.T) {
	var e errorRate
	old := time.Now()

	// A slow trickle: enough failures in total, but spread far enough apart
	// that they never coexist in the window.
	for i := 0; i < errorRateThreshold*2; i++ {
		if _, alert := e.record(old.Add(time.Duration(i) * errorRateWindow)); alert {
			t.Fatalf("alerted on a trickle at step %d", i)
		}
	}
}

func TestTruncateBytesCutsAtLineBoundary(t *testing.T) {
	stack := "frame one\nframe two\nframe three\nframe four"
	got := truncateBytes(stack, 25)

	if len(got) > 30 {
		t.Errorf("result too long: %q", got)
	}
	// HasSuffix, not a last-byte comparison: "…" is three bytes of UTF-8 and
	// got[len(got)-1:] is the last one of them, which never matches.
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncation not marked: %q", got)
	}
	if !strings.HasPrefix(got, "frame one") {
		t.Errorf("kept the wrong end: %q", got)
	}
	// Short input is returned unchanged.
	if truncateBytes("short", 100) != "short" {
		t.Error("short input was modified")
	}
}

package bot

import "testing"

// The dampening rule, which is the difference between a monitor and a
// nuisance: one failed probe is usually a dropped packet or a container
// restarting, and a monitor that reports those teaches you to ignore it.

func TestWatchStateFirstRoundIsSilent(t *testing.T) {
	s := newWatchState()

	// A bot restarted during an outage must not replay the alert, and one
	// restarted after it must not announce a recovery it never saw break.
	if _, announce := s.observe("API", false); announce {
		t.Error("announced on the first observation")
	}
	if _, announce := s.observe("API", false); announce {
		t.Error("announced a state that never changed")
	}
}

func TestWatchStateNeedsConfirmation(t *testing.T) {
	s := newWatchState()
	s.observe("API", true) // first round: healthy, recorded silently

	if _, announce := s.observe("API", false); announce {
		t.Error("announced after a single failure")
	}
	verdict, announce := s.observe("API", false)
	if !announce {
		t.Fatal("no announcement after the confirming failure")
	}
	if verdict {
		t.Error("announced the wrong verdict")
	}
}

// A blip — one failure followed by a success — produces nothing at all.
func TestWatchStateIgnoresBlips(t *testing.T) {
	s := newWatchState()
	s.observe("Sayt", true)

	if _, announce := s.observe("Sayt", false); announce {
		t.Fatal("announced on the blip itself")
	}
	if _, announce := s.observe("Sayt", true); announce {
		t.Fatal("announced a recovery from an outage that was never announced")
	}
	// And the counter is reset, so the next lone failure is still just a blip.
	if _, announce := s.observe("Sayt", false); announce {
		t.Error("pending count survived the recovery")
	}
}

func TestWatchStateTracksTargetsSeparately(t *testing.T) {
	s := newWatchState()
	s.observe("API", true)
	s.observe("Sayt", true)

	s.observe("API", false)
	if _, announce := s.observe("Sayt", false); announce {
		t.Error("one target's failure confirmed another's")
	}
}

func TestCertThresholdFor(t *testing.T) {
	tests := []struct {
		days      int
		want      int
		wantAlert bool
	}{
		{90, 0, false},
		{30, 0, false},
		{15, 0, false},
		{14, 14, true},
		{8, 14, true},
		{7, 7, true},
		{3, 3, true},
		{0, 3, true},
		{-2, 3, true}, // already expired is still worth saying
	}

	for _, tc := range tests {
		got, alert := certThresholdFor(tc.days)
		if alert != tc.wantAlert {
			t.Errorf("certThresholdFor(%d): alert = %v, want %v", tc.days, alert, tc.wantAlert)
			continue
		}
		if alert && got != tc.want {
			t.Errorf("certThresholdFor(%d) = %d, want %d", tc.days, got, tc.want)
		}
	}
}

// The allow list is the bot's entire authorisation model, so it is worth a
// test of its own: the first entry is both a permitted user and the address
// every notification goes to.
func TestConfigAllowList(t *testing.T) {
	cfg := Config{AllowedUserIDs: []int64{111, 222}}

	if !cfg.allowed(111) || !cfg.allowed(222) {
		t.Error("a configured user was refused")
	}
	if cfg.allowed(333) {
		t.Error("an unconfigured user was allowed")
	}
	if cfg.notifyTarget() != 111 {
		t.Errorf("notifyTarget = %d, want the first entry", cfg.notifyTarget())
	}

	// An empty config must refuse everyone rather than defaulting to open.
	var empty Config
	if empty.allowed(0) || empty.allowed(111) {
		t.Error("an empty allow list let someone in")
	}
	if empty.notifyTarget() != 0 {
		t.Error("notifyTarget invented a recipient")
	}
}

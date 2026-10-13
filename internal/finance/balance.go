package finance

import "time"

// The balance: how much money is in hand.
//
// It is never stored. A checkpoint records what was actually there at one
// moment ("/balans 25mln" — read off the card, counted in the wallet), and the
// balance now is the latest checkpoint plus every row that came after it. A
// new checkpoint does not erase anything; it re-anchors the sum, and the gap
// between what was computed and what was counted is shown as "not written
// down" instead of silently piling up.
//
// "After" needs care, because a row carries the day the money moved and the
// moment it was typed, which are not the same thing ("kecha taksi 20k"):
//
//   - a row for a later day than the checkpoint's: after.
//   - a row for an earlier day: before — the counted balance already
//     reflected it, even if it was typed later.
//   - a row for the same day: after only if typed after the checkpoint.
//
// A checkpoint entered for a past date is anchored at the start of that day
// (the dashboard does this), so every row of that day counts after it.

type Checkpoint struct {
	ID       string    `json:"id"`
	Currency string    `json:"currency"`
	Minor    int64     `json:"amount_minor"`
	TakenAt  time.Time `json:"taken_at"`
	// What the rows said the balance was just before this checkpoint; nil
	// for the first one. Minor - *Computed is what went unrecorded.
	Computed *int64 `json:"computed_minor"`
	Note     string `json:"note"`
}

// Flow is one row as the balance sees it.
type Flow struct {
	Currency  string
	Kind      string
	Minor     int64
	Day       time.Time // a date: only Y-M-D is read
	CreatedAt time.Time
}

func ymd(t time.Time) int {
	y, m, d := t.Date()
	return y*10000 + int(m)*100 + d
}

// Covers reports whether the checkpoint's counted amount already includes f.
// loc is the zone the checkpoint's day is read in (Asia/Tashkent).
func (cp Checkpoint) Covers(f Flow, loc *time.Location) bool {
	cpDay := ymd(cp.TakenAt.In(loc))
	fDay := ymd(f.Day)
	switch {
	case fDay < cpDay:
		return true
	case fDay > cpDay:
		return false
	default:
		return !f.CreatedAt.After(cp.TakenAt)
	}
}

// Since sums the rows after a checkpoint: what came in, what went out, and
// the resulting balance.
func Since(cp Checkpoint, flows []Flow, loc *time.Location) (balance, in, out int64) {
	for _, f := range flows {
		if f.Currency != cp.Currency || cp.Covers(f, loc) {
			continue
		}
		e := CashEffect(f.Kind, f.Minor)
		if e > 0 {
			in += e
		} else {
			out -= e
		}
	}
	return cp.Minor + in - out, in, out
}

// SeriesPoint is the balance at the end of one day.
type SeriesPoint struct {
	Day   time.Time
	Minor int64
	// False before the first checkpoint: there is no balance to show.
	Known bool
}

// Series gives the end-of-day balance for every day in [from, to] in one
// currency. Checkpoints must be in TakenAt order. Each day is anchored on
// the latest checkpoint taken on or before it, so a recount mid-period
// shows as a step, which is what happened.
func Series(cps []Checkpoint, flows []Flow, currency string, from, to time.Time, loc *time.Location) []SeriesPoint {
	var own []Checkpoint
	for _, cp := range cps {
		if cp.Currency == currency {
			own = append(own, cp)
		}
	}

	var out []SeriesPoint
	for d := from; ymd(d) <= ymd(to); d = d.AddDate(0, 0, 1) {
		day := ymd(d)
		var anchor *Checkpoint
		for i := range own {
			if ymd(own[i].TakenAt.In(loc)) <= day {
				anchor = &own[i]
			}
		}
		p := SeriesPoint{Day: d}
		if anchor != nil {
			p.Known = true
			p.Minor = anchor.Minor
			for _, f := range flows {
				if f.Currency != currency || anchor.Covers(f, loc) || ymd(f.Day) > day {
					continue
				}
				p.Minor += CashEffect(f.Kind, f.Minor)
			}
		}
		out = append(out, p)
	}
	return out
}

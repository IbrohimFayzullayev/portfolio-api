// Package growth holds the pure rules behind the bot's personal-growth log:
// what "/qildim 45 w" means, when a sprint starts and ends, how far into it
// today is, and when the evening reminder is due.
//
// Work is organised in sprints only. There is deliberately no streak and no
// daily quota: both turn study into ticking a box. A sprint has one result
// written as a sentence, and at the end the only question that matters is
// whether that result happened.
//
// Nothing here touches the database or Telegram. The interesting cases are
// all about calendars and wording, and this way each one is a table row in a
// test instead of a manual check in a chat.
package growth

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// IsRestDay: Sunday is guilt-free rest. Sprints do not count it as a working
// day and the evening reminder stays quiet.
func IsRestDay(d time.Time) bool { return d.Weekday() == time.Sunday }

// Kinds, in the order reports list them.
var Kinds = []string{"writing", "speaking", "listening", "reading", "vocab", "grammar", "mock", "other"}

var kindAliases = map[string]string{
	"w": "writing", "writing": "writing", "yozish": "writing", "essay": "writing", "esse": "writing",
	"s": "speaking", "speaking": "speaking", "gapirish": "speaking",
	"l": "listening", "listening": "listening", "tinglash": "listening",
	"r": "reading", "reading": "reading", "oqish": "reading", "o'qish": "reading",
	"v": "vocab", "vocab": "vocab", "soz": "vocab", "so'z": "vocab", "anki": "vocab",
	"g": "grammar", "grammar": "grammar", "grammatika": "grammar",
	"m": "mock", "mock": "mock",
}

// NormalizeApostrophes folds the several apostrophes Uzbek text arrives with
// (phone keyboards disagree) into the plain one the alias tables use.
func NormalizeApostrophes(s string) string {
	return strings.NewReplacer("’", "'", "‘", "'", "ʻ", "'", "ʼ", "'", "`", "'").Replace(s)
}

var (
	errNoMinutes  = errors.New("daqiqa topilmadi")
	errBadMinutes = errors.New("daqiqa 1–600 oralig'ida bo'lishi kerak")
)

// IsNoMinutes reports whether parsing failed only because no duration was given.
func IsNoMinutes(err error) bool { return errors.Is(err, errNoMinutes) }

// Log is one parsed /qildim.
type Log struct {
	Kind    string
	Minutes int
	Note    string
	// 0 for today, -1 for "kecha".
	DayOffset int
}

// durationToken matches "45", "45m", "45daq", "1s", "1soat", "1.5soat", "1,5h".
var durationToken = regexp.MustCompile(`^(\d+(?:[.,]\d+)?)(m|min|d|daq|daqiqa|s|soat|h)?$`)

// parseDuration reads one token as minutes. A bare number is minutes; a
// fraction is only accepted with an hour suffix, where it means something.
func parseDuration(tok string) (int, bool) {
	m := durationToken.FindStringSubmatch(strings.ToLower(tok))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
	if err != nil {
		return 0, false
	}
	switch m[2] {
	case "s", "soat", "h":
		return int(math.Round(v * 60)), true
	default:
		if v != math.Trunc(v) {
			return 0, false
		}
		return int(v), true
	}
}

// ParseLog reads "/qildim" arguments in any order: "45 writing", "w 45 essay 2",
// "kecha 30 s". The first duration and the first kind win; everything else is
// the note.
func ParseLog(args string) (Log, error) {
	l := Log{Kind: "other"}
	kindSet, minSet := false, false
	var note []string

	for _, raw := range strings.Fields(args) {
		tok := strings.ToLower(NormalizeApostrophes(raw))
		if !minSet {
			if n, ok := parseDuration(tok); ok {
				l.Minutes, minSet = n, true
				continue
			}
		}
		if !kindSet {
			if k, ok := kindAliases[tok]; ok {
				l.Kind, kindSet = k, true
				continue
			}
		}
		if tok == "kecha" && l.DayOffset == 0 {
			l.DayOffset = -1
			continue
		}
		note = append(note, raw)
	}

	l.Note = strings.Join(note, " ")
	if !minSet {
		return l, errNoMinutes
	}
	if l.Minutes < 1 || l.Minutes > 600 {
		return l, errBadMinutes
	}
	return l, nil
}

// DayKey is the map key for a calendar day.
func DayKey(t time.Time) string { return t.Format("2006-01-02") }

func midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// SprintWindow is where a sprint opened now begins and ends. Monday to Friday
// it starts today; on the weekend it starts next Monday, because a sprint that
// begins on a rest day begins with a failure. It always ends on the Saturday
// of its week — the result day.
func SprintWindow(now time.Time) (start, end time.Time) {
	today := midnight(now)
	switch today.Weekday() {
	case time.Saturday:
		start = today.AddDate(0, 0, 2)
	case time.Sunday:
		start = today.AddDate(0, 0, 1)
	default:
		start = today
	}
	end = start.AddDate(0, 0, int(time.Saturday-start.Weekday()))
	return start, end
}

// SprintDay is "day n of total", counting working days only. n is 0 before the
// sprint starts and total once it is over.
func SprintDay(start, end, today time.Time) (n, total int) {
	start, end, today = midnight(start), midnight(end), midnight(today)
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if IsRestDay(d) {
			continue
		}
		total++
		if !d.After(today) {
			n++
		}
	}
	return n, total
}

// The evening reminder. There is no daily quota to fall short of: the only
// question is whether the sprint was touched at all today. 21:00 leaves an
// evening to do it in; nothing is sent from 23:00, which is quiet hours.
const (
	nudgeHour     = 21
	nudgeLastHour = 23
)

// NudgeDue reports whether the evening reminder should be queued right now,
// for a sprint working day on which nothing has been written down yet. It is
// asked every minute and only says yes once the moment has come — queuing it
// in advance would deliver it even after the work was logged at 20:50.
func NudgeDue(now time.Time, todayMinutes int) bool {
	if IsRestDay(now) || todayMinutes > 0 {
		return false
	}
	return now.Hour() >= nudgeHour && now.Hour() < nudgeLastHour
}

// ActiveDays counts the working days in [from, to] on which anything was
// logged. It is a measure of rhythm, not of a target: the target of a sprint
// is its one result sentence, checked at the end.
func ActiveDays(minutesByDay map[string]int, from, to time.Time) (active, working int) {
	from, to = midnight(from), midnight(to)
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if IsRestDay(d) {
			continue
		}
		working++
		if minutesByDay[DayKey(d)] > 0 {
			active++
		}
	}
	return active, working
}

// Sprint outcomes, set when a sprint is closed.
var Outcomes = map[string]string{
	"done":    "✅ Natijaga erishildi",
	"partial": "🟡 Qisman",
	"missed":  "❌ Erishilmadi",
}

// FormatMinutes renders a duration the way a person says it: "45 daq",
// "1 s 20 daq", "3 s".
func FormatMinutes(m int) string {
	if m < 60 {
		return fmt.Sprintf("%d daq", m)
	}
	h, rest := m/60, m%60
	if rest == 0 {
		return fmt.Sprintf("%d s", h)
	}
	return fmt.Sprintf("%d s %d daq", h, rest)
}

// Sparkline draws one bar per value, scaled to the largest. Zero is the
// lowest bar rather than a gap, so a week of reading still reads as seven days.
func Sparkline(values []int) string {
	bars := []rune("▁▂▃▄▅▆▇█")
	max := 0
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	var sb strings.Builder
	for _, v := range values {
		i := 0
		if max > 0 && v > 0 {
			i = 1 + (v*(len(bars)-2)+max-1)/max
			if i >= len(bars) {
				i = len(bars) - 1
			}
		}
		sb.WriteRune(bars[i])
	}
	return sb.String()
}

// WeekdayShort is the Uzbek two-letter weekday.
func WeekdayShort(d time.Weekday) string {
	return [...]string{"Ya", "Du", "Se", "Ch", "Pa", "Ju", "Sh"}[d]
}

// WeekdayName is the full Uzbek weekday.
func WeekdayName(d time.Weekday) string {
	return [...]string{"yakshanba", "dushanba", "seshanba", "chorshanba", "payshanba", "juma", "shanba"}[d]
}

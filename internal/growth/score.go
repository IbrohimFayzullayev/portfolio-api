package growth

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Score is one parsed /ball: an overall band and, optionally, the four parts.
// Bands are held as halves (6.5 → 13) so equality and deltas are exact.
type Score struct {
	OverallHalves int
	// 0 means "not given"; a real band of 0 is not a case worth supporting.
	Listening, Reading, Writing, Speaking int
	Kind                                  string // mock | official | tutor
	Note                                  string
}

var (
	errNoOverall = errors.New("umumiy ball topilmadi")
	errBadBand   = errors.New("ball 0–9 oralig'ida va 0.5 qadamli bo'lishi kerak")
)

var (
	bandToken = regexp.MustCompile(`^\d(?:[.,][05])?$`)
	partToken = regexp.MustCompile(`^([lrws])[:=]?(\d(?:[.,]\d+)?)$`)
)

// looksLikeBand catches "6.3" so it is refused instead of becoming a note.
var looksLikeBand = regexp.MustCompile(`^\d(?:[.,]\d+)?$`)

var scoreKinds = map[string]string{
	"mock": "mock", "m": "mock",
	"rasmiy": "official", "official": "official", "imtihon": "official",
	"tutor": "tutor", "repetitor": "tutor", "ustoz": "tutor",
}

// ParseBand reads "6", "6.5" or "6,5" into halves.
func ParseBand(s string) (int, error) {
	if !bandToken.MatchString(s) {
		return 0, errBadBand
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
	if err != nil || v < 0 || v > 9 {
		return 0, errBadBand
	}
	return int(v * 2), nil
}

// FormatBand renders halves back as "6.5" / "7.0".
func FormatBand(halves int) string {
	return fmt.Sprintf("%.1f", float64(halves)/2)
}

// ParseScore reads "/ball 6.0 L6.5 R6 W5.5 S5.5 mock izoh".
func ParseScore(args string) (Score, error) {
	s := Score{Kind: "mock"}
	var note []string
	overallSet, kindSet := false, false

	for _, raw := range strings.Fields(args) {
		tok := strings.ToLower(raw)
		if m := partToken.FindStringSubmatch(tok); m != nil {
			v, err := ParseBand(m[2])
			if err != nil {
				return s, err
			}
			switch m[1] {
			case "l":
				s.Listening = v
			case "r":
				s.Reading = v
			case "w":
				s.Writing = v
			case "s":
				s.Speaking = v
			}
			continue
		}
		if !overallSet && looksLikeBand.MatchString(tok) {
			v, err := ParseBand(tok)
			if err != nil {
				return s, err
			}
			s.OverallHalves, overallSet = v, true
			continue
		}
		if k, ok := scoreKinds[tok]; ok && !kindSet {
			s.Kind, kindSet = k, true
			continue
		}
		note = append(note, raw)
	}
	s.Note = strings.Join(note, " ")
	if !overallSet {
		return s, errNoOverall
	}
	return s, nil
}

// ScoreKindLabel is the Uzbek label for a score kind.
func ScoreKindLabel(kind string) string {
	switch kind {
	case "official":
		return "rasmiy"
	case "tutor":
		return "repetitor"
	}
	return "mock"
}

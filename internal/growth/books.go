package growth

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Book commands.
//
//	/kitob yangi O'tkan kunlar - Abdulla Qodiriy 420 bet
//	/kitob 40                 40 minutes on the current book
//	/kitob 40 120-bet         ... and reached page 120
//	/kitob kecha 1s Kecha va kunduz 85b
//	/kitob tugatdim
//
// Anything that is neither a duration, a page, nor "kecha" is a piece of a
// title, used to pick a book when more than one is being read.

// NewBook is a parsed "/kitob yangi".
type NewBook struct {
	Title  string
	Author string
	Pages  int
}

var (
	errNoTitle = errors.New("kitob nomi bo'sh")
	// "420 bet", "420-bet", "420bet", "420b", "420 sahifa" at the very end.
	trailingPages = regexp.MustCompile(`(?i)\s*(\d{1,5})\s*-?\s*(bet|b|sahifa|sah|p)\.?\s*$`)
	// Title and author are separated by a dash with spaces around it, so a
	// hyphenated word inside a title is left alone.
	authorSeparator = regexp.MustCompile(`\s+[-–—]\s+`)
)

// ParseNewBook reads "Title - Author 320 bet". Author and pages are optional.
func ParseNewBook(args string) (NewBook, error) {
	s := strings.TrimSpace(NormalizeApostrophes(args))
	var b NewBook

	if m := trailingPages.FindStringSubmatchIndex(s); m != nil {
		b.Pages, _ = strconv.Atoi(s[m[2]:m[3]])
		s = strings.TrimSpace(s[:m[0]])
	}

	parts := authorSeparator.Split(s, 2)
	b.Title = strings.Trim(strings.TrimSpace(parts[0]), `"«»“”`)
	if len(parts) == 2 {
		b.Author = strings.TrimSpace(parts[1])
	}
	if b.Title == "" {
		return b, errNoTitle
	}
	if len([]rune(b.Title)) > 200 {
		b.Title = string([]rune(b.Title)[:200])
	}
	return b, nil
}

// BookLog is a parsed reading session.
type BookLog struct {
	Minutes   int
	Page      int    // page reached; 0 when not given
	Query     string // words that pick the book; "" means the current one
	DayOffset int
}

// pageToken matches "120-bet", "120bet", "120b", "b120", "p120".
var (
	pageSuffix = regexp.MustCompile(`^(\d{1,5})-?(bet|b|sahifa|sah|p)\.?$`)
	pagePrefix = regexp.MustCompile(`^(?:b|p|bet)(\d{1,5})$`)
)

// ParseBookLog reads a session. A number followed by the word "bet" is a page,
// not a duration: "/kitob 40 120 bet".
func ParseBookLog(args string) (BookLog, error) {
	var l BookLog
	toks := strings.Fields(NormalizeApostrophes(args))
	var query []string
	minSet := false

	for i := 0; i < len(toks); i++ {
		tok := strings.ToLower(toks[i])

		if m := pageSuffix.FindStringSubmatch(tok); m != nil && l.Page == 0 {
			l.Page, _ = strconv.Atoi(m[1])
			continue
		}
		if m := pagePrefix.FindStringSubmatch(tok); m != nil && l.Page == 0 {
			l.Page, _ = strconv.Atoi(m[1])
			continue
		}
		if n, err := strconv.Atoi(tok); err == nil && l.Page == 0 && i+1 < len(toks) {
			next := strings.ToLower(toks[i+1])
			if next == "bet" || next == "sahifa" || next == "b" {
				l.Page = n
				i++
				continue
			}
		}
		if !minSet {
			if n, ok := parseDuration(tok); ok {
				l.Minutes, minSet = n, true
				continue
			}
		}
		if tok == "kecha" && l.DayOffset == 0 {
			l.DayOffset = -1
			continue
		}
		query = append(query, toks[i])
	}

	l.Query = strings.Join(query, " ")
	if !minSet {
		return l, errNoMinutes
	}
	if l.Minutes < 1 || l.Minutes > 720 {
		return l, errors.New("daqiqa 1–720 oralig'ida bo'lishi kerak")
	}
	return l, nil
}

// Percent is how far through a book a page is, clamped to 0–100.
func Percent(page, total int) int {
	if page <= 0 || total <= 0 {
		return 0
	}
	p := page * 100 / total
	if p > 100 {
		return 100
	}
	return p
}

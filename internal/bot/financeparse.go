package bot

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ibrohimcoder/portfolio-api/internal/finance"
)

// Reading one line of money talk.
//
// The whole point of the feature is that writing things down costs nothing,
// so the input is whatever a person would type into a chat: "tushlik 35k",
// "Aliga qarz 500 ming", "kecha taksi 18000", "Ali 200k qaytardi". Nothing
// here asks a follow-up question. A wrong guess is shown back immediately
// with buttons to fix it, which is cheaper than making every line precise.
//
// Everything in this file is pure: text and today's date in, an entry out.

// The kinds and categories themselves live in internal/finance, shared with
// the API; these are the short local names the parser and the bot use.
const (
	kindExpense   = finance.KindExpense
	kindGift      = finance.KindGift
	kindLent      = finance.KindLent
	kindRepaidIn  = finance.KindRepaidIn
	kindBorrowed  = finance.KindBorrowed
	kindRepaidOut = finance.KindRepaidOut
)

func isDebtKind(kind string) bool { return finance.IsDebt(kind) }

// needsPerson: an entry of this kind means nothing without a name.
func needsPerson(kind string) bool { return finance.IsDebt(kind) }

func kindOf(key string) finance.Kind {
	k, _ := finance.KindOf(key)
	return k
}

func categoryByKey(key string) finance.Category { return finance.CategoryByKey(key) }

// Suffixes a stem may carry and still count as that word.
var stemSuffixes = []string{
	"", "ga", "ka", "qa", "da", "ta", "dan", "tan", "ni", "ning", "i", "si",
	"lar", "larga", "larni", "lari", "im", "imga", "imni", "imiz", "ing", "lik",
}

func matchesStem(word, stem string) bool {
	if !strings.HasPrefix(word, stem) {
		return false
	}
	rest := word[len(stem):]
	// Long stems are distinctive enough to accept any ending:
	// "korzinkadagi", "restoranlarda".
	if utf8.RuneCountInString(stem) >= 5 {
		return true
	}
	for _, s := range stemSuffixes {
		if rest == s {
			return true
		}
	}
	return false
}

func categoryOf(word string) string {
	for _, c := range finance.Categories {
		for _, s := range c.Stems {
			if matchesStem(word, s) {
				return c.Key
			}
		}
	}
	return ""
}

// Words that are never a name, never a category, and say nothing about kind.
var stopWords = map[string]bool{
	"men": true, "menga": true, "mendan": true, "meni": true, "mening": true,
	"sen": true, "senga": true, "sendan": true, "u": true, "unga": true,
	"undan": true, "bu": true, "bunga": true, "shu": true, "shunga": true,
	"uchun": true, "va": true, "bilan": true, "ham": true, "pul": true,
	"pulni": true, "pulga": true, "naqd": true, "karta": true, "kartadan": true,
	"kartaga": true, "plastik": true, "click": true, "payme": true, "uzum": true,
	"ta": true, "dona": true, "bir": true, "kim": true, "kimga": true,
	"nima": true, "xarajat": true, "harajat": true, "bugun": true, "kecha": true,
	"so'm": true, "som": true, "sum": true, "$": true, "dollar": true,
	"usd": true, "k": true, "ming": true, "mln": true, "ga": true,
	"oyga": true, "kunga": true, "haftaga": true, "yilga": true,
	"berdim": true, "berdi": true, "berib": true, "oldim": true, "oldi": true,
	"olindi": true, "qaytardim": true, "qaytardi": true, "qaytarib": true,
	"qaytdi": true, "qaytarishdi": true, "to'ladim": true, "to'ladi": true,
	"uzdim": true, "uzdi": true, "ketdi": true, "ishlatdim": true,
	"sarfladim": true, "sotib": true, "bo'ldi": true,
}

// Words that follow a name rather than replace it: "Ali aka", "Dilnoza opa".
var kinshipWords = map[string]bool{
	"aka": true, "opa": true, "uka": true, "singil": true, "ota": true,
	"ona": true, "amaki": true, "tog'a": true, "xola": true, "amma": true,
	"bobo": true, "buvi": true, "domla": true, "ustoz": true,
}

var giftStems = []string{"sovg'a", "hadya", "yordam", "ehson", "sadaqa"}

type parsedEntry struct {
	kind        string
	amountMinor int64
	currency    string
	category    string
	person      string
	note        string
	spentOn     time.Time
}

var (
	errNoAmount = errors.New("no amount")
	errNoPerson = errors.New("no person")
)

// Telegram keyboards produce at least five different apostrophes for o' and
// g'. Matching is done on one.
var apostrophes = strings.NewReplacer(
	"ʻ", "'", "ʼ", "'", "‘", "'", "’", "'", "`", "'", "´", "'",
)

func normalizeWord(w string) string {
	w = strings.ToLower(apostrophes.Replace(w))
	return strings.Trim(w, ",.!?;:()\"«»")
}

func cleanOriginal(w string) string {
	return strings.Trim(apostrophes.Replace(w), ",.!?;:()\"«»")
}

func parseEntry(text string, today time.Time) (parsedEntry, error) {
	orig := strings.Fields(text)
	norm := make([]string, len(orig))
	for i, w := range orig {
		norm[i] = normalizeWord(w)
	}
	used := make([]bool, len(orig))

	e := parsedEntry{spentOn: today, currency: "UZS"}

	// The date first, so "12.10" is never read as twelve so'm.
	for i := range norm {
		if d, n, ok := parseDateAt(norm, used, i, today); ok {
			e.spentOn = d
			markUsed(used, i, n)
			break
		}
	}

	found := false
	for i := range norm {
		if used[i] {
			continue
		}
		if minor, cur, n, ok := parseAmountAt(norm, used, i); ok {
			e.amountMinor, e.currency = minor, cur
			markUsed(used, i, n)
			found = true
			break
		}
	}
	if !found {
		return e, errNoAmount
	}

	words := make([]int, 0, len(norm))
	for i := range norm {
		if !used[i] && norm[i] != "" {
			words = append(words, i)
		}
	}
	has := func(set ...string) bool {
		for _, i := range words {
			for _, s := range set {
				if norm[i] == s {
					return true
				}
			}
		}
		return false
	}
	hasQarz := false
	hasGiftWord := false
	for _, i := range words {
		if strings.HasPrefix(norm[i], "qarz") {
			hasQarz = true
		}
		for _, s := range giftStems {
			if matchesStem(norm[i], s) {
				hasGiftWord = true
			}
		}
	}

	dative := findPerson(orig, norm, used, words, dativeStem)
	ablative := findPerson(orig, norm, used, words, ablativeStem)

	switch {
	case has("qaytardim", "uzdim") || (has("qaytarib") && has("berdim")) ||
		(hasQarz && has("to'ladim")):
		e.kind = kindRepaidOut
		e.person = firstNonEmpty(dative, ablative)

	case has("qaytardi", "qaytdi", "qaytarishdi", "uzdi") ||
		(has("qaytarib") && has("berdi")) || (hasQarz && has("to'ladi")):
		e.kind = kindRepaidIn
		e.person = firstNonEmpty(ablative, nominative(orig, norm, words), dative)

	case hasQarz && (has("oldim", "olindi") || (ablative != "" && dative == "")):
		e.kind = kindBorrowed
		e.person = firstNonEmpty(ablative, dative)

	case hasQarz:
		e.kind = kindLent
		e.person = dative

	case hasGiftWord || (has("berdim") && dative != "") || capitalisedDative(orig, norm, used, words):
		e.kind = kindGift
		e.person = dative

	default:
		e.kind = kindExpense
	}

	if needsPerson(e.kind) && e.person == "" {
		return e, errNoPerson
	}

	if isDebtKind(e.kind) {
		e.category = "qarz"
	} else {
		e.category = "boshqa"
		for _, i := range words {
			if c := categoryOf(norm[i]); c != "" {
				e.category = c
				break
			}
		}
	}

	noteWords := make([]string, 0, len(words))
	for _, i := range words {
		noteWords = append(noteWords, cleanOriginal(orig[i]))
	}
	e.note = strings.TrimSpace(strings.Join(noteWords, " "))

	return e, nil
}

func markUsed(used []bool, from, n int) {
	for j := from; j < from+n && j < len(used); j++ {
		used[j] = true
	}
}

// isPlainWord: could be a name — not a number, not a keyword, not a category.
func isPlainWord(w string) bool {
	if w == "" || stopWords[w] || strings.HasPrefix(w, "qarz") || categoryOf(w) != "" {
		return false
	}
	for _, s := range giftStems {
		if matchesStem(w, s) {
			return false
		}
	}
	r, _ := utf8.DecodeRuneInString(w)
	return unicode.IsLetter(r)
}

// dativeStem: "Aliga" → "Ali". The suffix is ASCII, so cutting bytes is safe.
func dativeStem(w string) (string, bool) {
	for _, suf := range []string{"ga", "ka", "qa"} {
		if strings.HasSuffix(strings.ToLower(w), suf) && utf8.RuneCountInString(w) >= 4 {
			return w[:len(w)-2], true
		}
	}
	return "", false
}

// ablativeStem: "Alidan" → "Ali".
func ablativeStem(w string) (string, bool) {
	for _, suf := range []string{"dan", "tan"} {
		if strings.HasSuffix(strings.ToLower(w), suf) && utf8.RuneCountInString(w) >= 5 {
			return w[:len(w)-3], true
		}
	}
	return "", false
}

// findPerson returns the first word carrying the given case ending, with the
// name before it when the word itself is "aka", "opa" and the like.
func findPerson(orig, norm []string, used []bool, words []int, stem func(string) (string, bool)) string {
	for _, i := range words {
		if !isPlainWord(norm[i]) || kinshipWords[norm[i]] {
			continue
		}
		s, ok := stem(cleanOriginal(orig[i]))
		if !ok {
			continue
		}
		name := capitalise(s)
		if kinshipWords[strings.ToLower(s)] && i > 0 && !used[i-1] && isPlainWord(norm[i-1]) {
			name = capitalise(cleanOriginal(orig[i-1])) + " " + strings.ToLower(s)
		}
		return name
	}
	return ""
}

// nominative: "Ali qaytardi" — the first word that could be a name, plus a
// kinship word right after it.
func nominative(orig, norm []string, words []int) string {
	for k, i := range words {
		if !isPlainWord(norm[i]) || kinshipWords[norm[i]] {
			continue
		}
		name := capitalise(cleanOriginal(orig[i]))
		if k+1 < len(words) && words[k+1] == i+1 && kinshipWords[norm[i+1]] {
			name += " " + norm[i+1]
		}
		return name
	}
	return ""
}

// capitalisedDative: "Aliga 50k" with no verb. Things are rarely written with
// a capital letter and names usually are, so that alone reads as giving.
func capitalisedDative(orig, norm []string, used []bool, words []int) bool {
	for _, i := range words {
		if !isPlainWord(norm[i]) {
			continue
		}
		if _, ok := dativeStem(norm[i]); !ok {
			continue
		}
		r, _ := utf8.DecodeRuneInString(cleanOriginal(orig[i]))
		if unicode.IsUpper(r) {
			return true
		}
		// "Dilnoza opaga": the kinship word carries the case ending, the
		// name before it carries the capital.
		if stem, _ := dativeStem(norm[i]); kinshipWords[stem] && i > 0 && !used[i-1] {
			r, _ := utf8.DecodeRuneInString(cleanOriginal(orig[i-1]))
			if unicode.IsUpper(r) && isPlainWord(norm[i-1]) {
				return true
			}
		}
	}
	return false
}

func capitalise(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

/* -------------------------------- amounts --------------------------------- */

var amountRe = regexp.MustCompile(
	`^(\$)?(\d+(?:[.,]\d+)*)(k|ming|mln|million|milyon|mil|m)?(\$|usd|so'm|som|sum|dollar)?$`)

var groupedRe = regexp.MustCompile(`^\d{1,3}(?:[.,]\d{3})+$`)

var multiplierWords = map[string]float64{
	"k": 1e3, "ming": 1e3,
	"mln": 1e6, "million": 1e6, "milyon": 1e6, "mil": 1e6, "m": 1e6,
}

var countWords = map[string]bool{
	"ta": true, "dona": true, "kishi": true, "kun": true, "kunlik": true,
	"marta": true, "soat": true, "oy": true, "oylik": true, "kg": true,
	"kilo": true, "litr": true, "l": true, "hafta": true, "yil": true,
}

var currencyWords = map[string]string{
	"$": "USD", "usd": "USD", "dollar": "USD", "dollr": "USD",
	"so'm": "UZS", "som": "UZS", "sum": "UZS",
}

func parseAmountAt(norm []string, used []bool, i int) (minor int64, currency string, consumed int, ok bool) {
	m := amountRe.FindStringSubmatch(norm[i])
	if m == nil {
		return 0, "", 0, false
	}
	num, mult, cur := m[2], m[3], ""
	if m[1] != "" || m[4] != "" {
		cur = currencyWords[m[1]+m[4]]
	}
	consumed = 1

	next := func() string {
		j := i + consumed
		if j < len(norm) && !used[j] {
			return norm[j]
		}
		return ""
	}

	// "1 250 000": the groups arrive as separate words.
	if mult == "" && cur == "" && len(num) <= 3 && !strings.ContainsAny(num, ".,") {
		for {
			n := next()
			if len(n) == 3 && isDigits(n) {
				num += n
				consumed++
				continue
			}
			break
		}
	}
	// "2 ta non 8000": a count, not the price.
	if consumed == 1 && mult == "" && cur == "" && countWords[next()] {
		return 0, "", 0, false
	}
	if mult == "" {
		if _, ok := multiplierWords[next()]; ok {
			mult = next()
			consumed++
		}
	}
	if cur == "" {
		if c, ok := currencyWords[next()]; ok {
			cur = c
			consumed++
		}
	}
	explicitUZS := cur == "UZS"
	if cur == "" {
		cur = "UZS"
	}

	var value float64
	switch {
	case mult == "" && groupedRe.MatchString(num):
		// "35.000" and "1,250,000" — separators, not decimals.
		v, err := strconv.ParseFloat(strings.NewReplacer(".", "", ",", "").Replace(num), 64)
		if err != nil {
			return 0, "", 0, false
		}
		value = v
	case strings.Count(num, ".")+strings.Count(num, ",") <= 1:
		v, err := strconv.ParseFloat(strings.Replace(num, ",", ".", 1), 64)
		if err != nil {
			return 0, "", 0, false
		}
		value = v
	default:
		return 0, "", 0, false
	}

	if mult != "" {
		value *= multiplierWords[mult]
	} else if cur == "UZS" && !explicitUZS && value < 1000 && value == math.Trunc(value) {
		// Nothing costs 35 so'm. "tushlik 35" means 35 000.
		value *= 1000
	}

	minor = int64(math.Round(value * 100))
	if minor <= 0 {
		return 0, "", 0, false
	}
	return minor, cur, consumed, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseAmount reads a whole reply as an amount — the "Summa" edit button.
func parseAmount(text string) (int64, string, bool) {
	fields := strings.Fields(text)
	norm := make([]string, len(fields))
	for i, f := range fields {
		norm[i] = normalizeWord(f)
	}
	used := make([]bool, len(norm))
	for i := range norm {
		if minor, cur, _, ok := parseAmountAt(norm, used, i); ok {
			return minor, cur, true
		}
	}
	return 0, "", false
}

/* --------------------------------- dates ---------------------------------- */

var dateRe = regexp.MustCompile(`^(\d{1,2})[./](\d{1,2})(?:[./](\d{2}|\d{4}))?$`)

func parseDateAt(norm []string, used []bool, i int, today time.Time) (time.Time, int, bool) {
	w := norm[i]
	switch w {
	case "bugun":
		return today, 1, true
	case "kecha":
		return today.AddDate(0, 0, -1), 1, true
	case "o'tgan", "avvalgi":
		if i+1 < len(norm) && !used[i+1] && norm[i+1] == "kuni" {
			return today.AddDate(0, 0, -2), 2, true
		}
		return time.Time{}, 0, false
	}

	m := dateRe.FindStringSubmatch(w)
	if m == nil {
		return time.Time{}, 0, false
	}
	// "1.5 mln" is an amount, not the first of May.
	if i+1 < len(norm) {
		next := norm[i+1]
		if _, ok := multiplierWords[next]; ok {
			return time.Time{}, 0, false
		}
		if _, ok := currencyWords[next]; ok {
			return time.Time{}, 0, false
		}
	}
	return dateFromParts(m[1], m[2], m[3], today)
}

func dateFromParts(dd, mm, yy string, today time.Time) (time.Time, int, bool) {
	day, _ := strconv.Atoi(dd)
	month, _ := strconv.Atoi(mm)
	year := today.Year()
	if yy != "" {
		year, _ = strconv.Atoi(yy)
		if year < 100 {
			year += 2000
		}
	}
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, 0, false
	}
	d := time.Date(year, time.Month(month), day, 0, 0, 0, 0, today.Location())
	if d.Day() != day {
		return time.Time{}, 0, false // 31.02
	}
	// Without a year, a date ahead of today is last year's.
	if yy == "" && d.After(today) {
		d = d.AddDate(-1, 0, 0)
	}
	return d, 1, true
}

// parseDay reads a whole reply as a date — the "Sana" edit button.
func parseDay(text string, today time.Time) (time.Time, bool) {
	fields := strings.Fields(text)
	norm := make([]string, len(fields))
	for i, f := range fields {
		norm[i] = normalizeWord(f)
	}
	if len(norm) == 0 {
		return time.Time{}, false
	}
	d, _, ok := parseDateAt(norm, make([]bool, len(norm)), 0, today)
	return d, ok
}

/* ------------------------------- formatting ------------------------------- */

// formatMoney: "35 000 so'm", "$12.50", "$1 200".
func formatMoney(minor int64, currency string) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	whole, cents := minor/100, minor%100

	s := groupThousands(whole)
	if cents != 0 {
		s += "." + strconv.FormatInt(cents/10, 10) + strconv.FormatInt(cents%10, 10)
	}

	if currency == "USD" {
		s = "$" + s
	} else {
		s += " so'm"
	}
	if neg {
		s = "-" + s
	}
	return s
}

func groupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// money is an amount per currency. Most people will only ever have so'm in
// it; a dollar loan should still add up without being converted at a rate
// nobody wrote down.
type money map[string]int64

func (m money) add(currency string, minor int64) { m[currency] += minor }

func (m money) isZero() bool {
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func (m money) String() string {
	parts := make([]string, 0, 2)
	for _, cur := range []string{"UZS", "USD"} {
		if v := m[cur]; v != 0 {
			parts = append(parts, formatMoney(v, cur))
		}
	}
	if len(parts) == 0 {
		return "0 so'm"
	}
	return strings.Join(parts, " + ")
}

var uzMonths = []string{
	"Yanvar", "Fevral", "Mart", "Aprel", "May", "Iyun",
	"Iyul", "Avgust", "Sentabr", "Oktabr", "Noyabr", "Dekabr",
}

func monthName(m time.Month) string { return uzMonths[m-1] }

// dayLabel: "13.10 (bugun)", "12.10 (kecha)", "05.10".
func dayLabel(d, today time.Time) string {
	s := d.Format("02.01")
	if d.Year() != today.Year() {
		s = d.Format("02.01.2006")
	}
	switch {
	case sameDay(d, today):
		s += " (bugun)"
	case sameDay(d, today.AddDate(0, 0, -1)):
		s += " (kecha)"
	}
	return s
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}

// dayStart is today's date at midnight in Tashkent — what spent_on means.
func dayStart(t time.Time) time.Time {
	t = t.In(tashkent)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, tashkent)
}

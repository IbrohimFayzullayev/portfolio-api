// Package finance is what the bot, the API and the dashboard agree on about
// the money log: which kinds of entry exist, which categories, and how a
// person's debt balance follows from their rows. The bot's text parser and the
// API's HTTP handlers both build on it, so a label changed here changes in
// Telegram and on the page at the same time.
package finance

const (
	KindExpense   = "expense"    // spent on myself
	KindGift      = "gift"       // given away, not expected back
	KindLent      = "lent"       // loan I gave: they owe me more
	KindRepaidIn  = "repaid_in"  // they paid back: they owe me less
	KindBorrowed  = "borrowed"   // loan I took: I owe more
	KindRepaidOut = "repaid_out" // I paid back: I owe less
	KindIncome    = "income"     // money earned or received: salary, freelance
)

type Kind struct {
	Key string `json:"key"`
	// One letter, for Telegram callback data (64 bytes in all).
	Code  string `json:"-"`
	Emoji string `json:"emoji"`
	Label string `json:"label"`
	// Moves a person's balance.
	Debt bool `json:"debt"`
	// Money that left and is not coming back: what "spent" sums.
	Spending bool `json:"spending"`
	// Which way the money in hand moves: +1 in, -1 out. Every kind moves it;
	// a loan given is not spending, but the cash is gone from the pocket.
	Cash int `json:"cash"`
}

// Kinds in picker order: the common case first.
var Kinds = []Kind{
	{KindExpense, "e", "💸", "Xarajat", false, true, -1},
	{KindIncome, "k", "💰", "Kirim", false, false, +1},
	{KindGift, "g", "🎁", "Berdim", false, true, -1},
	{KindLent, "l", "🤝", "Qarz berdim", true, false, -1},
	{KindRepaidIn, "i", "↩️", "Qarz qaytdi", true, false, +1},
	{KindBorrowed, "b", "🫴", "Qarz oldim", true, false, +1},
	{KindRepaidOut, "o", "↪️", "Qarzni qaytardim", true, false, -1},
}

// KindOf returns the kind with this key; ok is false for an unknown key.
func KindOf(key string) (Kind, bool) {
	for _, k := range Kinds {
		if k.Key == key {
			return k, true
		}
	}
	return Kind{Key: key, Label: key}, false
}

func KindByCode(code string) string {
	for _, k := range Kinds {
		if k.Code == code {
			return k.Key
		}
	}
	return ""
}

// IsDebt: the four kinds that move a person's balance. An entry of one of
// these means nothing without a name.
func IsDebt(kind string) bool {
	k, ok := KindOf(kind)
	return ok && k.Debt
}

func IsSpending(kind string) bool {
	k, ok := KindOf(kind)
	return ok && k.Spending
}

func IsIncome(kind string) bool { return kind == KindIncome }

// CashEffect is a row's effect on the money in hand.
func CashEffect(kind string, minor int64) int64 {
	k, ok := KindOf(kind)
	if !ok {
		return 0
	}
	return int64(k.Cash) * minor
}

type Category struct {
	Key   string   `json:"key"` // ASCII only: it goes into callback data
	Emoji string   `json:"emoji"`
	Label string   `json:"label"`
	Stems []string `json:"-"`
}

// The guess list. A word matches a stem when it is the stem itself or the
// stem plus a common suffix ("taksiga", "bozordan"), so "gul" catches
// "gulga" but not "Gulnora". Order matters only when one message mentions
// two categories: the first word that matches wins.
var Categories = []Category{
	{"ovqat", "🍽", "Ovqat", []string{
		"tushlik", "nonushta", "kechki", "ovqat", "kafe", "restoran", "choyxona",
		"kofe", "coffee", "choy", "osh", "somsa", "lavash", "burger", "shashlik",
		"fastfud", "fastfood", "pitsa", "pizza", "shirinlik", "muzqaymoq", "tort",
		"hotdog", "donar", "shaurma", "manti", "chuchvara", "lag'mon", "kabob",
		"evos", "kfc", "bellissimo", "dostavka",
	}},
	{"oziq", "🛒", "Oziq-ovqat", []string{
		"bozor", "magazin", "market", "supermarket", "korzinka", "makro", "havas",
		"oziq", "go'sht", "sabzavot", "meva", "sut", "tuxum", "yog'", "non",
		"guruch", "shakar", "mahsulot", "produkt",
	}},
	{"transport", "🚕", "Transport", []string{
		"taksi", "taxi", "yandex", "mytaxi", "metro", "avtobus", "benzin", "metan",
		"propan", "zapravka", "parkovka", "moyka", "poyezd", "samolyot", "avia",
		"yo'lkira", "mashina", "avto", "shina",
	}},
	{"texnika", "💻", "Texnika", []string{
		"noutbuk", "kompyuter", "texnika", "naushnik", "quloqchin", "zaryadka",
		"kabel", "planshet", "monitor", "klaviatura", "sichqoncha",
	}},
	{"uy", "🏠", "Uy", []string{
		"ijara", "kvartira", "kommunal", "svet", "elektr", "gaz", "uy", "mebel",
		"remont", "xo'jalik",
	}},
	{"aloqa", "📱", "Aloqa va obuna", []string{
		"telefon", "paynet", "internet", "uzmobile", "beeline", "ucell", "mobiuz",
		"obuna", "spotify", "youtube", "netflix", "icloud", "chatgpt", "claude",
		"apple", "google",
	}},
	{"kiyim", "👕", "Kiyim", []string{
		"kiyim", "ko'ylak", "shim", "poyabzal", "krossovka", "kurtka", "futbolka",
		"kostyum", "sumka", "oyoqkiyim",
	}},
	{"salomatlik", "💊", "Salomatlik", []string{
		"dori", "apteka", "dorixona", "shifokor", "doktor", "vrach", "klinika",
		"analiz", "stomatolog", "tish", "sport", "zal", "fitnes", "gym",
	}},
	{"talim", "📚", "Ta'lim", []string{
		"kitob", "kurs", "dars", "kontrakt", "repetitor", "udemy",
	}},
	{"dam", "🎬", "Dam olish", []string{
		"kino", "o'yin", "playstation", "konsert", "sayohat", "sayr", "bilet",
		"mehmonxona", "hotel",
	}},
	{"marosim", "🎉", "Marosim va sovg'a", []string{
		"to'y", "tug'ilgan", "sovg'a", "gul", "marosim", "ehson", "sadaqa",
		"hadya", "nikoh", "xatna",
	}},
	{"ish", "💼", "Ish", []string{
		"server", "hosting", "domen", "vps", "reklama", "figma", "github", "cursor",
	}},
	// Never guessed from words: every debt entry gets it.
	{"qarz", "🤝", "Qarz", nil},
	{"boshqa", "📦", "Boshqa", nil},
}

// Where income comes from. A separate list, so a salary is never filed under
// "Ovqat" and a lunch never under "Oylik"; the parser guesses from this list
// only for income, and from Categories only for everything else.
var IncomeCategories = []Category{
	{"oylik", "💵", "Oylik", []string{"maosh", "oylik", "avans", "bonus", "premiya", "zarplata"}},
	{"qoshimcha", "🧑‍💻", "Qo'shimcha ish", []string{
		"freelance", "frilans", "loyiha", "zakaz", "buyurtma", "upwork", "dars",
	}},
	{"sovga_in", "🎁", "Sovg'a, yordam", []string{"sovg'a", "hadya", "yordam"}},
	{"kirim_boshqa", "💰", "Boshqa kirim", nil},
}

// DefaultCategory is where an entry of this kind goes when nothing better
// is known.
func DefaultCategory(kind string) string {
	switch {
	case IsDebt(kind):
		return "qarz"
	case IsIncome(kind):
		return "kirim_boshqa"
	}
	return "boshqa"
}

// CategoryByKey returns the category, or "boshqa" for an unknown key.
func CategoryByKey(key string) Category {
	for _, list := range [][]Category{Categories, IncomeCategories} {
		for _, c := range list {
			if c.Key == key {
				return c
			}
		}
	}
	return Categories[len(Categories)-1]
}

// IsCategory reports whether key is one of the categories above.
func IsCategory(key string) bool {
	return CategoryFits(KindExpense, key) || CategoryFits(KindIncome, key) || key == "qarz"
}

// CategoryFits reports whether a category belongs with a kind: debt rows in
// "qarz", income in an income category, everything else in a spending one.
func CategoryFits(kind, key string) bool {
	if IsDebt(kind) {
		return key == "qarz"
	}
	list := Categories
	if IsIncome(kind) {
		list = IncomeCategories
	}
	for _, c := range list {
		if c.Key == key && c.Key != "qarz" {
			return true
		}
	}
	return false
}

// CategoriesFor lists what may be chosen for a kind.
func CategoriesFor(kind string) []Category {
	if IsDebt(kind) {
		return []Category{CategoryByKey("qarz")}
	}
	if IsIncome(kind) {
		return IncomeCategories
	}
	out := make([]Category, 0, len(Categories))
	for _, c := range Categories {
		if c.Key != "qarz" {
			out = append(out, c)
		}
	}
	return out
}

// IsCurrency: the two currencies entries may be in.
func IsCurrency(c string) bool { return c == "UZS" || c == "USD" }

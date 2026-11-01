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
		"choy", "osh", "somsa", "lavash", "burger", "shashlik",
		"fastfud", "fastfood", "pitsa", "pizza", "shirinlik", "tort",
		"hotdog", "donar", "shaurma", "manti", "chuchvara", "lag'mon", "kabob",
		"evos", "kfc", "bellissimo", "dostavka",
		// As people actually write it: Russian meal words, and dishes.
		"zavtrak", "obed", "ujen", "ujin", "uzhin", "stolovaya", "tabaka",
		"baliq", "sushi", "rollar", "steyk", "kotlet", "dimlama", "norin", "qozon",
	}},
	// Bought on the way, not a meal: water from a shop, a cola, an energy
	// drink, a coffee to go, crisps. Kept apart from "Ovqat" so a month of
	// small bottles shows as one number instead of hiding in lunches or
	// "Boshqa".
	{"ichimlik", "🥤", "Ichimlik va gazak", []string{
		"suv", "kola", "cola", "coca", "pepsi", "fanta", "sprite", "mirinda",
		"limonad", "gazli", "sok", "kompot", "energetik", "energy", "redbull",
		"wedrink", "adrenalin", "gorilla", "kofe", "coffee", "kapuchino",
		"latte", "amerikano", "chips", "semechka", "pista", "keshu", "yong'oq",
		"shokolad", "shokalad", "snickers", "konfet", "pechenye", "saqich",
		"jvachka", "muzqaymoq", "marojni", "morojni", "gazak",
	}},
	{"oziq", "🛒", "Oziq-ovqat", []string{
		"bozor", "magazin", "market", "supermarket", "korzinka", "makro", "havas",
		"oziq", "go'sht", "sabzavot", "meva", "sut", "tuxum", "yog'", "non",
		"guruch", "shakar", "mahsulot", "produkt", "kartoshka", "sosiska",
		"kolbasa", "pishloq", "qatiq",
	}},
	{"transport", "🚕", "Transport", []string{
		"taksi", "taxi", "yandex", "mytaxi", "metro", "avtobus", "benzin", "metan",
		"propan", "zapravka", "parkovka", "moyka", "poyezd", "samolyot", "avia",
		"yo'lkira", "mashina", "avto", "shina", "marshrutka", "dizel",
		"solyarka", "yoqilg'i", "quydim", "quydirdim",
	}},
	{"texnika", "💻", "Texnika", []string{
		"noutbuk", "kompyuter", "texnika", "naushnik", "quloqchin", "zaryadka",
		"kabel", "planshet", "monitor", "klaviatura", "sichqoncha", "fleshka",
		"flashka", "rozetka",
	}},
	{"uy", "🏠", "Uy va jihozlar", []string{
		"ijara", "kvartira", "kommunal", "svet", "elektr", "gaz", "uy", "mebel",
		"remont", "xo'jalik", "kreslo", "stol", "stul", "divan", "shkaf",
		"javon", "polka", "podstavka", "lampa", "chiroq", "gilam", "parda",
		"idish", "choyshab", "yostiq", "ko'rpa", "matras",
	}},
	// The family home in another town: its gas and electricity are paid
	// from here, and should not be added to the flat one actually lives in.
	// No stems of its own: a message lands here when it names that home
	// (FamilyHomeStems) and would otherwise have gone to "uy" or nowhere,
	// so "Jizzax gaz 250k" is here but "Jizzaxga taksi 150k" is transport.
	{"oila", "🏡", "Oila uyi", nil},
	{"aloqa", "📱", "Aloqa va obuna", []string{
		"telefon", "paynet", "internet", "uzmobile", "beeline", "ucell", "mobiuz",
		"obuna", "spotify", "youtube", "netflix", "icloud", "chatgpt", "claude",
		"apple", "google", "telegram", "stars", "premium",
	}},
	{"kiyim", "👕", "Kiyim", []string{
		"kiyim", "ko'ylak", "shim", "poyabzal", "krossovka", "kurtka", "futbolka",
		"kostyum", "sumka", "oyoqkiyim",
	}},
	{"salomatlik", "💊", "Salomatlik", []string{
		"dori", "apteka", "dorixona", "shifokor", "doktor", "vrach", "klinika",
		"analiz", "stomatolog", "tish", "sport", "zal", "fitnes", "gym",
	}},
	// Things for oneself that are neither health nor clothes: a haircut,
	// shampoo, perfume, cigarettes. The label stays general on purpose.
	{"shaxsiy", "🧴", "Shaxsiy", []string{
		"soch", "sartarosh", "barber", "shampun", "sovun", "atir", "parfyum",
		"dezodorant", "krem", "britva", "kosmetika", "manikyur", "gigiena",
		"salfetka", "sigaret", "sigareta", "iqos", "vape", "zajigalka",
	}},
	{"talim", "📚", "Ta'lim", []string{
		"kitob", "kurs", "dars", "kontrakt", "repetitor", "udemy",
	}},
	{"dam", "🎬", "Dam olish", []string{
		"kino", "o'yin", "playstation", "konsert", "sayohat", "sayr", "bilet",
		"mehmonxona", "hotel", "dacha", "sauna", "bouling", "bilyard",
	}},
	{"marosim", "🎉", "Marosim va sovg'a", []string{
		"to'y", "tug'ilgan", "tugilgan", "sovg'a", "sovga", "gul", "marosim",
		"ehson", "sadaqa", "hadya", "nikoh", "xatna",
	}},
	{"ish", "💼", "Ish", []string{
		"server", "hosting", "domen", "vps", "reklama", "figma", "github", "cursor",
	}},
	// Never guessed from words: every debt entry gets it.
	{"qarz", "🤝", "Qarz", nil},
	{"boshqa", "📦", "Boshqa", nil},
}

// Words that point at the family home rather than the flat: the town, "at
// home" ("uydagi"), the family. See the "oila" category.
var FamilyHomeStems = []string{
	"jizzax", "uydagi", "uydagilar", "oila", "oilam", "ota-ona", "qishloq",
}

// Where income comes from. A separate list, so a salary is never filed under
// "Ovqat" and a lunch never under "Oylik"; the parser guesses from this list
// only for income, and from Categories only for everything else.
var IncomeCategories = []Category{
	{"oylik", "💵", "Oylik", []string{"maosh", "oylik", "avans", "bonus", "premiya", "zarplata"}},
	{"qoshimcha", "🧑‍💻", "Qo'shimcha ish", []string{
		"freelance", "frilans", "loyiha", "zakaz", "buyurtma", "upwork", "dars",
	}},
	{"sovga_in", "🎁", "Sovg'a, yordam", []string{"sovg'a", "sovga", "hadya", "yordam"}},
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

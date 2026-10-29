package financetext

import (
	"errors"
	"testing"
	"time"
)

func TestParseEntry(t *testing.T) {
	today := time.Date(2026, 10, 13, 0, 0, 0, 0, Tashkent)
	yesterday := today.AddDate(0, 0, -1)

	tests := []struct {
		in       string
		kind     string
		amount   int64 // minor units
		currency string
		category string
		person   string
		day      time.Time
	}{
		// Everyday spending.
		{"tushlik 35k", kindExpense, 35_000_00, "UZS", "ovqat", "", today},
		{"taksi 18000", kindExpense, 18_000_00, "UZS", "transport", "", today},
		{"taksiga 18 000", kindExpense, 18_000_00, "UZS", "transport", "", today},
		{"kecha taksi 18000", kindExpense, 18_000_00, "UZS", "transport", "", yesterday},
		{"35 ming tushlik", kindExpense, 35_000_00, "UZS", "ovqat", "", today},
		{"tushlik 35", kindExpense, 35_000_00, "UZS", "ovqat", "", today},
		{"1.5 mln telefon", kindExpense, 1_500_000_00, "UZS", "aloqa", "", today},
		{"1,5mln telefonga", kindExpense, 1_500_000_00, "UZS", "aloqa", "", today},
		{"korzinka 245.000", kindExpense, 245_000_00, "UZS", "oziq", "", today},
		{"Korzinkaga 1 250 000", kindExpense, 1_250_000_00, "UZS", "oziq", "", today},
		{"2 ta non 8000", kindExpense, 8_000_00, "UZS", "oziq", "", today},
		{"parkovka 500 so'm", kindExpense, 500_00, "UZS", "transport", "", today},
		{"ijaraga 3mln berdim", kindExpense, 3_000_000_00, "UZS", "uy", "", today},
		{"vps $12.50", kindExpense, 12_50, "USD", "ish", "", today},
		{"krossovka 80$", kindExpense, 80_00, "USD", "kiyim", "", today},
		{"nimadir 20k", kindExpense, 20_000_00, "UZS", "boshqa", "", today},
		{"05.10 dori 60k", kindExpense, 60_000_00, "UZS", "salomatlik", "", time.Date(2026, 10, 5, 0, 0, 0, 0, Tashkent)},
		{"o'tgan kuni kino 90k", kindExpense, 90_000_00, "UZS", "dam", "", today.AddDate(0, 0, -2)},
		{"choyxonaʼda osh 45k", kindExpense, 45_000_00, "UZS", "ovqat", "", today},

		// Given away.
		{"Sardorga 100k berdim to'yga", kindGift, 100_000_00, "UZS", "marosim", "Sardor", today},
		{"onamga 500k berdim", kindGift, 500_000_00, "UZS", "boshqa", "Onam", today},
		{"Aliga 50k", kindGift, 50_000_00, "UZS", "boshqa", "Ali", today},
		{"sadaqa 20k", kindGift, 20_000_00, "UZS", "marosim", "", today},

		// Money in.
		{"+10mln oylik", kindIncome, 10_000_000_00, "UZS", "oylik", "", today},
		{"oylik 10000000", kindIncome, 10_000_000_00, "UZS", "oylik", "", today},
		{"maosh tushdi 12 mln", kindIncome, 12_000_000_00, "UZS", "oylik", "", today},
		{"+500$ freelance", kindIncome, 500_00, "USD", "qoshimcha", "", today},
		{"onamdan +300k sovg'a", kindIncome, 300_000_00, "UZS", "sovga_in", "Onam", today},
		{"+ 2mln", kindIncome, 2_000_000_00, "UZS", "kirim_boshqa", "", today},
		{"kecha +150k", kindIncome, 150_000_00, "UZS", "kirim_boshqa", "", yesterday},
		// A monthly bill is not a salary.
		{"internet oylik 100k", kindExpense, 100_000_00, "UZS", "aloqa", "", today},
		{"obuna 1 oylik 50k", kindExpense, 50_000_00, "UZS", "aloqa", "", today},

		// Loans.
		{"Aliga qarz 500k", kindLent, 500_000_00, "UZS", "qarz", "Ali", today},
		{"aliga 500 ming qarz berdim", kindLent, 500_000_00, "UZS", "qarz", "Ali", today},
		{"Ali akaga qarz 2 mln", kindLent, 2_000_000_00, "UZS", "qarz", "Ali aka", today},
		{"Ulug'bekka $300 qarzga berdim", kindLent, 300_00, "USD", "qarz", "Ulug'bek", today},
		{"Alidan qarz oldim 200k", kindBorrowed, 200_000_00, "UZS", "qarz", "Ali", today},
		{"Sardordan 1 mln qarz", kindBorrowed, 1_000_000_00, "UZS", "qarz", "Sardor", today},
		{"Ali qaytardi +200k", kindRepaidIn, 200_000_00, "UZS", "qarz", "Ali", today},
		{"Ali qaytardi 200k", kindRepaidIn, 200_000_00, "UZS", "qarz", "Ali", today},
		{"Ali aka 200k qaytarib berdi", kindRepaidIn, 200_000_00, "UZS", "qarz", "Ali aka", today},
		{"Alidan 200k qaytdi", kindRepaidIn, 200_000_00, "UZS", "qarz", "Ali", today},
		{"Sardorga 300k qaytardim", kindRepaidOut, 300_000_00, "UZS", "qarz", "Sardor", today},
		{"Sardorga qarzimni to'ladim 300k", kindRepaidOut, 300_000_00, "UZS", "qarz", "Sardor", today},
	}

	for _, tc := range tests {
		got, err := Parse(tc.in, today)
		if err != nil {
			t.Errorf("%q: unexpected error %v", tc.in, err)
			continue
		}
		if got.Kind != tc.kind || got.AmountMinor != tc.amount || got.Currency != tc.currency ||
			got.Category != tc.category || got.Person != tc.person || !got.SpentOn.Equal(tc.day) {
			t.Errorf("%q:\n got  kind=%s amount=%d %s cat=%s person=%q day=%s\n want kind=%s amount=%d %s cat=%s person=%q day=%s",
				tc.in,
				got.Kind, got.AmountMinor, got.Currency, got.Category, got.Person, got.SpentOn.Format("02.01"),
				tc.kind, tc.amount, tc.currency, tc.category, tc.person, tc.day.Format("02.01"))
		}
	}
}

func TestParseEntryErrors(t *testing.T) {
	today := time.Date(2026, 10, 13, 0, 0, 0, 0, Tashkent)

	tests := []struct {
		in   string
		want error
	}{
		{"salom", ErrNoAmount},
		{"tushlik", ErrNoAmount},
		{"", ErrNoAmount},
		{"qarz berdim 500k", ErrNoPerson},
		{"qaytardi 200k", ErrNoPerson},
	}
	for _, tc := range tests {
		if _, err := Parse(tc.in, today); !errors.Is(err, tc.want) {
			t.Errorf("%q: err = %v, want %v", tc.in, err, tc.want)
		}
	}
}

// The note is the message minus the amount and the date: what shows in lists.
func TestParseEntryNote(t *testing.T) {
	today := time.Date(2026, 10, 13, 0, 0, 0, 0, Tashkent)
	got, err := Parse("kecha Sardorga 100k berdim to’yga", today)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "Sardorga berdim to'yga" {
		t.Errorf("note = %q", got.Note)
	}
}

// A date in the future without a year is last year's: "28.12" written in
// January is the December just gone.
func TestParseDayRollsBack(t *testing.T) {
	jan := time.Date(2027, 1, 5, 0, 0, 0, 0, Tashkent)
	d, ok := ParseDay("28.12", jan)
	if !ok || d.Year() != 2026 || d.Month() != 12 || d.Day() != 28 {
		t.Errorf("ParseDay(28.12) in January = %s, %v", d, ok)
	}
	if _, ok := ParseDay("31.02", jan); ok {
		t.Error("31.02 accepted")
	}
	if d, ok := ParseDay("kecha", jan); !ok || d.Day() != 4 {
		t.Errorf("kecha = %s, %v", d, ok)
	}
}

func TestParseAmount(t *testing.T) {
	tests := []struct {
		in       string
		minor    int64
		currency string
		ok       bool
	}{
		{"45000", 45_000_00, "UZS", true},
		{"45k", 45_000_00, "UZS", true},
		{"$20", 20_00, "USD", true},
		{"20 dollar", 20_00, "USD", true},
		{"0", 0, "", false},
		{"abc", 0, "", false},
	}
	for _, tc := range tests {
		minor, cur, ok := ParseAmount(tc.in)
		if ok != tc.ok || minor != tc.minor || (ok && cur != tc.currency) {
			t.Errorf("ParseAmount(%q) = %d %s %v", tc.in, minor, cur, ok)
		}
	}
}

func TestFormatMoney(t *testing.T) {
	tests := []struct {
		minor    int64
		currency string
		want     string
	}{
		{35_000_00, "UZS", "35 000 so'm"},
		{1_250_000_00, "UZS", "1 250 000 so'm"},
		{500_00, "UZS", "500 so'm"},
		{12_50, "USD", "$12.50"},
		{1_200_00, "USD", "$1 200"},
		{-300_000_00, "UZS", "-300 000 so'm"},
	}
	for _, tc := range tests {
		if got := FormatMoney(tc.minor, tc.currency); got != tc.want {
			t.Errorf("FormatMoney(%d, %s) = %q, want %q", tc.minor, tc.currency, got, tc.want)
		}
	}

	m := Money{}
	if m.String() != "0 so'm" {
		t.Errorf("empty Money = %q", m.String())
	}
	m.Add("USD", 300_00)
	m.Add("UZS", 1_000_000_00)
	if m.String() != "1 000 000 so'm + $300" {
		t.Errorf("mixed Money = %q", m.String())
	}
}

// "Gulnora" must not become flowers, while "gulga" still is.
func TestCategoryStems(t *testing.T) {
	if CategoryOf("gulnoraga") != "" {
		t.Error("gulnoraga matched a category")
	}
	if CategoryOf("gulga") != "marosim" {
		t.Error("gulga did not match marosim")
	}
	if CategoryOf("korzinkadagi") != "oziq" {
		t.Error("long stem with any ending should match")
	}
}

func TestSplit(t *testing.T) {
	today := time.Date(2026, 10, 13, 0, 0, 0, 0, Tashkent)
	got := Split("tushlik 35k; taksi 20k\nkofe, pishiriq 45k\n\n+10mln oylik", today)
	want := []string{"tushlik 35k", "taksi 20k", "kofe, pishiriq 45k", "+10mln oylik"}
	if len(got) != len(want) {
		t.Fatalf("Split = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Split[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGuessCategoryFromNote(t *testing.T) {
	if got := GuessCategoryFromNote(kindExpense, "Korzinkadan non"); got != "oziq" {
		t.Errorf("expense = %q, want oziq", got)
	}
	if got := GuessCategoryFromNote(kindIncome, "oylik"); got != "oylik" {
		t.Errorf("income = %q, want oylik", got)
	}
	if got := GuessCategoryFromNote(kindLent, "tushlik"); got != "qarz" {
		t.Errorf("lent = %q, want qarz", got)
	}
}

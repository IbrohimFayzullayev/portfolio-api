package finance

import (
	"testing"
	"time"
)

var tash = time.FixedZone("UTC+5", 5*3600)

// at is a moment in Tashkent.
func at(m time.Month, d, hour int) time.Time {
	return time.Date(2026, m, d, hour, 0, 0, 0, tash)
}

func flow(kind string, minor int64, m time.Month, d int, typedDay, typedHour int) Flow {
	return Flow{"UZS", kind, minor, day(m, d), at(m, typedDay, typedHour)}
}

func TestCashEffect(t *testing.T) {
	cases := map[string]int64{
		KindIncome: 100, KindRepaidIn: 100, KindBorrowed: 100,
		KindExpense: -100, KindGift: -100, KindLent: -100, KindRepaidOut: -100,
		"nonsense": 0,
	}
	for kind, want := range cases {
		if got := CashEffect(kind, 100); got != want {
			t.Errorf("CashEffect(%s) = %d, want %d", kind, got, want)
		}
	}
}

func TestCovers(t *testing.T) {
	cp := Checkpoint{Currency: "UZS", Minor: 0, TakenAt: at(10, 13, 12)} // 13 Oct, noon

	tests := []struct {
		name string
		f    Flow
		want bool
	}{
		{"earlier day, typed earlier", flow(KindExpense, 1, 10, 12, 12, 20), true},
		// "kecha taksi" typed after the recount: the card already showed it.
		{"earlier day, typed later", flow(KindExpense, 1, 10, 12, 13, 15), true},
		{"same day, typed before", flow(KindExpense, 1, 10, 13, 13, 9), true},
		{"same day, typed after", flow(KindExpense, 1, 10, 13, 13, 14), false},
		{"later day", flow(KindExpense, 1, 10, 14, 14, 9), false},
	}
	for _, tc := range tests {
		if got := cp.Covers(tc.f, tash); got != tc.want {
			t.Errorf("%s: Covers = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSince(t *testing.T) {
	cp := Checkpoint{Currency: "UZS", Minor: 25_000_000_00, TakenAt: at(10, 1, 0)}
	flows := []Flow{
		flow(KindIncome, 10_000_000_00, 10, 5, 5, 10),
		flow(KindExpense, 35_000_00, 10, 6, 6, 13),
		flow(KindLent, 500_000_00, 10, 7, 7, 9),
		flow(KindRepaidIn, 200_000_00, 10, 9, 9, 9),
		flow(KindExpense, 99_000_00, 9, 30, 30, 9), // before the checkpoint
		{"USD", KindIncome, 500_00, day(10, 5), at(10, 5, 10)},
	}
	balance, in, out := Since(cp, flows, tash)
	if in != 10_200_000_00 || out != 535_000_00 {
		t.Errorf("in = %d, out = %d", in, out)
	}
	if balance != 34_665_000_00 {
		t.Errorf("balance = %d", balance)
	}
}

func TestSeries(t *testing.T) {
	cps := []Checkpoint{
		{Currency: "UZS", Minor: 1_000_00, TakenAt: at(10, 2, 0)},
		// A recount on the 4th, after the morning's spending was typed.
		{Currency: "UZS", Minor: 500_00, TakenAt: at(10, 4, 12)},
	}
	flows := []Flow{
		flow(KindExpense, 100_00, 10, 2, 2, 9),
		flow(KindIncome, 300_00, 10, 3, 3, 9),
		flow(KindExpense, 50_00, 10, 4, 4, 9),  // covered by the recount
		flow(KindExpense, 20_00, 10, 4, 4, 18), // after it
		flow(KindExpense, 10_00, 10, 5, 5, 9),
	}
	got := Series(cps, flows, "UZS", day(10, 1), day(10, 5), tash)
	want := []struct {
		known bool
		minor int64
	}{
		{false, 0},       // 1st: nothing counted yet
		{true, 900_00},   // 1000 - 100
		{true, 1_200_00}, // + 300
		{true, 480_00},   // recount 500, - 20 after it
		{true, 470_00},   // - 10
	}
	if len(got) != len(want) {
		t.Fatalf("got %d points", len(got))
	}
	for i, w := range want {
		if got[i].Known != w.known || got[i].Minor != w.minor {
			t.Errorf("day %d: %+v, want known=%v minor=%d", i+1, got[i], w.known, w.minor)
		}
	}
}

func TestCategoriesByKind(t *testing.T) {
	if !CategoryFits(KindIncome, "oylik") || CategoryFits(KindExpense, "oylik") {
		t.Error("oylik should fit income only")
	}
	if !CategoryFits(KindExpense, "ovqat") || CategoryFits(KindIncome, "ovqat") {
		t.Error("ovqat should fit spending only")
	}
	if !CategoryFits(KindLent, "qarz") || CategoryFits(KindExpense, "qarz") {
		t.Error("qarz is for debt rows only")
	}
	if DefaultCategory(KindIncome) != "kirim_boshqa" || DefaultCategory(KindBorrowed) != "qarz" {
		t.Error("defaults")
	}
	if CategoryByKey("oylik").Label != "Oylik" {
		t.Error("income categories must be found by key")
	}
}

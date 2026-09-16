package bot

import "testing"

// slugify used to drop Cyrillic entirely, which meant every Cyrillic title
// produced a dated fallback slug that had to be renamed by hand.

func TestSlugifyTransliteratesCyrillic(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Салом дунё", "salom-dunyo"},
		{"Ўзбекистон", "ozbekiston"},
		{"Қишлоқ хўжалиги", "qishloq-xojaligi"},
		{"Ғалаба", "galaba"},
		{"Хабарлар", "xabarlar"},
		{"Привет мир", "privet-mir"},
	}

	for _, tc := range tests {
		if got := slugify(tc.in); got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSlugifyKeepsLatinBehaviour(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Uch servisli stack", "uch-servisli-stack"},
		{"O‘zbekcha sarlavha", "ozbekcha-sarlavha"},
		{"  Bo'sh joylar  ", "bosh-joylar"},
		{"Go 1.26 + Docker!", "go-1-26-docker"},
		{"!!!", ""},
	}

	for _, tc := range tests {
		if got := slugify(tc.in); got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Every editable field needs a short key and every short key an intent:
// a gap in either direction is a button that silently does nothing.
func TestFieldKeysRoundTrip(t *testing.T) {
	for intent := range editableFields {
		key := shortKey(intent)
		if key == "" {
			t.Errorf("%s has no short key", intent)
			continue
		}
		if got := intentForKey(key); got != intent {
			t.Errorf("intentForKey(%q) = %q, want %q", key, got, intent)
		}
	}

	for _, row := range editButtonRows {
		for _, intent := range row {
			if _, ok := editableFields[intent]; !ok {
				t.Errorf("button row names unknown field %q", intent)
			}
		}
	}
}

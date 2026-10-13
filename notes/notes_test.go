package notes

import (
	"strings"
	"testing"
)

func TestNotes(t *testing.T) {
	list := List()
	if len(list) == 0 {
		t.Fatal("no notes embedded")
	}
	seen := map[string]bool{}
	for _, n := range list {
		if n.Body != "" {
			t.Errorf("%s: List must not carry bodies", n.Slug)
		}
		if n.Title == "" || n.Title == n.Slug {
			t.Errorf("%s: no '# ' title", n.Slug)
		}
		if seen[n.Slug] {
			t.Errorf("duplicate slug %s", n.Slug)
		}
		seen[n.Slug] = true

		full, ok := Get(n.Slug)
		if !ok || !strings.Contains(full.Body, "# ") {
			t.Errorf("%s: Get failed", n.Slug)
		}
		// The Mini App's deep link is "/tg?tab=notes&doc=<slug>": a slug must
		// survive a URL and a Telegram button untouched.
		if strings.ContainsAny(n.Slug, " /?&#") {
			t.Errorf("%s: slug is not URL-safe", n.Slug)
		}
	}
	if _, ok := Get("does-not-exist"); ok {
		t.Error("unknown slug found")
	}
}

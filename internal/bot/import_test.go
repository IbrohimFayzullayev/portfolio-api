package bot

import (
	"strings"
	"testing"
)

func TestParseFrontMatter(t *testing.T) {
	raw := strings.Join([]string{
		"---",
		"title: Uch servisli stack",
		`slug: "uch-servisli-stack"`,
		"locale: uz",
		"tags: [go, docker, deploy]",
		"draft: false",
		"---",
		"",
		"Birinchi xatboshi.",
		"",
		"Ikkinchi xatboshi.",
	}, "\n")

	meta, body := parseFrontMatter(raw)

	if meta["title"] != "Uch servisli stack" {
		t.Errorf("title = %q", meta["title"])
	}
	if meta["slug"] != "uch-servisli-stack" {
		t.Errorf("quotes not stripped: %q", meta["slug"])
	}
	if meta["tags"] != "go, docker, deploy" {
		t.Errorf("brackets not stripped: %q", meta["tags"])
	}
	if meta["draft"] != "false" {
		t.Errorf("draft = %q", meta["draft"])
	}
	if !strings.HasPrefix(body, "Birinchi") || strings.Contains(body, "---") {
		t.Errorf("body = %q", body)
	}
}

func TestParseFrontMatterWithoutAFence(t *testing.T) {
	meta, body := parseFrontMatter("# Sarlavha\n\nMatn.")
	if len(meta) != 0 {
		t.Errorf("invented metadata: %v", meta)
	}
	if !strings.HasPrefix(body, "# Sarlavha") {
		t.Errorf("body = %q", body)
	}
}

// An opening fence with no closing one must not swallow the article.
func TestParseFrontMatterUnclosedFence(t *testing.T) {
	raw := "---\ntitle: yarim\n\nMatn davom etadi."
	meta, body := parseFrontMatter(raw)

	if len(meta) != 0 {
		t.Errorf("parsed an unclosed fence: %v", meta)
	}
	if !strings.Contains(body, "Matn davom etadi") {
		t.Errorf("body lost: %q", body)
	}
}

func TestPostTitle(t *testing.T) {
	t.Run("front matter wins", func(t *testing.T) {
		title, body := postTitle(map[string]string{"title": "Sarlavha"}, "# Boshqa\n\nMatn", "x.md")
		if title != "Sarlavha" {
			t.Errorf("title = %q", title)
		}
		if !strings.HasPrefix(body, "# Boshqa") {
			t.Errorf("body was modified: %q", body)
		}
	})

	t.Run("heading is used and removed", func(t *testing.T) {
		title, body := postTitle(map[string]string{}, "# Sarlavha\n\nMatn.", "x.md")
		if title != "Sarlavha" {
			t.Errorf("title = %q", title)
		}
		if strings.Contains(body, "# Sarlavha") {
			t.Errorf("title repeated in body: %q", body)
		}
	})

	t.Run("file name is the last resort", func(t *testing.T) {
		title, _ := postTitle(map[string]string{}, "Matn.", "uch-servisli-stack.md")
		if title != "uch servisli stack" {
			t.Errorf("title = %q", title)
		}
	})
}

func TestSplitTags(t *testing.T) {
	got := splitTags(" go , docker ,, deploy ")
	want := []string{"go", "docker", "deploy"}

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
			break
		}
	}
	if len(splitTags("")) != 0 {
		t.Error("empty input produced tags")
	}
}

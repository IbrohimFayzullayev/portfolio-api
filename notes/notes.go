// Package notes holds personal reference documents — the IELTS sprint plan,
// the list of study resources, the bot's command guide — so they can be read
// from the Telegram Mini App and the dashboard instead of being hunted down in
// a chat or a folder.
//
// They are Markdown files in this directory, embedded into the binary. Editing
// one is an ordinary commit: the text is versioned with the code that refers
// to it, and a deploy is what publishes it.
//
// File names are "NN-slug.md": NN orders the list, slug is the address
// (/notes/reja). The title is the first "# " heading.
package notes

import (
	"embed"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

//go:embed *.md
var files embed.FS

// Note is one document.
type Note struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

var fileName = regexp.MustCompile(`^(\d+)-([a-z0-9-]+)\.md$`)

type entry struct {
	order int
	note  Note
}

func load() []entry {
	names, _ := fs.Glob(files, "*.md")
	var out []entry
	for _, name := range names {
		m := fileName.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		raw, err := files.ReadFile(name)
		if err != nil {
			continue
		}
		body := string(raw)
		title := m[2]
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "# ") {
				title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
				break
			}
		}
		order := 0
		for _, c := range m[1] {
			order = order*10 + int(c-'0')
		}
		out = append(out, entry{order: order, note: Note{Slug: m[2], Title: title, Body: body}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].order < out[j].order })
	return out
}

// List returns every note without its body, in file-name order.
func List() []Note {
	out := []Note{}
	for _, e := range load() {
		n := e.note
		n.Body = ""
		out = append(out, n)
	}
	return out
}

// Get returns one note with its body.
func Get(slug string) (Note, bool) {
	for _, e := range load() {
		if e.note.Slug == slug {
			return e.note, true
		}
	}
	return Note{}, false
}

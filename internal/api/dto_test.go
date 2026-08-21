package api

import (
	"testing"
	"time"
)

func TestPostInput_Validate(t *testing.T) {
	base := func() postInput {
		return postInput{Locale: "en", Slug: "hello-world", Title: "Hello", Date: "2026-01-02"}
	}

	tests := []struct {
		name    string
		mutate  func(*postInput)
		wantErr bool
	}{
		{"valid", func(*postInput) {}, false},
		{"valid uz locale", func(p *postInput) { p.Locale = "uz" }, false},
		{"empty date is allowed", func(p *postInput) { p.Date = "" }, false},
		{"bad locale", func(p *postInput) { p.Locale = "ru" }, true},
		{"empty locale", func(p *postInput) { p.Locale = "" }, true},
		{"uppercase slug", func(p *postInput) { p.Slug = "Hello-World" }, true},
		{"slug with spaces", func(p *postInput) { p.Slug = "hello world" }, true},
		{"slug with leading hyphen", func(p *postInput) { p.Slug = "-hello" }, true},
		{"empty slug", func(p *postInput) { p.Slug = "" }, true},
		{"empty title", func(p *postInput) { p.Title = "" }, true},
		{"whitespace title", func(p *postInput) { p.Title = "   " }, true},
		{"bad date format", func(p *postInput) { p.Date = "02-01-2026" }, true},
		{"impossible date", func(p *postInput) { p.Date = "2026-13-40" }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base()
			tt.mutate(&in)
			err := in.validate()
			if tt.wantErr && err == nil {
				t.Errorf("validate() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validate() = %v, want nil", err)
			}
		})
	}
}

func TestProjectInput_Validate(t *testing.T) {
	base := func() projectInput {
		return projectInput{Locale: "uz", Slug: "my-app", Title: "My App", Date: "2026-05-01"}
	}

	tests := []struct {
		name    string
		mutate  func(*projectInput)
		wantErr bool
	}{
		{"valid", func(*projectInput) {}, false},
		{"bad locale", func(p *projectInput) { p.Locale = "fr" }, true},
		{"bad slug", func(p *projectInput) { p.Slug = "My_App" }, true},
		{"empty title", func(p *projectInput) { p.Title = "" }, true},
		{"bad date", func(p *projectInput) { p.Date = "2026/05/01" }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base()
			tt.mutate(&in)
			err := in.validate()
			if tt.wantErr && err == nil {
				t.Errorf("validate() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validate() = %v, want nil", err)
			}
		})
	}
}

func TestPublishedAtFor(t *testing.T) {
	existing := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	// Draft content is never published.
	if got := publishedAtFor(true, nil); got != nil {
		t.Errorf("draft=true, existing=nil: got %v, want nil", got)
	}
	if got := publishedAtFor(true, &existing); got != nil {
		t.Errorf("draft=true, existing set: got %v, want nil", got)
	}

	// Publishing keeps an existing timestamp (does not reset it).
	if got := publishedAtFor(false, &existing); got == nil || !got.Equal(existing) {
		t.Errorf("draft=false, existing set: got %v, want %v", got, existing)
	}

	// Publishing fresh content stamps "now".
	before := time.Now().UTC()
	got := publishedAtFor(false, nil)
	if got == nil {
		t.Fatal("draft=false, existing=nil: got nil, want a timestamp")
	}
	if got.Before(before) {
		t.Errorf("published_at %v should be at or after %v", got, before)
	}
}

func TestParseContentDate(t *testing.T) {
	// A valid date is parsed exactly.
	got := parseContentDate("2026-03-15")
	want := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("parseContentDate(valid) = %v, want %v", got, want)
	}

	// Empty and invalid strings both fall back to today (UTC, midnight).
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, in := range []string{"", "not-a-date", "  "} {
		if got := parseContentDate(in); !got.Equal(today) {
			t.Errorf("parseContentDate(%q) = %v, want today %v", in, got, today)
		}
	}
}

func TestNonNil(t *testing.T) {
	if got := nonNil(nil); got == nil || len(got) != 0 {
		t.Errorf("nonNil(nil) = %v, want empty non-nil slice", got)
	}
	in := []string{"a", "b"}
	if got := nonNil(in); len(got) != 2 {
		t.Errorf("nonNil(%v) = %v, want unchanged", in, got)
	}
}

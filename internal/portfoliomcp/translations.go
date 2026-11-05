package portfoliomcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Linking the two language versions of one piece of content.
//
// A post and its translation are tied by translation_key, which the public
// site uses for hreflang and the language switch (migration 000003). The key
// is unique per locale in the database, so a link is checked here first:
// the API would answer a clash with a misleading "slug already exists".

var keyRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// item is what pairing needs from a post or a project.
type item struct {
	Type   string // post | project
	ID     string
	Locale string
	Slug   string
	Title  string
	Date   string
	Key    string
	Tags   []string
}

func postItems(ps []post) []item {
	out := make([]item, 0, len(ps))
	for _, p := range ps {
		out = append(out, item{"post", p.ID, p.Locale, p.Slug, p.Title, p.Date, p.TranslationKey, p.Tags})
	}
	return out
}

func projectItems(ps []project) []item {
	out := make([]item, 0, len(ps))
	for _, p := range ps {
		out = append(out, item{"project", p.ID, p.Locale, p.Slug, p.Title, p.Date, p.TranslationKey, p.Tags})
	}
	return out
}

// PairSuggestion is a likely uz/en pair that is not linked yet.
type PairSuggestion struct {
	Type    string   `json:"type"`
	UzID    string   `json:"uz_id"`
	UzSlug  string   `json:"uz_slug"`
	EnID    string   `json:"en_id"`
	EnSlug  string   `json:"en_slug"`
	Key     string   `json:"key"` // what link_translations would use
	Reasons []string `json:"reasons"`
	Score   int      `json:"score"`
}

// Words that say nothing about the topic. Short words are dropped anyway.
var pairStopWords = map[string]bool{
	"the": true, "and": true, "with": true, "for": true, "how": true, "your": true,
	"that": true, "without": true, "bilan": true, "va": true, "uchun": true, "qanday": true,
}

func topicWords(it item) map[string]bool {
	out := map[string]bool{}
	split := func(s string) []string {
		return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
		})
	}
	for _, w := range append(split(it.Slug), split(it.Title)...) {
		if len(w) >= 3 && !pairStopWords[w] {
			out[w] = true
		}
	}
	return out
}

// pairScore: how sure a uz and an en item are the same content. A shared
// slug decides it; a shared date is strong (both versions are written
// together) but not enough alone - two unrelated drafts made on one day are
// common - so it needs a shared word or tag too. Below minPairScore it is a
// guess.
const minPairScore = 35

func pairScore(uz, en item) (int, []string) {
	score, why := 0, []string{}
	if uz.Slug == en.Slug {
		score += 100
		why = append(why, "same_slug")
	}
	if uz.Date != "" && uz.Date == en.Date {
		score += 30
		why = append(why, "same_date")
	}
	uw, n := topicWords(uz), 0
	for w := range topicWords(en) {
		if uw[w] {
			n++
		}
	}
	if n > 0 {
		score += 10 * n
		why = append(why, fmt.Sprintf("%d_shared_words", n))
	}
	tags, t := map[string]bool{}, 0
	for _, x := range uz.Tags {
		tags[strings.ToLower(x)] = true
	}
	for _, x := range en.Tags {
		if tags[strings.ToLower(x)] {
			t++
		}
	}
	if t > 0 {
		score += 5 * t
		why = append(why, fmt.Sprintf("%d_shared_tags", t))
	}
	return score, why
}

// SuggestPairs finds unlinked uz/en items that look like one piece of
// content, best matches first, each item in at most one suggestion.
func SuggestPairs(items []item) []PairSuggestion {
	linked := map[string]map[string]bool{} // type|key -> locales
	for _, it := range items {
		if it.Key == "" {
			continue
		}
		k := it.Type + "|" + it.Key
		if linked[k] == nil {
			linked[k] = map[string]bool{}
		}
		linked[k][it.Locale] = true
	}
	alone := func(it item) bool { return it.Key == "" || len(linked[it.Type+"|"+it.Key]) < 2 }

	type cand struct {
		uz, en item
		score  int
		why    []string
	}
	var cands []cand
	for _, uz := range items {
		if uz.Locale != "uz" || !alone(uz) {
			continue
		}
		for _, en := range items {
			if en.Locale != "en" || en.Type != uz.Type || !alone(en) {
				continue
			}
			if s, why := pairScore(uz, en); s >= minPairScore {
				cands = append(cands, cand{uz, en, s, why})
			}
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })

	used := map[string]bool{}
	out := []PairSuggestion{}
	for _, c := range cands {
		if used[c.uz.ID] || used[c.en.ID] {
			continue
		}
		used[c.uz.ID], used[c.en.ID] = true, true
		out = append(out, PairSuggestion{
			Type: c.uz.Type, UzID: c.uz.ID, UzSlug: c.uz.Slug, EnID: c.en.ID, EnSlug: c.en.Slug,
			Key: pickKey(c.uz, c.en, items), Reasons: c.why, Score: c.score,
		})
	}
	return out
}

// pickKey: a key one side already has, if the other locale does not use it
// elsewhere; otherwise the English slug.
func pickKey(uz, en item, all []item) string {
	for _, k := range []string{en.Key, uz.Key} {
		if k != "" && clash(k, uz, en, all) == nil {
			return k
		}
	}
	return en.Slug
}

// clash returns another item of the same type that already holds key in the
// locale of uz or en.
func clash(key string, uz, en item, all []item) *item {
	for i, it := range all {
		if it.Type != uz.Type || it.Key != key || it.ID == uz.ID || it.ID == en.ID {
			continue
		}
		if it.Locale == uz.Locale || it.Locale == en.Locale {
			return &all[i]
		}
	}
	return nil
}

/* --------------------------------- the tool -------------------------------- */

func (t *tools) linkTranslations(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		Type string `json:"type"`
		UzID string `json:"uz_id"`
		EnID string `json:"en_id"`
		Key  string `json:"key"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	if in.Type != "post" && in.Type != "project" {
		return nil, fmt.Errorf("type must be post or project")
	}
	if in.Key != "" && !keyRe.MatchString(in.Key) {
		return nil, fmt.Errorf("key must be lowercase-words-with-hyphens")
	}
	for _, id := range []string{in.UzID, in.EnID} {
		if _, err := checkID(id); err != nil {
			return nil, err
		}
	}

	// Everything of this type: to find both items and to check for clashes.
	var all []item
	var posts map[string]post
	var projects map[string]project
	if in.Type == "post" {
		ps, err := t.posts(ctx, "", "")
		if err != nil {
			return nil, err
		}
		posts = map[string]post{}
		for _, p := range ps {
			posts[p.ID] = p
		}
		all = postItems(ps)
	} else {
		ps, err := t.projects(ctx, "")
		if err != nil {
			return nil, err
		}
		projects = map[string]project{}
		for _, p := range ps {
			projects[p.ID] = p
		}
		all = projectItems(ps)
	}
	find := func(id, locale string) (item, error) {
		for _, it := range all {
			if it.ID == id {
				if it.Locale != locale {
					return it, fmt.Errorf("%s %q is in %s, not %s", in.Type, it.Slug, it.Locale, locale)
				}
				return it, nil
			}
		}
		return item{}, fmt.Errorf("no %s with id %s", in.Type, id)
	}
	uz, err := find(in.UzID, "uz")
	if err != nil {
		return nil, err
	}
	en, err := find(in.EnID, "en")
	if err != nil {
		return nil, err
	}

	key := in.Key
	if key == "" {
		key = pickKey(uz, en, all)
	}
	if c := clash(key, uz, en, all); c != nil {
		return nil, fmt.Errorf("key %q is already used by the %s %s %q (%s); choose another key", key, c.Locale, c.Type, c.Slug, c.ID)
	}

	var (
		changed  []string
		writeErr error
	)
	for _, it := range []item{uz, en} {
		if it.Key == key {
			continue
		}
		if in.Type == "post" {
			p := posts[it.ID]
			next := p.input()
			next.TranslationKey = key
			if next.Tags == nil {
				next.Tags = []string{}
			}
			writeErr = t.c.Do(ctx, http.MethodPut, "/api/v1/posts/"+url.PathEscape(p.ID), nil, next, nil)
		} else {
			p := projects[it.ID]
			next := p.input()
			next.TranslationKey = key
			if next.Tags == nil {
				next.Tags = []string{}
			}
			if next.Stack == nil {
				next.Stack = []string{}
			}
			writeErr = t.c.Do(ctx, http.MethodPut, "/api/v1/projects/"+url.PathEscape(p.ID), nil, next, nil)
		}
		if writeErr != nil {
			writeErr = fmt.Errorf("%s %s: %w", it.Locale, it.Slug, writeErr)
			break
		}
		changed = append(changed, it.Locale+":"+it.Slug)
	}
	if len(changed) == 0 && writeErr == nil {
		// Already linked with this key: nothing written, nothing to log.
		return map[string]any{"type": in.Type, "key": key, "changed": []string{}, "unchanged": true}, nil
	}
	ch := change{
		Tool: "link_translations", TargetType: in.Type,
		Targets: []target{{uz.ID, label(uz.Locale, uz.Slug)}, {en.ID, label(en.Locale, en.Slug)}},
		Changes: map[string]any{"translation_key": map[string]any{
			"from": map[string]string{"uz": uz.Key, "en": en.Key}, "to": key,
		}},
	}
	if writeErr != nil && len(changed) > 0 {
		// Half done: say which side already carries the new key.
		ch.Changes["written_before_error"] = changed
	}
	return t.audited(ctx, ch, map[string]any{
		"type": in.Type, "key": key, "changed": nonNilStrings(changed),
		"linked": []map[string]string{
			{"locale": "uz", "id": uz.ID, "slug": uz.Slug},
			{"locale": "en", "id": en.ID, "slug": en.Slug},
		},
	}, writeErr)
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

package api

import (
	"context"
	"net/http"
	"testing"
)

// The growth, books and notes endpoints, read the way the Mini App reads them:
// with a viewer token, over rows shaped exactly like the bot writes.

func TestIntegration_GrowthBooksNotes(t *testing.T) {
	s, admin := setupFinance(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `
		TRUNCATE growth_logs, growth_recordings, growth_sprints, growth_scores, books, book_sessions CASCADE`); err != nil {
		t.Fatal(err)
	}

	today := todayTashkent()
	d := func(off int) string { return today.AddDate(0, 0, off).Format(dateLayout) }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	exec(`INSERT INTO growth_logs (day, kind, minutes) VALUES ($1::date,'writing',40), ($1::date,'speaking',10), ($2::date,'listening',30)`, d(0), d(-1))
	exec(`INSERT INTO growth_recordings (day, telegram_file_id, file_unique_id, duration_sec) VALUES ($1::date,'f','u',60)`, d(0))
	exec(`INSERT INTO growth_sprints (goal, starts_on, ends_on, closed_at, outcome) VALUES ('old one', $1::date, $2::date, now(), 'partial')`, d(-20), d(-15))
	exec(`INSERT INTO growth_sprints (goal, starts_on, ends_on) VALUES ('5 ta essay', $1::date, $2::date)`, d(-1), d(4))
	exec(`INSERT INTO growth_scores (taken_on, kind, overall, writing) VALUES ($1::date,'mock',5.5,5.0), ($2::date,'mock',6.0,NULL)`, d(-10), d(0))

	viewer := viewerToken(t, s, 42)

	rec := do(t, s, http.MethodGet, "/api/v1/growth/summary", viewer, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body.String())
	}
	var sum growthSummary
	decode(t, rec, &sum)
	if sum.Minutes != 80 || sum.ByKind["writing"] != 40 || len(sum.ByDay) != 28 || sum.Recordings != 1 {
		t.Errorf("summary totals: %+v", sum)
	}
	if len(sum.Sprints) != 2 || !sum.Sprints[0].Open || sum.Sprints[0].Minutes != 80 {
		t.Errorf("sprints: %+v", sum.Sprints)
	}
	if o := sum.Sprints[1].Outcome; o == nil || *o != "partial" {
		t.Errorf("closed sprint outcome: %v", o)
	}
	if len(sum.Scores) != 2 || sum.Scores[1].Overall != 6.0 || sum.Scores[0].Writing == nil {
		t.Errorf("scores: %+v", sum.Scores)
	}
	if rec := do(t, s, http.MethodGet, "/api/v1/growth/summary?from=2026-13-01", viewer, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad date: %d", rec.Code)
	}

	exec(`INSERT INTO books (title, author, total_pages, started_on) VALUES ('Ufq','Said Ahmad',400,$1::date)`, d(-5))
	exec(`INSERT INTO books (title, status, started_on, finished_on) VALUES ('Alkimyogar','finished',$1::date,$2::date)`, d(-30), d(-2))
	exec(`INSERT INTO book_sessions (book_id, day, minutes, page_reached)
	      SELECT id, $1::date, 45, 100 FROM books WHERE title = 'Ufq'`, d(-1))
	exec(`INSERT INTO book_sessions (book_id, day, minutes)
	      SELECT id, $1::date, 30 FROM books WHERE title = 'Ufq'`, d(0))

	rec = do(t, s, http.MethodGet, "/api/v1/growth/books", viewer, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("books: %d %s", rec.Code, rec.Body.String())
	}
	var books booksSummary
	decode(t, rec, &books)
	if len(books.Reading) != 1 || books.Reading[0].Minutes != 75 || books.Reading[0].Percent != 25 || books.Reading[0].Days != 2 {
		t.Errorf("reading: %+v", books.Reading)
	}
	if len(books.ByMonth) != 12 || len(books.Recent) != 30 || books.Recent[29].Minutes != 30 {
		t.Errorf("series: months %d, recent %d", len(books.ByMonth), len(books.Recent))
	}
	if rec := do(t, s, http.MethodGet, "/api/v1/growth/books?year=abc", viewer, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad year: %d", rec.Code)
	}

	rec = do(t, s, http.MethodGet, "/api/v1/notes", viewer, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("notes: %d", rec.Code)
	}
	if rec := do(t, s, http.MethodGet, "/api/v1/notes/reja", admin, nil); rec.Code != http.StatusOK {
		t.Errorf("note by admin: %d", rec.Code)
	}
	if rec := do(t, s, http.MethodGet, "/api/v1/notes/nope", viewer, nil); rec.Code != http.StatusNotFound {
		t.Errorf("missing note: %d", rec.Code)
	}
	// Without a token there is nothing to read.
	if rec := do(t, s, http.MethodGet, "/api/v1/growth/summary", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
}

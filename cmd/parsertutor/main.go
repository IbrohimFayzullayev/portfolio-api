// Command parsertutor runs the finance bot's parser tutor once: it reads the
// last weeks of entries and unreadable messages, lets the model propose
// catalog stems, proves them against every real line and writes a markdown
// report. It changes nothing: no database writes, no file in the repository.
//
//	FINBOT_TUTOR_DATABASE_URL=postgres://finbot_tutor:...@localhost:5432/finbot \
//	FINBOT_ADMIN_SEES_MONEY=true ANTHROPIC_API_KEY=... \
//	go run ./cmd/parsertutor -days 30 -out report.md
//
// -offline skips the model and writes only the state of the parser, which is
// free and needs no API key.
//
// See internal/tutor for the design and docs/parsertutor.md for setup.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ibrohimcoder/portfolio-api/internal/database"
	"github.com/ibrohimcoder/portfolio-api/internal/tutor"
)

const defaultModel = "claude-sonnet-5-5"

func main() {
	days := flag.Int("days", 30, "how many days of entries and failures to read")
	out := flag.String("out", "parsertutor-report.md", `where to write the report; "-" for stdout`)
	offline := flag.Bool("offline", false, "no model: report the parser's state only")
	maxTurns := flag.Int("max-turns", 12, "model round trips at most")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The same promise the bot makes its users (privacyLine in
	// internal/finbot/bot.go): while it says the owner cannot see their
	// records, no tool of the owner's reads them either.
	if sees, _ := strconv.ParseBool(os.Getenv("FINBOT_ADMIN_SEES_MONEY")); !sees {
		log.Fatal("parsertutor: FINBOT_ADMIN_SEES_MONEY is not true. Users are told the owner does not see their records, so this tool does not read them.")
	}
	dbURL := os.Getenv("FINBOT_TUTOR_DATABASE_URL")
	if dbURL == "" {
		log.Fatal("parsertutor: FINBOT_TUTOR_DATABASE_URL is not set (a read-only role, see docs/parsertutor.md)")
	}
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if !*offline && apiKey == "" {
		log.Fatal("parsertutor: ANTHROPIC_API_KEY is not set (or run with -offline)")
	}
	model := os.Getenv("TUTOR_MODEL")
	if model == "" {
		model = defaultModel
	}

	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	pool, err := database.Connect(cctx, dbURL)
	cancel()
	if err != nil {
		log.Fatalf("parsertutor: database: %v", err)
	}
	entries, failures, err := load(ctx, pool, *days)
	pool.Close()
	if err != nil {
		log.Fatalf("parsertutor: read: %v", err)
	}

	samples := tutor.BuildSamples(entries, failures)
	log.Printf("parsertutor: %d entries, %d failures; %s", len(entries), len(failures), tutor.Summary(samples))

	in := tutor.ReportInput{Samples: samples, Model: model, Days: *days, Now: time.Now(), Offline: *offline}
	if !*offline {
		agent := &tutor.Agent{LLM: &tutor.Anthropic{APIKey: apiKey}, Model: model, MaxTurns: *maxTurns}
		in.Final, err = agent.Run(ctx, samples)
		if err != nil {
			log.Fatalf("parsertutor: agent: %v (after %d turns, %d+%d tokens)",
				err, in.Final.Turns, in.Final.Usage.InputTokens, in.Final.Usage.OutputTokens)
		}
	}

	report := tutor.Report(in)
	if *out == "-" {
		// In a container the report is easiest to keep through stdout; the
		// log above goes to stderr and stays out of it.
		fmt.Print(report)
		return
	}
	if err := os.WriteFile(*out, []byte(report), 0o600); err != nil {
		log.Fatalf("parsertutor: write report: %v", err)
	}
	fmt.Printf("%s: %d proposals, %d test lines\n", *out, len(in.Final.Proposals), len(in.Final.Tests))
}

// load reads what the tutor learns from. Only bot entries: a row typed in
// the web app was never parsed, so it says nothing about the parser.
func load(ctx context.Context, pool *pgxpool.Pool, days int) ([]tutor.EntryRow, []tutor.FailureRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT raw_text, kind, category, person, amount_minor, currency, created_at, updated_at
		FROM entries
		WHERE source = 'bot' AND raw_text <> ''
		  AND created_at > now() - make_interval(days => $1::int)`, days)
	if err != nil {
		return nil, nil, err
	}
	var entries []tutor.EntryRow
	for rows.Next() {
		var e tutor.EntryRow
		if err := rows.Scan(&e.RawText, &e.Kind, &e.Category, &e.Person, &e.AmountMinor,
			&e.Currency, &e.CreatedAt, &e.UpdatedAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	rows, err = pool.Query(ctx, `
		SELECT text, reason, created_at FROM parse_failures
		WHERE created_at > now() - make_interval(days => $1::int)`, days)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var failures []tutor.FailureRow
	for rows.Next() {
		var f tutor.FailureRow
		if err := rows.Scan(&f.Text, &f.Reason, &f.CreatedAt); err != nil {
			return nil, nil, err
		}
		failures = append(failures, f)
	}
	return entries, failures, rows.Err()
}

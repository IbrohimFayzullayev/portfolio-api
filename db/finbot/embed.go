// Package finbotmigrations embeds the finance bot's own schema. The finance
// bot keeps its data in a database of its own (FINBOT_DATABASE_URL), apart
// from the portfolio's, and applies these on every start the same way the
// API applies db/migrations.
package finbotmigrations

import "embed"

//go:embed migrations/*.sql
var FS embed.FS

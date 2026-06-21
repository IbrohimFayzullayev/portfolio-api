// Package migrations embeds the SQL migration files so they can be applied
// automatically at application startup (no external migrate CLI required).
package migrations

import "embed"

//go:embed migrations/*.sql
var FS embed.FS

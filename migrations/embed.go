// Package migrations embeds the ordered database schema migrations.
package migrations

import "embed"

// FS holds the SQL migration files.
//
//go:embed *.sql
var FS embed.FS

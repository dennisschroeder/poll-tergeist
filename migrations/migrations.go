// Package migrations embeds the SQL schema files so the server ships as one
// binary with no separate migration step. Each file is idempotent
// (CREATE TABLE IF NOT EXISTS) and applied in filename order at startup.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

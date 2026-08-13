// Package migrations embeds the SQL schema files so the server ships as one
// binary with no separate migration step or version tracking: every file is
// reapplied, in filename order, on every startup. That means every file
// must be idempotent on its own — 0001 does this via CREATE TABLE IF NOT
// EXISTS, later files that alter an already-created schema do it via
// guarded ALTER TABLE statements (see 0002 for the pattern).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

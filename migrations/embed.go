package migrations

import "embed"

// Files contains all ordered SQLite migrations.
//
//go:embed *.sql
var Files embed.FS

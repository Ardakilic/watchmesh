// Package migrations embeds versioned SQL so the binary migrates via iofs
// with no external files (dev override: --migrations-path <directory>).
package migrations

import "embed"

// FS holds migrations/*.sql for the golang-migrate iofs source.
//
//go:embed *.sql
var FS embed.FS

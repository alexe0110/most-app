// Package migrations embeds the goose SQL migration files so they can be applied
// at startup and from integration tests without depending on the working
// directory. The SQL itself is the source of truth; this file only exposes it.
package migrations

import "embed"

// FS holds every migration file in this directory.
//
//go:embed *.sql
var FS embed.FS

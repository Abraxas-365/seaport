// Package migrations embeds the SQL migrations (goose).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

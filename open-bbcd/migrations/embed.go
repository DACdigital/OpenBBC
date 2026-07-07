package migrations

import "embed"

//go:embed [0-9]*.sql
var FS embed.FS

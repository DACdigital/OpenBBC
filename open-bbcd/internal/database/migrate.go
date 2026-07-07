package database

import (
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"

	"github.com/DACdigital/OpenBBC/open-bbcd/migrations"
)

// Migrate applies all pending migrations from the embedded FS. Idempotent —
// goose tracks state in the `goose_db_version` table.
//
// Single-instance only: this uses goose's package-level API which does NOT
// take an advisory lock. Concurrent boots against the same DB will race on
// first-time migrations (loser sees "relation already exists" and crash-loops
// until the winner finishes). Safe for `docker compose up` (one replica).
// If we later deploy multi-replica via helm, switch to goose.NewProvider(...)
// with WithSessionLocker(lock.NewPostgresSessionLocker()).
func Migrate(db *sql.DB) error {
	goose.SetLogger(goose.NopLogger())
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

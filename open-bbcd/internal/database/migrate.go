package database

import (
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"

	"github.com/DACdigital/OpenBBC/open-bbcd/migrations"
)

// Migrate applies all pending migrations from the embedded FS. Idempotent —
// goose tracks state in the `goose_db_version` table. Safe under concurrent
// callers thanks to goose's advisory lock.
func Migrate(db *sql.DB) error {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

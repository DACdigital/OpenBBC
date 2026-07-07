package database

import (
	"os"
	"testing"
)

func TestMigrate_AppliesFromScratch(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := NewPostgres(url)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	defer db.Close()

	// Fresh baseline: wipe the entire public schema so Migrate runs Up from
	// zero. Just dropping goose_db_version isn't enough because the underlying
	// tables from prior migration runs would still exist and CREATE TABLE
	// would fail. Dropping the schema is the standard Postgres reset.
	if _, err := db.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset public schema: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// The `agents` table exists after migration 001 — sanity check.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name='agents'`).Scan(&n); err != nil {
		t.Fatalf("verify agents table: %v", err)
	}
	if n != 1 {
		t.Fatalf("agents table not created after Migrate")
	}
}

func TestMigrate_IdempotentSecondRun(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := NewPostgres(url)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	defer db.Close()

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate first: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate second (should be no-op): %v", err)
	}
}

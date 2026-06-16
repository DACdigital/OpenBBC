package repository

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

// withRepo returns an AgentRepository against the test database
// (DATABASE_URL). If DATABASE_URL is unset, the test is skipped — keeps
// CI environments without Postgres green.
//
// Tests using this helper must apply migrations beforehand (`make migrate-up`)
// or run against an already-migrated dev DB. The helper does NOT manage
// migrations.
//
// Each call hands back a fresh schema-scoped tablespace by truncating the
// tables it touches at the start of the test — minimizing cross-test
// interference without dropping migrations.
func withRepo(t *testing.T) (*AgentRepository, *AgentVersionRepository, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Truncate in dependency order. agents is parent for many other tables;
	// CASCADE handles the rest. Tables created in migration 011 are included.
	if _, err := db.Exec(`TRUNCATE deployed_messages, deployed_sessions, chat_messages, chat_sessions, resources, agent_versions, agents RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return NewAgentRepository(db), NewAgentVersionRepository(db), db
}

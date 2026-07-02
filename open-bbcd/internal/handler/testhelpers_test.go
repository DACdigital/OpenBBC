package handler

import (
	"database/sql"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"testing"

	_ "github.com/lib/pq"

	"github.com/DACdigital/OpenBBC/open-bbcd/web"
)

// testLogger returns a slog.Logger that discards output, suitable for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// openTestDBForHandlers opens a Postgres connection and truncates every table
// the handler-integration tests touch. Skips if DATABASE_URL is unset.
//
// This is the eval-era superset of openHandlerTestDB / openConfiguratorTestDB;
// use it for new handler tests that need the eval + dataset + chat schema.
func openTestDBForHandlers(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping handler integration test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`TRUNCATE
		eval_sessions, evals,
		chat_message_feedback,
		dataset_version_sessions, dataset_versions, datasets,
		deployed_messages, deployed_sessions, chat_messages, chat_sessions,
		resources, agent_versions, agents,
		tool_backends, agent_endpoint_backend, agent_version_mcp_backend
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return db
}

// testWebFS returns the embedded web assets FS, for handlers that need to
// parse templates.
func testWebFS() fs.FS { return web.Assets }

// unwrapDB is a test-only accessor for the *sql.DB embedded via
// evalStoreAdapter, so tests can seed extra rows through the same connection
// used by the handler under test.
func unwrapDB(h *EvalHandler) *sql.DB {
	if a, ok := h.adapter.(*evalStoreAdapter); ok {
		return a.db
	}
	return nil
}

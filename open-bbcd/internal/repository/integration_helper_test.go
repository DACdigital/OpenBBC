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
	if _, err := db.Exec(`TRUNCATE
		deployed_messages, deployed_sessions, chat_messages, chat_sessions,
		resources, agent_versions, agents,
		tool_backends, agent_version_endpoint_backend, agent_version_mcp_backend
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return NewAgentRepository(db), NewAgentVersionRepository(db), db
}

// openTestDB connects to the test DB and truncates all migration-tracked tables.
// Use this in tests that need a fresh DB but don't want the existing agent/version repos.
func openTestDB(t *testing.T) *sql.DB {
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

	// Truncate in dependency order. Add tool_backends so subsequent tests
	// start from a clean slate.
	if _, err := db.Exec(`TRUNCATE
		deployed_messages, deployed_sessions, chat_messages, chat_sessions,
		resources, agent_versions, agents,
		tool_backends, agent_version_endpoint_backend, agent_version_mcp_backend
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return db
}

// seedAgent creates a minimal agents + agent_versions pair and returns the
// (agent_id, version_id) tuple. Use this when the test needs both ids
// (e.g. endpoint wiring uses agent_id, MCP attachment uses version_id).
func seedAgent(t *testing.T, db *sql.DB) (string, string) {
	t.Helper()
	var agentID, versionID string
	err := db.QueryRow(`
		WITH a AS (
			INSERT INTO agents (name) VALUES ('test-' || gen_random_uuid())
			RETURNING id
		), v AS (
			INSERT INTO agent_versions (agent_id, status, flow_map_config)
			SELECT id, 'INITIALIZING', '{}'::jsonb FROM a
			RETURNING id, agent_id
		)
		SELECT v.agent_id::text, v.id::text FROM v
	`).Scan(&agentID, &versionID)
	if err != nil {
		t.Fatalf("seedAgent: %v", err)
	}
	return agentID, versionID
}

// seedAgentVersion is a back-compat wrapper around seedAgent that returns
// only the version id (the more common test pre-condition).
func seedAgentVersion(t *testing.T, db *sql.DB) string {
	t.Helper()
	_, vid := seedAgent(t, db)
	return vid
}

// seedHTTPBackend creates a tool_backends row of kind http_endpoint and returns its id.
func seedHTTPBackend(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var id string
	err := db.QueryRow(`
		INSERT INTO tool_backends (name, kind, config)
		VALUES ($1, 'http_endpoint', '{"base_url":"https://test.example"}'::jsonb)
		RETURNING id
	`, name).Scan(&id)
	if err != nil {
		t.Fatalf("seedHTTPBackend: %v", err)
	}
	return id
}

// seedMCPBackend creates a tool_backends row of kind mcp_client and returns its id.
func seedMCPBackend(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var id string
	err := db.QueryRow(`
		INSERT INTO tool_backends (name, kind, config)
		VALUES ($1, 'mcp_client', '{"url":"https://test.example","transport":"streamable_http"}'::jsonb)
		RETURNING id
	`, name).Scan(&id)
	if err != nil {
		t.Fatalf("seedMCPBackend: %v", err)
	}
	return id
}

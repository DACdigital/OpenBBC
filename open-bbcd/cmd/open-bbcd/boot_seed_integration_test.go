package main

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/database"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/handler"
)

const bootSeedYAML = `format: openbbc-seed-v1
backends:
  - name: e2e-mcp
    kind: mcp_client
    config:
      url: https://mcp.example.test
      default_headers:
        Authorization: ${E2E_TOKEN}
agents:
  - static_id: e2e_agent
    name: E2E agent
    description: boot seed end to end
    architecture:
      tools: []
versions:
  - static_id: e2e_v1
    agent: e2e_agent
    prompts:
      main_prompt: hello
    mcp_backends:
      - backend: e2e-mcp
    deploy: if_none
`

func openBootTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping boot seed integration test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`TRUNCATE
		deployed_messages, deployed_sessions, chat_messages, chat_sessions,
		resources, agent_versions, agents,
		tool_backends, agent_endpoint_backend, agent_version_mcp_backend, agent_version_subagent
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return db
}

func rowCounts(t *testing.T, db *sql.DB) [3]int {
	t.Helper()
	var c [3]int
	for i, q := range []string{`SELECT count(*) FROM tool_backends`, `SELECT count(*) FROM agents`, `SELECT count(*) FROM agent_versions`} {
		if err := db.QueryRow(q).Scan(&c[i]); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestBoot_SeedThenServe(t *testing.T) {
	db := openBootTestDB(t)
	path := writeSeed(t, bootSeedYAML)
	ctx := context.Background()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	t.Setenv("E2E_TOKEN", "Bearer e2e-secret")
	if err := runBootSeed(ctx, db, path, logger); err != nil {
		t.Fatalf("first boot seed: %v", err)
	}
	if got := rowCounts(t, db); got != [3]int{1, 1, 1} {
		t.Fatalf("row counts after first seed = %v", got)
	}

	api := handler.NewAPI(db, &config.Config{
		Discovery: config.DiscoveryConfig{MaxUploadMB: 50},
		Anthropic: config.AnthropicConfig{DefaultModel: "claude-sonnet-4-6", MaxTokens: 4096},
		Chat:      config.ChatConfig{Transport: "jsonl", MaxToolRounds: 10},
	}, logger)
	req := httptest.NewRequest(http.MethodPost, "/deployed/e2e_agent/sessions", strings.NewReader(`{"user_id":"u1"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("create session via static id: %d %s", rr.Code, rr.Body.String())
	}

	// Second boot: everything but the backend must be unchanged.
	logBuf.Reset()
	if err := runBootSeed(ctx, db, path, logger); err != nil {
		t.Fatalf("second boot seed: %v", err)
	}
	seen := 0
	for _, line := range strings.Split(strings.TrimSpace(logBuf.String()), "\n") {
		if !strings.Contains(line, "msg=seed") || strings.Contains(line, "kind=backend") {
			continue
		}
		seen++
		if !strings.Contains(line, "action=unchanged") {
			t.Errorf("second boot: want unchanged, got %q", line)
		}
	}
	if seen == 0 {
		t.Fatalf("no non-backend seed actions logged: %q", logBuf.String())
	}

	// Missing env var: boot must fail and leave the DB untouched.
	before := rowCounts(t, db)
	os.Unsetenv("E2E_TOKEN")
	err := runBootSeed(ctx, db, path, logger)
	if err == nil || !strings.Contains(err.Error(), "E2E_TOKEN") {
		t.Fatalf("missing env: err = %v", err)
	}
	if after := rowCounts(t, db); after != before {
		t.Fatalf("DB changed on failed seed: %v -> %v", before, after)
	}
}

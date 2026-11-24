package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/lib/pq"
)

const seedYAML = `format: openbbc-seed-v1
backends:
  - name: crm-mcp
    kind: mcp_client
    config:
      url: ${SEEDTEST_URL}
      token: ${SEEDTEST_ALPHA}
`

func writeSeed(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "seed.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSeedCmd_ValidateNoEnvListsVars(t *testing.T) {
	var out bytes.Buffer
	if err := runSeedCmd([]string{"validate", "--no-env", writeSeed(t, seedYAML)}, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "SEEDTEST_ALPHA\nSEEDTEST_URL\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestSeedCmd_ValidateMissingVarFails(t *testing.T) {
	os.Unsetenv("SEEDTEST_URL")
	os.Unsetenv("SEEDTEST_ALPHA")
	err := runSeedCmd([]string{"validate", writeSeed(t, seedYAML)}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "SEEDTEST_URL") {
		t.Fatalf("err = %v", err)
	}
	t.Setenv("SEEDTEST_URL", "http://x")
	t.Setenv("SEEDTEST_ALPHA", "t")
	if err := runSeedCmd([]string{"validate", writeSeed(t, seedYAML)}, io.Discard); err != nil {
		t.Fatalf("with env set: %v", err)
	}
}

func TestSeedCmd_ValidateBadFileNamesKey(t *testing.T) {
	bad := "format: openbbc-seed-v1\nbackends:\n  - name: weird-one\n    kind: bogus\n    config: {}\n"
	err := runSeedCmd([]string{"validate", "--no-env", writeSeed(t, bad)}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "weird-one") {
		t.Fatalf("err = %v", err)
	}
}

func TestSeedCmd_Usage(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"validate"}, {"apply"}} {
		var ue *usageError
		if err := runSeedCmd(args, io.Discard); !asUsage(err, &ue) {
			t.Errorf("args %v: err = %v, want usageError", args, err)
		}
	}
}

func asUsage(err error, target **usageError) bool {
	ue, ok := err.(*usageError)
	if ok {
		*target = ue
	}
	return ok
}

func seedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`TRUNCATE tool_backends RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return db
}

func TestRunBootSeed_EmptyPathClearsFlags(t *testing.T) {
	db := seedTestDB(t)
	if _, err := db.Exec(`INSERT INTO tool_backends (name, kind, config, managed_by_seed) VALUES ('b','mcp_client','{}'::jsonb,true)`); err != nil {
		t.Fatal(err)
	}
	if err := runBootSeed(context.Background(), db, "", slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	var managed bool
	if err := db.QueryRow(`SELECT managed_by_seed FROM tool_backends WHERE name='b'`).Scan(&managed); err != nil || managed {
		t.Fatalf("managed = %v, err = %v; want false", managed, err)
	}
}

func TestRunBootSeed_FailureReturnsError(t *testing.T) {
	db := seedTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := runBootSeed(context.Background(), db, filepath.Join(t.TempDir(), "missing.yaml"), logger); err == nil {
		t.Fatal("want error for missing path")
	}
	t.Setenv("SEEDTEST_URL", "http://x")
	t.Setenv("SEEDTEST_ALPHA", "t")
	if err := runBootSeed(context.Background(), db, writeSeed(t, seedYAML), logger); err != nil {
		t.Fatalf("valid seed: %v", err)
	}
	os.Unsetenv("SEEDTEST_URL")
	if err := runBootSeed(context.Background(), db, writeSeed(t, seedYAML), logger); err == nil || !strings.Contains(err.Error(), "SEEDTEST_URL") {
		t.Fatalf("missing env err = %v", err)
	}
}

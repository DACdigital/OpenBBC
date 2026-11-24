package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func versionStatus(t *testing.T, db *sql.DB, versionID string) string {
	t.Helper()
	var st string
	if err := db.QueryRow(`SELECT status FROM agent_versions WHERE id = $1::uuid`, versionID).Scan(&st); err != nil {
		t.Fatalf("versionStatus: %v", err)
	}
	return st
}

// The unmapped-endpoint check lives in the repository Deploy (inside the
// deploy transaction), not only in the HTTP handler.
func TestDeploy_UnmappedEndpointsRejectedInRepo(t *testing.T) {
	db := openTestDB(t)
	r := NewAgentVersionRepository(db)
	ctx := context.Background()
	agentID, versionID := seedVersionWithStatus(t, db, types.AgentStatusReady)
	seedArchitecture(t, db, agentID, `{"tools":[{"id":"orders.create"},{"id":"orders.list"}]}`)
	backendID := seedHTTPBackend(t, db, "api")
	if err := NewAgentWiringRepository(db).SetEndpointBackend(ctx, agentID, "orders.create", backendID); err != nil {
		t.Fatalf("SetEndpointBackend: %v", err)
	}

	_, err := r.Deploy(ctx, versionID)
	if !errors.Is(err, types.ErrUnmappedEndpoints) {
		t.Fatalf("Deploy = %v, want ErrUnmappedEndpoints", err)
	}
	if !strings.Contains(err.Error(), "orders.list") || strings.Contains(err.Error(), "orders.create") {
		t.Fatalf("error should name only the missing endpoint, got %q", err)
	}
	if st := versionStatus(t, db, versionID); st != string(types.AgentStatusReady) {
		t.Fatalf("status = %q, want READY", st)
	}

	// Mapping the missing endpoint makes the same version deployable.
	if err := NewAgentWiringRepository(db).SetEndpointBackend(ctx, agentID, "orders.list", backendID); err != nil {
		t.Fatalf("SetEndpointBackend: %v", err)
	}
	if _, err := r.Deploy(ctx, versionID); err != nil {
		t.Fatalf("Deploy after mapping: %v", err)
	}
	if st := versionStatus(t, db, versionID); st != string(types.AgentStatusDeployed) {
		t.Fatalf("status = %q, want DEPLOYED", st)
	}
}

// addBindingTx runs inside the caller's transaction; the commit is the
// caller's.
func addBindingInTx(t *testing.T, db *sql.DB, callerID, name, targetID string, allowReadyCaller bool) error {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := addBindingTx(ctx, tx, callerID, name, targetID, "", allowReadyCaller); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return nil
}

func TestAddBindingTx_AllowReadyCaller(t *testing.T) {
	db := openTestDB(t)
	caller := seedVersionStatusOnly(t, db, types.AgentStatusReady)
	target := seedVersionStatusOnly(t, db, types.AgentStatusReady)

	if err := addBindingInTx(t, db, caller, "helper", target, false); !errors.Is(err, types.ErrVersionLocked) {
		t.Fatalf("allowReadyCaller=false = %v, want ErrVersionLocked", err)
	}
	if n := countBindings(t, db, caller); n != 0 {
		t.Fatalf("rows after refused add = %d", n)
	}
	if err := addBindingInTx(t, db, caller, "helper", target, true); err != nil {
		t.Fatalf("allowReadyCaller=true = %v", err)
	}
	if n := countBindings(t, db, caller); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestAddBindingTx_CycleAgainstDB(t *testing.T) {
	db := openTestDB(t)
	a := seedVersionStatusOnly(t, db, types.AgentStatusReady)
	b := seedVersionStatusOnly(t, db, types.AgentStatusReady)
	rawBind(t, db, b, "a", a)

	if err := addBindingInTx(t, db, a, "b", b, true); !errors.Is(err, types.ErrTopologyCycle) {
		t.Fatalf("a→b over existing b→a = %v, want ErrTopologyCycle", err)
	}
	if n := countBindings(t, db, a); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
}

// open-bbcd/internal/repository/agent_version_test.go
package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

// seedReadyAgentVersion creates an agent + a READY version with a dummy bundle.
// Returns (agentID, versionID) plus the repos and db handle from withRepo.
// Callers MUST reuse these returns instead of calling withRepo again (which
// would truncate the just-seeded rows).
func seedReadyAgentVersion(t *testing.T) (agentID, versionID string, agentRepo *AgentRepository, versionRepo *AgentVersionRepository, db *sql.DB) {
	t.Helper()
	agentRepo, versionRepo, db = withRepo(t)
	ctx := context.Background()
	agent, version, err := agentRepo.CreateFromWizard(ctx, types.CreateAgentFromWizardOpts{
		Name: "av-" + uuid.NewString()[:8],
	})
	if err != nil {
		t.Fatalf("CreateFromWizard: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE agent_versions SET status='READY', bundle='{}'::jsonb WHERE id=$1`, version.ID,
	); err != nil {
		t.Fatalf("seed READY: %v", err)
	}
	return agent.ID, version.ID, agentRepo, versionRepo, db
}

// insertReadyChildVersion inserts a READY child version under parentID.
func insertReadyChildVersion(t *testing.T, db *sql.DB, parentID string) string {
	t.Helper()
	var agentID string
	if err := db.QueryRow(`SELECT agent_id::text FROM agent_versions WHERE id=$1`, parentID).Scan(&agentID); err != nil {
		t.Fatalf("lookup agent: %v", err)
	}
	id := uuid.NewString()
	if _, err := db.Exec(`
		INSERT INTO agent_versions (id, agent_id, parent_version_id, status, bundle)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'READY', '{}'::jsonb)
	`, id, agentID, parentID); err != nil {
		t.Fatalf("insert child: %v", err)
	}
	return id
}

func TestAgentVersionRepository_GetByID(t *testing.T) {
	_, versionID, _, versionRepo, _ := seedReadyAgentVersion(t)
	v, err := versionRepo.GetByID(context.Background(), versionID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if v.ID != versionID {
		t.Fatalf("ID mismatch")
	}
	if v.Status != "READY" {
		t.Fatalf("status=%q", v.Status)
	}
}

func TestAgentVersionRepository_GetWithAgent(t *testing.T) {
	agentID, versionID, _, versionRepo, _ := seedReadyAgentVersion(t)
	v, a, err := versionRepo.GetWithAgent(context.Background(), versionID)
	if err != nil {
		t.Fatalf("GetWithAgent: %v", err)
	}
	if v.AgentID != agentID || a.ID != agentID {
		t.Fatalf("mismatch v=%q a=%q want %q", v.AgentID, a.ID, agentID)
	}
}

func TestAgentVersionRepository_Deploy_HappyPath(t *testing.T) {
	_, versionID, _, versionRepo, _ := seedReadyAgentVersion(t)
	ctx := context.Background()
	prev, err := versionRepo.Deploy(ctx, versionID)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if prev != nil {
		t.Fatalf("prev should be nil, got %q", *prev)
	}
	v, _ := versionRepo.GetByID(ctx, versionID)
	if v.Status != "DEPLOYED" {
		t.Fatalf("status=%q", v.Status)
	}
}

func TestAgentVersionRepository_Deploy_Rotates(t *testing.T) {
	_, versionID, _, versionRepo, db := seedReadyAgentVersion(t)
	ctx := context.Background()
	_, _ = versionRepo.Deploy(ctx, versionID)
	child := insertReadyChildVersion(t, db, versionID)
	prev, err := versionRepo.Deploy(ctx, child)
	if err != nil {
		t.Fatalf("Deploy child: %v", err)
	}
	if prev == nil || *prev != versionID {
		t.Fatalf("prev=%v want %q", prev, versionID)
	}
	rootAfter, _ := versionRepo.GetByID(ctx, versionID)
	childAfter, _ := versionRepo.GetByID(ctx, child)
	if rootAfter.Status != "READY" {
		t.Fatalf("root status=%q", rootAfter.Status)
	}
	if childAfter.Status != "DEPLOYED" {
		t.Fatalf("child status=%q", childAfter.Status)
	}
}

func TestAgentVersionRepository_Deploy_NotDeployable(t *testing.T) {
	// Create an agent + INITIALIZING version (Status not READY).
	agentRepo, versionRepo, _ := withRepo(t)
	ctx := context.Background()
	_, version, err := agentRepo.CreateFromWizard(ctx, types.CreateAgentFromWizardOpts{Name: "nd-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err = versionRepo.Deploy(ctx, version.ID)
	if !errors.Is(err, types.ErrAgentNotDeployable) {
		t.Fatalf("got %v want ErrAgentNotDeployable", err)
	}
}

func TestAgentVersionRepository_Deploy_Idempotent(t *testing.T) {
	_, versionID, _, versionRepo, _ := seedReadyAgentVersion(t)
	ctx := context.Background()
	_, _ = versionRepo.Deploy(ctx, versionID)
	prev, err := versionRepo.Deploy(ctx, versionID)
	if err != nil {
		t.Fatalf("re-Deploy: %v", err)
	}
	if prev != nil {
		t.Fatalf("prev should be nil, got %q", *prev)
	}
	v, _ := versionRepo.GetByID(ctx, versionID)
	if v.Status != "DEPLOYED" {
		t.Fatalf("status=%q", v.Status)
	}
}

func TestAgentVersionRepository_Undeploy(t *testing.T) {
	_, versionID, _, versionRepo, _ := seedReadyAgentVersion(t)
	ctx := context.Background()
	_, _ = versionRepo.Deploy(ctx, versionID)
	if err := versionRepo.Undeploy(ctx, versionID); err != nil {
		t.Fatalf("Undeploy: %v", err)
	}
	v, _ := versionRepo.GetByID(ctx, versionID)
	if v.Status != "READY" {
		t.Fatalf("status=%q", v.Status)
	}
	if err := versionRepo.Undeploy(ctx, versionID); !errors.Is(err, types.ErrAgentNotDeployed) {
		t.Fatalf("re-Undeploy: got %v want ErrAgentNotDeployed", err)
	}
}

func TestAgentVersionRepository_CurrentDeployedID(t *testing.T) {
	agentID, versionID, _, versionRepo, _ := seedReadyAgentVersion(t)
	ctx := context.Background()
	id, err := versionRepo.CurrentDeployedID(ctx, agentID)
	if err != nil {
		t.Fatalf("CurrentDeployedID empty: %v", err)
	}
	if id != "" {
		t.Fatalf("empty expected, got %q", id)
	}
	_, _ = versionRepo.Deploy(ctx, versionID)
	id, _ = versionRepo.CurrentDeployedID(ctx, agentID)
	if id != versionID {
		t.Fatalf("got %q want %q", id, versionID)
	}
}

func TestAgentVersionRepository_PartialUniqueIndex_RejectsDoubleDeploy(t *testing.T) {
	_, _, db := withRepo(t)
	ctx := context.Background()
	agentID := uuid.NewString()
	_, err := db.ExecContext(ctx, `INSERT INTO agents (id, name) VALUES ($1::uuid, $2)`, agentID, "dbl-test")
	if err != nil {
		t.Fatalf("agent insert: %v", err)
	}
	id1 := uuid.NewString()
	_, err = db.ExecContext(ctx,
		`INSERT INTO agent_versions (id, agent_id, status, bundle) VALUES ($1::uuid, $2::uuid, 'DEPLOYED', '{}'::jsonb)`,
		id1, agentID,
	)
	if err != nil {
		t.Fatalf("first deploy insert: %v", err)
	}
	id2 := uuid.NewString()
	_, err = db.ExecContext(ctx,
		`INSERT INTO agent_versions (id, agent_id, parent_version_id, status, bundle) VALUES ($1::uuid, $2::uuid, $3::uuid, 'DEPLOYED', '{}'::jsonb)`,
		id2, agentID, id1,
	)
	if err == nil {
		t.Fatalf("expected partial unique index violation")
	}
}

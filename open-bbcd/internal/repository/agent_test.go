package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

func TestAgentRepository_Create_ValidationError(t *testing.T) {
	repo := NewAgentRepository(nil) // nil db, won't reach it

	_, err := repo.Create(nil, types.CreateAgentOpts{Name: ""})
	if err != types.ErrNameRequired {
		t.Errorf("error = %v, want %v", err, types.ErrNameRequired)
	}
}

func TestAgentRepository_CreateFromWizard_ValidationError(t *testing.T) {
	repo := NewAgentRepository(nil)
	_, err := repo.CreateFromWizard(context.Background(), types.CreateAgentFromWizardOpts{Name: ""})
	if err != types.ErrNameRequired {
		t.Errorf("error = %v, want %v", err, types.ErrNameRequired)
	}
}

func TestAgentRepository_Create_SetsAgentIDToSelf(t *testing.T) {
	t.Parallel()
	repo, _ := withRepo(t)
	ctx := context.Background()

	agent, err := repo.Create(ctx, types.CreateAgentOpts{Name: "deploy-test-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if agent.AgentID != agent.ID {
		t.Fatalf("expected agent_id == id, got agent_id=%q id=%q",
			agent.AgentID, agent.ID)
	}
}

func TestAgentRepository_CreateFromWizard_SetsAgentIDToSelf(t *testing.T) {
	t.Parallel()
	repo, _ := withRepo(t)
	ctx := context.Background()

	agent, err := repo.CreateFromWizard(ctx, types.CreateAgentFromWizardOpts{
		Name: "deploy-test-2",
	})
	if err != nil {
		t.Fatalf("CreateFromWizard: %v", err)
	}
	if agent.AgentID != agent.ID {
		t.Fatalf("expected agent_id == id, got agent_id=%q id=%q",
			agent.AgentID, agent.ID)
	}
}

// helper: insert a READY child version under an existing chain root, bypassing
// the configurator. Used only in repo tests.
func insertReadyChildVersion(t *testing.T, db *sql.DB, parentID string) string {
	t.Helper()
	var rootID string
	if err := db.QueryRow(`SELECT agent_id::text FROM agents WHERE id=$1`, parentID).Scan(&rootID); err != nil {
		t.Fatalf("lookup root: %v", err)
	}
	id := uuid.NewString()
	_, err := db.Exec(`
		INSERT INTO agents (id, agent_id, name, status, parent_version_id, bundle)
		SELECT $1::uuid, $2::uuid, name, 'READY', $3::uuid, '{}'::jsonb FROM agents WHERE id=$3::uuid
	`, id, rootID, parentID)
	if err != nil {
		t.Fatalf("insert child: %v", err)
	}
	return id
}

func TestAgentRepository_Deploy_HappyPath(t *testing.T) {
	repo, db := withRepo(t)
	ctx := context.Background()

	root, err := repo.Create(ctx, types.CreateAgentOpts{Name: "deploy-happy"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE agents SET status='READY' WHERE id=$1`, root.ID); err != nil {
		t.Fatalf("seed READY: %v", err)
	}

	prev, err := repo.Deploy(ctx, root.ID)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if prev != nil {
		t.Fatalf("expected nil prev (first deploy in chain), got %q", *prev)
	}

	after, err := repo.GetByID(ctx, root.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if after.Status != string(types.AgentStatusDeployed) {
		t.Fatalf("got status %q, want DEPLOYED", after.Status)
	}
}

func TestAgentRepository_Deploy_Rotates(t *testing.T) {
	repo, db := withRepo(t)
	ctx := context.Background()

	root, _ := repo.Create(ctx, types.CreateAgentOpts{Name: "deploy-rotate"})
	_, _ = db.ExecContext(ctx, `UPDATE agents SET status='READY' WHERE id=$1`, root.ID)
	_, _ = repo.Deploy(ctx, root.ID)

	child := insertReadyChildVersion(t, db, root.ID)

	prev, err := repo.Deploy(ctx, child)
	if err != nil {
		t.Fatalf("Deploy child: %v", err)
	}
	if prev == nil || *prev != root.ID {
		t.Fatalf("expected prev=%q, got %v", root.ID, prev)
	}

	rootAfter, _ := repo.GetByID(ctx, root.ID)
	childAfter, _ := repo.GetByID(ctx, child)
	if rootAfter.Status != "READY" {
		t.Fatalf("root status: got %q want READY", rootAfter.Status)
	}
	if childAfter.Status != "DEPLOYED" {
		t.Fatalf("child status: got %q want DEPLOYED", childAfter.Status)
	}
}

func TestAgentRepository_Deploy_NotDeployable(t *testing.T) {
	repo, _ := withRepo(t)
	ctx := context.Background()

	root, _ := repo.Create(ctx, types.CreateAgentOpts{Name: "deploy-bad"})
	// Default status is DRAFT — not deployable.

	_, err := repo.Deploy(ctx, root.ID)
	if !errors.Is(err, types.ErrAgentNotDeployable) {
		t.Fatalf("got %v, want ErrAgentNotDeployable", err)
	}
}

func TestAgentRepository_Deploy_Idempotent(t *testing.T) {
	repo, db := withRepo(t)
	ctx := context.Background()

	root, _ := repo.Create(ctx, types.CreateAgentOpts{Name: "deploy-idem"})
	_, _ = db.ExecContext(ctx, `UPDATE agents SET status='READY' WHERE id=$1`, root.ID)
	_, _ = repo.Deploy(ctx, root.ID)

	prev, err := repo.Deploy(ctx, root.ID)
	if err != nil {
		t.Fatalf("Deploy (again): %v", err)
	}
	if prev != nil {
		t.Fatalf("expected nil prev on re-deploy, got %q", *prev)
	}

	after, _ := repo.GetByID(ctx, root.ID)
	if after.Status != "DEPLOYED" {
		t.Fatalf("status: got %q want DEPLOYED", after.Status)
	}
}

func TestAgentRepository_Undeploy(t *testing.T) {
	repo, db := withRepo(t)
	ctx := context.Background()

	root, _ := repo.Create(ctx, types.CreateAgentOpts{Name: "undeploy-test"})
	_, _ = db.ExecContext(ctx, `UPDATE agents SET status='READY' WHERE id=$1`, root.ID)
	_, _ = repo.Deploy(ctx, root.ID)

	if err := repo.Undeploy(ctx, root.ID); err != nil {
		t.Fatalf("Undeploy: %v", err)
	}
	after, _ := repo.GetByID(ctx, root.ID)
	if after.Status != "READY" {
		t.Fatalf("got %q, want READY", after.Status)
	}

	// Idempotency check — calling undeploy on a non-deployed should 409.
	err := repo.Undeploy(ctx, root.ID)
	if !errors.Is(err, types.ErrAgentNotDeployed) {
		t.Fatalf("got %v, want ErrAgentNotDeployed", err)
	}
}

func TestAgentRepository_CurrentDeployedVersionID(t *testing.T) {
	repo, db := withRepo(t)
	ctx := context.Background()

	root, _ := repo.Create(ctx, types.CreateAgentOpts{Name: "deployed-lookup"})

	// No deployed version yet.
	id, err := repo.CurrentDeployedVersionID(ctx, root.ID)
	if err != nil {
		t.Fatalf("CurrentDeployedVersionID: %v", err)
	}
	if id != "" {
		t.Fatalf("expected empty, got %q", id)
	}

	_, _ = db.ExecContext(ctx, `UPDATE agents SET status='READY' WHERE id=$1`, root.ID)
	_, _ = repo.Deploy(ctx, root.ID)

	id, err = repo.CurrentDeployedVersionID(ctx, root.ID)
	if err != nil {
		t.Fatalf("CurrentDeployedVersionID: %v", err)
	}
	if id != root.ID {
		t.Fatalf("got %q, want %q", id, root.ID)
	}
}

func TestAgentRepository_PartialUniqueIndex_RejectsDoubleDeploy(t *testing.T) {
	_, db := withRepo(t)
	ctx := context.Background()

	rootID := uuid.NewString()
	_, err := db.ExecContext(ctx,
		`INSERT INTO agents (id, agent_id, name, status) VALUES ($1::uuid, $1::uuid, 'dbl-a', 'DEPLOYED')`,
		rootID)
	if err != nil {
		t.Fatalf("first deploy insert: %v", err)
	}
	siblingID := uuid.NewString()
	_, err = db.ExecContext(ctx,
		`INSERT INTO agents (id, agent_id, name, status, parent_version_id) VALUES ($1::uuid, $2::uuid, 'dbl-a', 'DEPLOYED', $2::uuid)`,
		siblingID, rootID)
	if err == nil {
		t.Fatalf("expected partial unique index violation, got nil error")
	}
}

package repository

import (
	"context"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
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
	_, _, err := repo.CreateFromWizard(context.Background(), types.CreateAgentFromWizardOpts{Name: ""})
	if err != types.ErrNameRequired {
		t.Errorf("error = %v, want %v", err, types.ErrNameRequired)
	}
}

func TestAgentRepository_Create_ReturnsAgent(t *testing.T) {
	repo, _ := withRepo(t)
	ctx := context.Background()
	a, err := repo.Create(ctx, types.CreateAgentOpts{Name: "create-test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if a.ID == "" {
		t.Fatalf("ID empty")
	}
	if a.Name != "create-test" {
		t.Fatalf("Name=%q", a.Name)
	}
}

func TestAgentRepository_CreateFromWizard_CreatesAgentAndVersion(t *testing.T) {
	repo, db := withRepo(t)
	ctx := context.Background()
	agent, version, err := repo.CreateFromWizard(ctx, types.CreateAgentFromWizardOpts{
		Name: "wizard-test",
	})
	if err != nil {
		t.Fatalf("CreateFromWizard: %v", err)
	}
	if agent.Name != "wizard-test" {
		t.Fatalf("agent name=%q", agent.Name)
	}
	if version.AgentID != agent.ID {
		t.Fatalf("version.AgentID=%q want %q", version.AgentID, agent.ID)
	}
	if version.Status != "INITIALIZING" {
		t.Fatalf("version.Status=%q", version.Status)
	}
	// Verify the rows exist
	var count int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM agent_versions WHERE id=$1`, version.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("expected agent_versions row, count=%d", count)
	}
}

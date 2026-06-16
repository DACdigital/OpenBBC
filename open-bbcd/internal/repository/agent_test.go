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
	_, err := repo.CreateFromWizard(context.Background(), types.CreateAgentFromWizardOpts{Name: ""})
	if err != types.ErrNameRequired {
		t.Errorf("error = %v, want %v", err, types.ErrNameRequired)
	}
}

func TestAgentRepository_Create_SetsChainRootIDToSelf(t *testing.T) {
	t.Parallel()
	repo, _ := withRepo(t)
	ctx := context.Background()

	agent, err := repo.Create(ctx, types.CreateAgentOpts{Name: "deploy-test-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if agent.ChainRootID != agent.ID {
		t.Fatalf("expected chain_root_id == id, got chain_root_id=%q id=%q",
			agent.ChainRootID, agent.ID)
	}
}

func TestAgentRepository_CreateFromWizard_SetsChainRootIDToSelf(t *testing.T) {
	t.Parallel()
	repo, _ := withRepo(t)
	ctx := context.Background()

	agent, err := repo.CreateFromWizard(ctx, types.CreateAgentFromWizardOpts{
		Name: "deploy-test-2",
	})
	if err != nil {
		t.Fatalf("CreateFromWizard: %v", err)
	}
	if agent.ChainRootID != agent.ID {
		t.Fatalf("expected chain_root_id == id, got chain_root_id=%q id=%q",
			agent.ChainRootID, agent.ID)
	}
}

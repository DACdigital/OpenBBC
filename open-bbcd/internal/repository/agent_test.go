package repository

import (
	"context"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestAgentRepository_Create_ValidationError(t *testing.T) {
	repo := NewAgentRepository(nil) // nil db, won't reach it

	_, err := repo.Create(context.Background(), types.CreateAgentOpts{Name: "", Prompt: ""})
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

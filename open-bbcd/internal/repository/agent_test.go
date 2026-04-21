package repository

import (
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestAgentRepository_Create_ValidationError(t *testing.T) {
	repo := NewAgentRepository(nil) // nil db, won't reach it

	_, err := repo.Create(nil, types.CreateAgentInput{Name: "", Prompt: ""})
	if err != types.ErrNameRequired {
		t.Errorf("error = %v, want %v", err, types.ErrNameRequired)
	}
}

package repository

import (
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestResourceRepository_Create_ValidationError(t *testing.T) {
	repo := NewResourceRepository(nil)

	_, err := repo.Create(nil, types.CreateResourceInput{})
	if err != types.ErrAgentRequired {
		t.Errorf("error = %v, want %v", err, types.ErrAgentRequired)
	}
}

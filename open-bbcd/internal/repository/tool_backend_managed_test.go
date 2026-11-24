package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func createManagedBackend(t *testing.T, repo *ToolBackendRepository, name string) *types.ToolBackend {
	t.Helper()
	be := &types.ToolBackend{Name: name, Kind: types.ToolBackendKindHTTPEndpoint, Config: json.RawMessage(`{}`)}
	if err := repo.Create(context.Background(), be); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := repo.db.Exec(`UPDATE tool_backends SET managed_by_seed = true WHERE id = $1`, be.ID); err != nil {
		t.Fatalf("mark managed: %v", err)
	}
	return be
}

func TestToolBackend_ManagedRejectsUpdateDelete(t *testing.T) {
	db := openTestDB(t)
	repo := NewToolBackendRepository(db)
	ctx := context.Background()
	be := createManagedBackend(t, repo, "managed-be")

	be.Name = "renamed"
	if err := repo.Update(ctx, be); !errors.Is(err, types.ErrBackendManaged) {
		t.Fatalf("Update: want ErrBackendManaged, got %v", err)
	}
	if err := repo.Delete(ctx, be.ID); !errors.Is(err, types.ErrBackendManaged) {
		t.Fatalf("Delete: want ErrBackendManaged, got %v", err)
	}
	got, err := repo.Get(ctx, be.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "managed-be" || !got.ManagedBySeed {
		t.Fatalf("row changed: %+v", got)
	}
}

func TestToolBackend_UnmanagedStillUpdatableAndDeletable(t *testing.T) {
	db := openTestDB(t)
	repo := NewToolBackendRepository(db)
	ctx := context.Background()
	be := &types.ToolBackend{Name: "free", Kind: types.ToolBackendKindHTTPEndpoint, Config: json.RawMessage(`{}`)}
	if err := repo.Create(ctx, be); err != nil {
		t.Fatal(err)
	}
	be.Name = "free2"
	if err := repo.Update(ctx, be); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := repo.Delete(ctx, be.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := repo.Delete(ctx, be.ID); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("second Delete: want ErrNotFound, got %v", err)
	}
}

func TestToolBackend_GetByNameAndManagedRoundTrip(t *testing.T) {
	db := openTestDB(t)
	repo := NewToolBackendRepository(db)
	ctx := context.Background()
	be := createManagedBackend(t, repo, "by-name")

	got, err := repo.GetByName(ctx, "by-name")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.ID != be.ID || !got.ManagedBySeed {
		t.Fatalf("got %+v", got)
	}
	if _, err := repo.GetByName(ctx, "missing"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("miss: want ErrNotFound, got %v", err)
	}
	listed, err := repo.List(ctx)
	if err != nil || len(listed) != 1 || !listed[0].ManagedBySeed {
		t.Fatalf("List: %v %+v", err, listed)
	}
}

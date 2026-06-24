package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestToolBackendRepo_CRUD(t *testing.T) {
	db := openTestDB(t)
	repo := NewToolBackendRepository(db)
	ctx := context.Background()

	cfg := types.HTTPBackendConfig{BaseURL: "https://api.example.com"}
	cfgJSON, _ := json.Marshal(cfg)
	be := &types.ToolBackend{Name: "api", Kind: types.ToolBackendKindHTTPEndpoint, Config: cfgJSON}

	if err := repo.Create(ctx, be); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if be.ID == "" {
		t.Fatalf("expected id")
	}

	got, err := repo.Get(ctx, be.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "api" {
		t.Fatalf("got %s", got.Name)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1, got %d", len(list))
	}

	if err := repo.Delete(ctx, be.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestToolBackendRepo_DuplicateNameRejected(t *testing.T) {
	db := openTestDB(t)
	repo := NewToolBackendRepository(db)
	ctx := context.Background()
	a := &types.ToolBackend{Name: "x", Kind: types.ToolBackendKindHTTPEndpoint, Config: json.RawMessage(`{}`)}
	b := &types.ToolBackend{Name: "x", Kind: types.ToolBackendKindHTTPEndpoint, Config: json.RawMessage(`{}`)}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	err := repo.Create(ctx, b)
	if err == nil || !errors.Is(err, types.ErrToolBackendNameTaken) {
		t.Fatalf("want ErrToolBackendNameTaken, got %v", err)
	}
}

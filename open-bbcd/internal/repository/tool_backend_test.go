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

func TestToolBackendRepo_Get_NotFound(t *testing.T) {
	db := openTestDB(t)
	repo := NewToolBackendRepository(db)
	_, err := repo.Get(context.Background(), "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestToolBackendRepo_Update_NotFound(t *testing.T) {
	db := openTestDB(t)
	repo := NewToolBackendRepository(db)
	be := &types.ToolBackend{
		ID:     "00000000-0000-0000-0000-000000000000",
		Name:   "ghost",
		Kind:   types.ToolBackendKindHTTPEndpoint,
		Config: json.RawMessage(`{}`),
	}
	err := repo.Update(context.Background(), be)
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestToolBackendRepo_Delete_NotFound(t *testing.T) {
	db := openTestDB(t)
	repo := NewToolBackendRepository(db)
	err := repo.Delete(context.Background(), "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestToolBackendRepo_Update_HappyPath(t *testing.T) {
	db := openTestDB(t)
	repo := NewToolBackendRepository(db)
	ctx := context.Background()
	be := &types.ToolBackend{
		Name:   "api",
		Kind:   types.ToolBackendKindHTTPEndpoint,
		Config: json.RawMessage(`{"base_url":"https://before"}`),
	}
	if err := repo.Create(ctx, be); err != nil { t.Fatalf("Create: %v", err) }
	orig := be.UpdatedAt
	be.Name = "api-renamed"
	be.Config = json.RawMessage(`{"base_url":"https://after"}`)
	if err := repo.Update(ctx, be); err != nil { t.Fatalf("Update: %v", err) }
	if !be.UpdatedAt.After(orig) { t.Fatalf("UpdatedAt did not advance: %v vs %v", orig, be.UpdatedAt) }
	got, err := repo.Get(ctx, be.ID)
	if err != nil { t.Fatalf("Get: %v", err) }
	if got.Name != "api-renamed" { t.Fatalf("name not updated: %s", got.Name) }
}

func TestToolBackendRepo_Delete_BlockedByWiring(t *testing.T) {
	db := openTestDB(t)
	backendRepo := NewToolBackendRepository(db)
	agentWiring := NewAgentWiringRepository(db)
	ctx := context.Background()

	backendID := seedHTTPBackend(t, db, "wired-backend")
	agentID, _ := seedAgent(t, db)
	if err := agentWiring.SetEndpointBackend(ctx, agentID, "ep.foo", backendID); err != nil {
		t.Fatalf("SetEndpointBackend: %v", err)
	}
	err := backendRepo.Delete(ctx, backendID)
	if !errors.Is(err, types.ErrToolBackendInUse) {
		t.Fatalf("want ErrToolBackendInUse, got %v", err)
	}
}

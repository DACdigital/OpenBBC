package repository

import (
	"context"
	"testing"
)

func TestAgentWiring_EndpointBackend(t *testing.T) {
	db := openTestDB(t)
	repo := NewAgentWiringRepository(db)
	ctx := context.Background()

	agentID, _ := seedAgent(t, db)
	backendID := seedHTTPBackend(t, db, "api")

	if err := repo.SetEndpointBackend(ctx, agentID, "orders.create", backendID); err != nil {
		t.Fatalf("SetEndpointBackend: %v", err)
	}
	m, err := repo.ListEndpointBackends(ctx, agentID)
	if err != nil {
		t.Fatalf("ListEndpointBackends: %v", err)
	}
	if got := m["orders.create"]; got != backendID {
		t.Fatalf("want %s, got %s", backendID, got)
	}

	// Upsert: setting again with a different backend updates the row, no error.
	backend2 := seedHTTPBackend(t, db, "api2")
	if err := repo.SetEndpointBackend(ctx, agentID, "orders.create", backend2); err != nil {
		t.Fatalf("SetEndpointBackend (upsert): %v", err)
	}
	m, _ = repo.ListEndpointBackends(ctx, agentID)
	if got := m["orders.create"]; got != backend2 {
		t.Fatalf("upsert failed: want %s, got %s", backend2, got)
	}

	if err := repo.UnsetEndpointBackend(ctx, agentID, "orders.create"); err != nil {
		t.Fatalf("UnsetEndpointBackend: %v", err)
	}
	m, _ = repo.ListEndpointBackends(ctx, agentID)
	if _, ok := m["orders.create"]; ok {
		t.Fatalf("expected unset")
	}
}

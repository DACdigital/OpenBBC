package repository

import (
	"context"
	"testing"
)

func TestVersionWiring_EndpointBackend(t *testing.T) {
	db := openTestDB(t)
	repo := NewVersionWiringRepository(db)
	ctx := context.Background()

	versionID := seedAgentVersion(t, db)
	backendID := seedHTTPBackend(t, db, "api")

	if err := repo.SetEndpointBackend(ctx, versionID, "orders.create", backendID); err != nil {
		t.Fatalf("SetEndpointBackend: %v", err)
	}
	m, err := repo.ListEndpointBackends(ctx, versionID)
	if err != nil {
		t.Fatalf("ListEndpointBackends: %v", err)
	}
	if got := m["orders.create"]; got != backendID {
		t.Fatalf("want %s, got %s", backendID, got)
	}

	// Upsert: setting again with a different backend updates the row, no error.
	backend2 := seedHTTPBackend(t, db, "api2")
	if err := repo.SetEndpointBackend(ctx, versionID, "orders.create", backend2); err != nil {
		t.Fatalf("SetEndpointBackend (upsert): %v", err)
	}
	m, _ = repo.ListEndpointBackends(ctx, versionID)
	if got := m["orders.create"]; got != backend2 {
		t.Fatalf("upsert failed: want %s, got %s", backend2, got)
	}

	if err := repo.UnsetEndpointBackend(ctx, versionID, "orders.create"); err != nil {
		t.Fatalf("UnsetEndpointBackend: %v", err)
	}
	m, _ = repo.ListEndpointBackends(ctx, versionID)
	if _, ok := m["orders.create"]; ok {
		t.Fatalf("expected unset")
	}
}

func TestVersionWiring_MCPAttachment(t *testing.T) {
	db := openTestDB(t)
	repo := NewVersionWiringRepository(db)
	ctx := context.Background()
	versionID := seedAgentVersion(t, db)
	backendID := seedMCPBackend(t, db, "slack")

	if err := repo.AttachMCP(ctx, versionID, backendID, "use slack for escalations"); err != nil {
		t.Fatalf("AttachMCP: %v", err)
	}
	atts, err := repo.ListMCPAttachments(ctx, versionID)
	if err != nil {
		t.Fatalf("ListMCPAttachments: %v", err)
	}
	if len(atts) != 1 || atts[0].Note != "use slack for escalations" {
		t.Fatalf("got %+v", atts)
	}
	if atts[0].BackendID != backendID {
		t.Fatalf("backend id mismatch: want %s, got %s", backendID, atts[0].BackendID)
	}

	// Upsert: re-attach with a new note updates the note.
	if err := repo.AttachMCP(ctx, versionID, backendID, "updated note"); err != nil {
		t.Fatalf("AttachMCP (upsert): %v", err)
	}
	atts, _ = repo.ListMCPAttachments(ctx, versionID)
	if atts[0].Note != "updated note" {
		t.Fatalf("upsert failed: got %q", atts[0].Note)
	}

	if err := repo.DetachMCP(ctx, versionID, backendID); err != nil {
		t.Fatalf("DetachMCP: %v", err)
	}
	atts, _ = repo.ListMCPAttachments(ctx, versionID)
	if len(atts) != 0 {
		t.Fatalf("expected 0, got %d", len(atts))
	}
}

func TestVersionWiring_UsageCounts(t *testing.T) {
	db := openTestDB(t)
	repo := NewVersionWiringRepository(db)
	ctx := context.Background()

	httpB := seedHTTPBackend(t, db, "api")
	mcpB := seedMCPBackend(t, db, "slack")
	v1 := seedAgentVersion(t, db)
	v2 := seedAgentVersion(t, db)

	_ = repo.SetEndpointBackend(ctx, v1, "orders.create", httpB)
	_ = repo.SetEndpointBackend(ctx, v2, "orders.create", httpB) // 2 versions using httpB
	_ = repo.AttachMCP(ctx, v1, mcpB, "")                        // 1 version using mcpB

	counts, err := repo.UsageCounts(ctx)
	if err != nil {
		t.Fatalf("UsageCounts: %v", err)
	}
	if counts[httpB] != 2 {
		t.Fatalf("httpB usage: want 2, got %d", counts[httpB])
	}
	if counts[mcpB] != 1 {
		t.Fatalf("mcpB usage: want 1, got %d", counts[mcpB])
	}
}

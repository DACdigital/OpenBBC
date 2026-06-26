package repository

import (
	"context"
	"testing"
)

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
	agentWiring := NewAgentWiringRepository(db)
	ctx := context.Background()

	httpB := seedHTTPBackend(t, db, "api")
	mcpB := seedMCPBackend(t, db, "slack")
	a1, v1 := seedAgent(t, db)
	a2, _ := seedAgent(t, db)

	// HTTP wiring is per-agent (post-017): two distinct agents → count = 2.
	_ = agentWiring.SetEndpointBackend(ctx, a1, "orders.create", httpB)
	_ = agentWiring.SetEndpointBackend(ctx, a2, "orders.create", httpB)
	// MCP attachment stays per-version.
	_ = repo.AttachMCP(ctx, v1, mcpB, "")

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

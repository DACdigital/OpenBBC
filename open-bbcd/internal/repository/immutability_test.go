package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// Versions are immutable once they leave INITIALIZING/DRAFT (spec
// 2026-11-24-declarative-seed-design.md §4).

var lockedStatuses = []types.AgentStatus{
	types.AgentStatusPending, types.AgentStatusReady, types.AgentStatusTraining, types.AgentStatusDeployed,
}

const immutabilityTestBundle = `{
	"main_prompt": "<role>coffee bot</role>",
	"tools": [{"id":"orders.create","name":"orders_create","method":"POST","path":"/api/orders"}],
	"skills": [{"name":"place_order","description":"Take an order","prompt":"<role>order</role>"}]
}`

func TestAttachMCP_LockedOutsideDraft(t *testing.T) {
	for _, st := range lockedStatuses {
		t.Run(string(st), func(t *testing.T) {
			db := openTestDB(t)
			wiring := NewVersionWiringRepository(db)
			ctx := context.Background()
			_, vid := seedVersionWithStatus(t, db, st)
			be := seedMCPBackend(t, db, "slack")
			// Pre-existing attachment so DetachMCP has something to remove.
			if _, err := db.Exec(`INSERT INTO agent_version_mcp_backend (agent_version_id, backend_id, note) VALUES ($1::uuid, $2::uuid, 'old')`, vid, be); err != nil {
				t.Fatal(err)
			}

			if err := wiring.AttachMCP(ctx, vid, be, "new"); !errors.Is(err, types.ErrVersionLocked) {
				t.Fatalf("AttachMCP = %v, want ErrVersionLocked", err)
			}
			if err := wiring.DetachMCP(ctx, vid, be); !errors.Is(err, types.ErrVersionLocked) {
				t.Fatalf("DetachMCP = %v, want ErrVersionLocked", err)
			}
			atts, err := wiring.ListMCPAttachments(ctx, vid)
			if err != nil {
				t.Fatal(err)
			}
			if len(atts) != 1 || atts[0].Note != "old" {
				t.Fatalf("attachments changed: %+v", atts)
			}
		})
	}
	for _, st := range []types.AgentStatus{types.AgentStatusInitializing, types.AgentStatusDraft} {
		t.Run(string(st), func(t *testing.T) {
			db := openTestDB(t)
			wiring := NewVersionWiringRepository(db)
			ctx := context.Background()
			_, vid := seedVersionWithStatus(t, db, st)
			be := seedMCPBackend(t, db, "slack")
			if err := wiring.AttachMCP(ctx, vid, be, "n"); err != nil {
				t.Fatalf("AttachMCP: %v", err)
			}
			if err := wiring.DetachMCP(ctx, vid, be); err != nil {
				t.Fatalf("DetachMCP: %v", err)
			}
		})
	}
	t.Run("missing version", func(t *testing.T) {
		db := openTestDB(t)
		wiring := NewVersionWiringRepository(db)
		be := seedMCPBackend(t, db, "slack")
		err := wiring.AttachMCP(context.Background(), "00000000-0000-0000-0000-000000000000", be, "")
		if !errors.Is(err, types.ErrNotFound) {
			t.Fatalf("AttachMCP = %v, want ErrNotFound", err)
		}
	})
}

func readArchitecture(t *testing.T, db sqlQueryer, agentID string) string {
	t.Helper()
	var arch *string
	if err := db.QueryRowContext(context.Background(), `SELECT architecture::text FROM agents WHERE id = $1::uuid`, agentID).Scan(&arch); err != nil {
		t.Fatal(err)
	}
	if arch == nil {
		return ""
	}
	return *arch
}

func TestLandBundle_StatusGuard(t *testing.T) {
	cases := []struct {
		status types.AgentStatus
		force  bool
		want   error
	}{
		{types.AgentStatusPending, false, nil},
		{types.AgentStatusInitializing, false, types.ErrVersionLocked},
		{types.AgentStatusReady, false, types.ErrVersionLocked},
		{types.AgentStatusInitializing, true, nil},
		{types.AgentStatusPending, true, nil},
		{types.AgentStatusReady, true, types.ErrVersionLocked},
	}
	for _, c := range cases {
		name := string(c.status)
		if c.force {
			name += "/force"
		}
		t.Run(name, func(t *testing.T) {
			db := openTestDB(t)
			repo := NewAgentVersionRepository(db)
			_, vid := seedVersionWithStatus(t, db, c.status)
			err := repo.LandBundle(context.Background(), vid, []byte(immutabilityTestBundle), c.force)
			if !errors.Is(err, c.want) {
				t.Fatalf("LandBundle = %v, want %v", err, c.want)
			}
			var status string
			if err := db.QueryRow(`SELECT status FROM agent_versions WHERE id = $1::uuid`, vid).Scan(&status); err != nil {
				t.Fatal(err)
			}
			want := string(c.status)
			if c.want == nil {
				want = "READY"
			}
			if status != want {
				t.Fatalf("status = %s, want %s", status, want)
			}
		})
	}

	t.Run("force keeps finalized architecture", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewAgentVersionRepository(db)
		agentID, vid := seedVersionWithStatus(t, db, types.AgentStatusPending)
		const frozen = `{"tools": [], "frozen": true}`
		if _, err := db.Exec(`UPDATE agents SET architecture = $2::jsonb, finalized_at = now() WHERE id = $1::uuid`, agentID, frozen); err != nil {
			t.Fatal(err)
		}
		before := readArchitecture(t, db, agentID)
		if err := repo.LandBundle(context.Background(), vid, []byte(immutabilityTestBundle), true); err != nil {
			t.Fatalf("LandBundle: %v", err)
		}
		if after := readArchitecture(t, db, agentID); after != before {
			t.Fatalf("architecture rewritten: before %s, after %s", before, after)
		}
	})
}

func TestUpdateFlowMapConfig_RootInitializingOnly(t *testing.T) {
	ctx := context.Background()
	cfg := []byte(`{"edited": true}`)

	t.Run("READY root locked", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewAgentVersionRepository(db)
		agentID, vid := seedVersionWithStatus(t, db, types.AgentStatusReady)
		if err := repo.UpdateFlowMapConfig(ctx, vid, cfg); !errors.Is(err, types.ErrVersionLocked) {
			t.Fatalf("UpdateFlowMapConfig = %v, want ErrVersionLocked", err)
		}
		if err := repo.UpdateFlowMapConfigForAgent(ctx, agentID, cfg); !errors.Is(err, types.ErrVersionLocked) {
			t.Fatalf("UpdateFlowMapConfigForAgent = %v, want ErrVersionLocked", err)
		}
		got, _, err := repo.GetFlowMapConfig(ctx, vid)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `{}` {
			t.Fatalf("config changed: %s", got)
		}
	})

	t.Run("DRAFT non-root locked", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewAgentVersionRepository(db)
		_, root := seedAgent(t, db)
		child, err := repo.CreateVersionFromPrompts(ctx, root, []byte(`{}`), types.AgentStatusDraft)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.UpdateFlowMapConfig(ctx, child, cfg); !errors.Is(err, types.ErrVersionLocked) {
			t.Fatalf("UpdateFlowMapConfig = %v, want ErrVersionLocked", err)
		}
	})

	t.Run("INITIALIZING root writable", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewAgentVersionRepository(db)
		agentID, vid := seedAgent(t, db)
		if err := repo.UpdateFlowMapConfig(ctx, vid, cfg); err != nil {
			t.Fatalf("UpdateFlowMapConfig: %v", err)
		}
		if err := repo.UpdateFlowMapConfigForAgent(ctx, agentID, []byte(`{"again": true}`)); err != nil {
			t.Fatalf("UpdateFlowMapConfigForAgent: %v", err)
		}
		got, _, err := repo.GetFlowMapConfig(ctx, vid)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `{"again": true}` {
			t.Fatalf("config = %s", got)
		}
	})

	t.Run("missing", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewAgentVersionRepository(db)
		const nope = "00000000-0000-0000-0000-000000000000"
		if err := repo.UpdateFlowMapConfig(ctx, nope, cfg); !errors.Is(err, types.ErrNotFound) {
			t.Fatalf("UpdateFlowMapConfig = %v, want ErrNotFound", err)
		}
		if err := repo.UpdateFlowMapConfigForAgent(ctx, nope, cfg); !errors.Is(err, types.ErrNotFound) {
			t.Fatalf("UpdateFlowMapConfigForAgent = %v, want ErrNotFound", err)
		}
	})
}

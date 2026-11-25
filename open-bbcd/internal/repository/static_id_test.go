package repository

import (
	"context"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestStaticID_DBCheckRejectsUUIDShape(t *testing.T) {
	agentRepo, _, db := withRepo(t)
	ctx := context.Background()
	a, _, err := agentRepo.CreateFromWizard(ctx, types.CreateAgentFromWizardOpts{Name: "sid-check"})
	if err != nil {
		t.Fatalf("CreateFromWizard: %v", err)
	}
	_, err = db.ExecContext(ctx, `UPDATE agents SET static_id='0123456789abcdef0123456789abcdef' WHERE id=$1`, a.ID)
	if err == nil {
		t.Fatal("expected check violation, got nil")
	}
	if _, err := db.ExecContext(ctx, `UPDATE agents SET static_id='sales' WHERE id=$1`, a.ID); err != nil {
		t.Fatalf("valid static_id rejected: %v", err)
	}
}

func TestStaticID_RoundTripsInReads(t *testing.T) {
	agentRepo, versionRepo, db := withRepo(t)
	ctx := context.Background()
	a, v, err := agentRepo.CreateFromWizard(ctx, types.CreateAgentFromWizardOpts{Name: "sid-rt"})
	if err != nil {
		t.Fatalf("CreateFromWizard: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE agents SET static_id='sid-rt' WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE agent_versions SET static_id='sid-rt_v1' WHERE id=$1`, v.ID); err != nil {
		t.Fatal(err)
	}
	eq := func(what string, p *string, want string) {
		t.Helper()
		if p == nil || *p != want {
			t.Errorf("%s static_id = %v, want %q", what, p, want)
		}
	}

	gv, err := versionRepo.GetByID(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	eq("GetByID", gv.StaticID, "sid-rt_v1")

	wv, wa, err := versionRepo.GetWithAgent(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	eq("GetWithAgent version", wv.StaticID, "sid-rt_v1")
	eq("GetWithAgent agent", wa.StaticID, "sid-rt")

	groups, err := agentRepo.ListGrouped(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Versions) != 1 {
		t.Fatalf("unexpected groups: %+v", groups)
	}
	eq("ListGrouped version", groups[0].Versions[0].Version.StaticID, "sid-rt_v1")

	ga, err := agentRepo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	eq("agent GetByID", ga.StaticID, "sid-rt")

	list, err := agentRepo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("List len = %d", len(list))
	}
	eq("agent List", list[0].StaticID, "sid-rt")
}

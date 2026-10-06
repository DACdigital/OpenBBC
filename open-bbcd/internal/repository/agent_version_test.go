// open-bbcd/internal/repository/agent_version_test.go
package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

// seedReadyAgentVersion creates an agent + a READY version.
// Returns (agentID, versionID) plus the repos and db handle from withRepo.
// Callers MUST reuse these returns instead of calling withRepo again (which
// would truncate the just-seeded rows).
func seedReadyAgentVersion(t *testing.T) (agentID, versionID string, agentRepo *AgentRepository, versionRepo *AgentVersionRepository, db *sql.DB) {
	t.Helper()
	agentRepo, versionRepo, db = withRepo(t)
	ctx := context.Background()
	agent, version, err := agentRepo.CreateFromWizard(ctx, types.CreateAgentFromWizardOpts{
		Name: "av-" + uuid.NewString()[:8],
	})
	if err != nil {
		t.Fatalf("CreateFromWizard: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE agent_versions SET status='READY' WHERE id=$1`, version.ID,
	); err != nil {
		t.Fatalf("seed READY: %v", err)
	}
	return agent.ID, version.ID, agentRepo, versionRepo, db
}

// insertReadyChildVersion inserts a READY child version under parentID.
func insertReadyChildVersion(t *testing.T, db *sql.DB, parentID string) string {
	t.Helper()
	var agentID string
	if err := db.QueryRow(`SELECT agent_id::text FROM agent_versions WHERE id=$1`, parentID).Scan(&agentID); err != nil {
		t.Fatalf("lookup agent: %v", err)
	}
	id := uuid.NewString()
	if _, err := db.Exec(`
		INSERT INTO agent_versions (id, agent_id, parent_version_id, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'READY')
	`, id, agentID, parentID); err != nil {
		t.Fatalf("insert child: %v", err)
	}
	return id
}

func TestAgentVersionRepository_GetByID(t *testing.T) {
	_, versionID, _, versionRepo, _ := seedReadyAgentVersion(t)
	v, err := versionRepo.GetByID(context.Background(), versionID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if v.ID != versionID {
		t.Fatalf("ID mismatch")
	}
	if v.Status != "READY" {
		t.Fatalf("status=%q", v.Status)
	}
}

func TestAgentVersionRepository_GetWithAgent(t *testing.T) {
	agentID, versionID, _, versionRepo, _ := seedReadyAgentVersion(t)
	v, a, err := versionRepo.GetWithAgent(context.Background(), versionID)
	if err != nil {
		t.Fatalf("GetWithAgent: %v", err)
	}
	if v.AgentID != agentID || a.ID != agentID {
		t.Fatalf("mismatch v=%q a=%q want %q", v.AgentID, a.ID, agentID)
	}
}

func TestAgentVersionRepository_Deploy_HappyPath(t *testing.T) {
	_, versionID, _, versionRepo, _ := seedReadyAgentVersion(t)
	ctx := context.Background()
	prev, err := versionRepo.Deploy(ctx, versionID)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if prev != nil {
		t.Fatalf("prev should be nil, got %q", *prev)
	}
	v, _ := versionRepo.GetByID(ctx, versionID)
	if v.Status != "DEPLOYED" {
		t.Fatalf("status=%q", v.Status)
	}
}

func TestAgentVersionRepository_Deploy_Rotates(t *testing.T) {
	_, versionID, _, versionRepo, db := seedReadyAgentVersion(t)
	ctx := context.Background()
	_, _ = versionRepo.Deploy(ctx, versionID)
	child := insertReadyChildVersion(t, db, versionID)
	prev, err := versionRepo.Deploy(ctx, child)
	if err != nil {
		t.Fatalf("Deploy child: %v", err)
	}
	if prev == nil || *prev != versionID {
		t.Fatalf("prev=%v want %q", prev, versionID)
	}
	rootAfter, _ := versionRepo.GetByID(ctx, versionID)
	childAfter, _ := versionRepo.GetByID(ctx, child)
	if rootAfter.Status != "READY" {
		t.Fatalf("root status=%q", rootAfter.Status)
	}
	if childAfter.Status != "DEPLOYED" {
		t.Fatalf("child status=%q", childAfter.Status)
	}
}

func TestAgentVersionRepository_Deploy_NotDeployable(t *testing.T) {
	// Create an agent + INITIALIZING version (Status not READY).
	agentRepo, versionRepo, _ := withRepo(t)
	ctx := context.Background()
	_, version, err := agentRepo.CreateFromWizard(ctx, types.CreateAgentFromWizardOpts{Name: "nd-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err = versionRepo.Deploy(ctx, version.ID)
	if !errors.Is(err, types.ErrAgentNotDeployable) {
		t.Fatalf("got %v want ErrAgentNotDeployable", err)
	}
}

func TestAgentVersionRepository_Deploy_Idempotent(t *testing.T) {
	_, versionID, _, versionRepo, _ := seedReadyAgentVersion(t)
	ctx := context.Background()
	_, _ = versionRepo.Deploy(ctx, versionID)
	prev, err := versionRepo.Deploy(ctx, versionID)
	if err != nil {
		t.Fatalf("re-Deploy: %v", err)
	}
	if prev != nil {
		t.Fatalf("prev should be nil, got %q", *prev)
	}
	v, _ := versionRepo.GetByID(ctx, versionID)
	if v.Status != "DEPLOYED" {
		t.Fatalf("status=%q", v.Status)
	}
}

func TestAgentVersionRepository_Undeploy(t *testing.T) {
	_, versionID, _, versionRepo, _ := seedReadyAgentVersion(t)
	ctx := context.Background()
	_, _ = versionRepo.Deploy(ctx, versionID)
	if err := versionRepo.Undeploy(ctx, versionID); err != nil {
		t.Fatalf("Undeploy: %v", err)
	}
	v, _ := versionRepo.GetByID(ctx, versionID)
	if v.Status != "READY" {
		t.Fatalf("status=%q", v.Status)
	}
	if err := versionRepo.Undeploy(ctx, versionID); !errors.Is(err, types.ErrAgentNotDeployed) {
		t.Fatalf("re-Undeploy: got %v want ErrAgentNotDeployed", err)
	}
}

func TestAgentVersionRepository_CurrentDeployedID(t *testing.T) {
	agentID, versionID, _, versionRepo, _ := seedReadyAgentVersion(t)
	ctx := context.Background()
	id, err := versionRepo.CurrentDeployedID(ctx, agentID)
	if err != nil {
		t.Fatalf("CurrentDeployedID empty: %v", err)
	}
	if id != "" {
		t.Fatalf("empty expected, got %q", id)
	}
	_, _ = versionRepo.Deploy(ctx, versionID)
	id, _ = versionRepo.CurrentDeployedID(ctx, agentID)
	if id != versionID {
		t.Fatalf("got %q want %q", id, versionID)
	}
}

func TestAgentVersionRepository_PartialUniqueIndex_RejectsDoubleDeploy(t *testing.T) {
	_, _, db := withRepo(t)
	ctx := context.Background()
	agentID := uuid.NewString()
	_, err := db.ExecContext(ctx, `INSERT INTO agents (id, name) VALUES ($1::uuid, $2)`, agentID, "dbl-test")
	if err != nil {
		t.Fatalf("agent insert: %v", err)
	}
	id1 := uuid.NewString()
	_, err = db.ExecContext(ctx,
		`INSERT INTO agent_versions (id, agent_id, status) VALUES ($1::uuid, $2::uuid, 'DEPLOYED')`,
		id1, agentID,
	)
	if err != nil {
		t.Fatalf("first deploy insert: %v", err)
	}
	id2 := uuid.NewString()
	_, err = db.ExecContext(ctx,
		`INSERT INTO agent_versions (id, agent_id, parent_version_id, status) VALUES ($1::uuid, $2::uuid, $3::uuid, 'DEPLOYED')`,
		id2, agentID, id1,
	)
	if err == nil {
		t.Fatalf("expected partial unique index violation")
	}
}

func TestAgentVersionRepository_List_FiltersByStatus(t *testing.T) {
	agentRepo, versionRepo, db := withRepo(t)
	ctx := context.Background()

	// Seed 2 PENDING + 1 INITIALIZING + 1 READY across 4 separate agents.
	// Each CreateFromWizard produces an INITIALIZING root; UPDATE promotes
	// those that should be PENDING or READY.
	seed := func(name, status string) string {
		_, v, err := agentRepo.CreateFromWizard(ctx, types.CreateAgentFromWizardOpts{Name: name})
		if err != nil {
			t.Fatalf("CreateFromWizard(%s): %v", name, err)
		}
		if status != "INITIALIZING" {
			if _, err := db.ExecContext(ctx,
				`UPDATE agent_versions SET status=$2 WHERE id=$1`, v.ID, status,
			); err != nil {
				t.Fatalf("promote(%s → %s): %v", name, status, err)
			}
		}
		return v.ID
	}
	p1 := seed("list-p1-"+uuid.NewString()[:8], "PENDING")
	p2 := seed("list-p2-"+uuid.NewString()[:8], "PENDING")
	_ = seed("list-i-"+uuid.NewString()[:8], "INITIALIZING")
	_ = seed("list-r-"+uuid.NewString()[:8], "READY")

	got, err := versionRepo.List(ctx, "PENDING", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d want 2 (%v)", len(got), got)
	}
	ids := map[string]bool{got[0].ID: true, got[1].ID: true}
	if !ids[p1] || !ids[p2] {
		t.Errorf("returned ids=%v, want %s + %s", ids, p1, p2)
	}
	for _, v := range got {
		if v.Status != "PENDING" {
			t.Errorf("row status=%q, want PENDING", v.Status)
		}
	}

	// No filter: at least the 4 rows seeded above are returned. Order is
	// created_at DESC.
	all, err := versionRepo.List(ctx, "", 0)
	if err != nil {
		t.Fatalf("List no-filter: %v", err)
	}
	if len(all) < 4 {
		t.Fatalf("no-filter len=%d, want >= 4", len(all))
	}
}

// bindingRow is one agent_version_subagent row without its caller.
type bindingRow struct{ name, target, note string }

// readBindings returns the caller's bindings ordered by name.
func readBindings(t *testing.T, db *sql.DB, callerID string) []bindingRow {
	t.Helper()
	rows, err := db.Query(`
		SELECT name, target_version_id::text, note FROM agent_version_subagent
		WHERE caller_version_id = $1::uuid ORDER BY name`, callerID)
	if err != nil {
		t.Fatalf("readBindings: %v", err)
	}
	defer rows.Close()
	var out []bindingRow
	for rows.Next() {
		var b bindingRow
		if err := rows.Scan(&b.name, &b.target, &b.note); err != nil {
			t.Fatalf("readBindings scan: %v", err)
		}
		out = append(out, b)
	}
	return out
}

// seedForkParent turns parentID into a version with the agent tool on and two
// raw bindings (researcher, writer) to distinct READY targets with notes n1, n2.
// Returns the expected binding set.
func seedForkParent(t *testing.T, db *sql.DB, parentID string) []bindingRow {
	t.Helper()
	r1 := seedVersionStatusOnly(t, db, types.AgentStatusReady)
	r2 := seedVersionStatusOnly(t, db, types.AgentStatusReady)
	if _, err := db.Exec(`UPDATE agent_versions SET agent_tool_enabled = true WHERE id = $1::uuid`, parentID); err != nil {
		t.Fatalf("enable tool: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO agent_version_subagent (caller_version_id, target_version_id, name, note)
		VALUES ($1::uuid, $2::uuid, 'researcher', 'n1'), ($1::uuid, $3::uuid, 'writer', 'n2')`,
		parentID, r1, r2); err != nil {
		t.Fatalf("seed bindings: %v", err)
	}
	return []bindingRow{{"researcher", r1, "n1"}, {"writer", r2, "n2"}}
}

// assertForkCopied checks the new version carries the parent's flag and
// binding set, and that the parent's rows are untouched.
func assertForkCopied(t *testing.T, db *sql.DB, parentID, newID string, wantOn bool, want []bindingRow) {
	t.Helper()
	if got := toolEnabled(t, db, newID); got != wantOn {
		t.Fatalf("new agent_tool_enabled = %v, want %v", got, wantOn)
	}
	if got := toolEnabled(t, db, parentID); got != wantOn {
		t.Fatalf("parent agent_tool_enabled = %v, want %v", got, wantOn)
	}
	for _, id := range []string{newID, parentID} {
		got := readBindings(t, db, id)
		if len(got) != len(want) {
			t.Fatalf("bindings of %s = %v, want %v", id, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("bindings of %s = %v, want %v", id, got, want)
			}
		}
	}
}

func TestCreateVersionFromPrompts_CopiesAgentToolConfig(t *testing.T) {
	for _, st := range []types.AgentStatus{types.AgentStatusDraft, types.AgentStatusReady} {
		t.Run(string(st), func(t *testing.T) {
			db := openTestDB(t)
			vrepo := NewAgentVersionRepository(db)
			ctx := context.Background()
			parent := seedVersionStatusOnly(t, db, types.AgentStatusReady)
			want := seedForkParent(t, db, parent)

			newID, err := vrepo.CreateVersionFromPrompts(ctx, parent, []byte(`{}`), st)
			if err != nil {
				t.Fatalf("CreateVersionFromPrompts: %v", err)
			}
			assertForkCopied(t, db, parent, newID, true, want)
		})
	}
}

func TestCreateVersionFromPrompts_ToolOffForksOff(t *testing.T) {
	db := openTestDB(t)
	vrepo := NewAgentVersionRepository(db)
	parent := seedVersionStatusOnly(t, db, types.AgentStatusReady)
	newID, err := vrepo.CreateVersionFromPrompts(context.Background(), parent, []byte(`{}`), types.AgentStatusDraft)
	if err != nil {
		t.Fatalf("CreateVersionFromPrompts: %v", err)
	}
	assertForkCopied(t, db, parent, newID, false, nil)
}

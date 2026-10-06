package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// seedVersionWithStatus seeds a fresh agent + version and forces the version
// into status (raw UPDATE, bypassing lifecycle transitions).
func seedVersionWithStatus(t *testing.T, db *sql.DB, status types.AgentStatus) (agentID, versionID string) {
	t.Helper()
	agentID, versionID = seedAgent(t, db)
	if _, err := db.Exec(`UPDATE agent_versions SET status = $2 WHERE id = $1::uuid`, versionID, string(status)); err != nil {
		t.Fatalf("seedVersionWithStatus: %v", err)
	}
	return agentID, versionID
}

// seedArchitecture overwrites the agent's architecture blob.
func seedArchitecture(t *testing.T, db *sql.DB, agentID, arch string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE agents SET architecture = $2::jsonb WHERE id = $1::uuid`, agentID, arch); err != nil {
		t.Fatalf("seedArchitecture: %v", err)
	}
}

// rawBind inserts a binding directly, bypassing every repository invariant.
func rawBind(t *testing.T, db *sql.DB, callerID, name, targetID string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO agent_version_subagent (caller_version_id, target_version_id, name)
		VALUES ($1::uuid, $2::uuid, $3)`, callerID, targetID, name); err != nil {
		t.Fatalf("rawBind: %v", err)
	}
}

func countBindings(t *testing.T, db *sql.DB, callerID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM agent_version_subagent WHERE caller_version_id = $1::uuid`, callerID).Scan(&n); err != nil {
		t.Fatalf("countBindings: %v", err)
	}
	return n
}

func toolEnabled(t *testing.T, db *sql.DB, versionID string) bool {
	t.Helper()
	var on bool
	if err := db.QueryRow(`SELECT agent_tool_enabled FROM agent_versions WHERE id = $1::uuid`, versionID).Scan(&on); err != nil {
		t.Fatalf("toolEnabled: %v", err)
	}
	return on
}

// allWrites runs the four config writes against callerID and returns their
// errors in order: SetAgentToolEnabled, AddBinding, UpdateNotes, DeleteBinding.
// DeleteBinding targets "existing" so a not-found never masks the guard.
func allWrites(ctx context.Context, r *SubAgentRepository, callerID, targetID string) []error {
	return []error{
		r.SetAgentToolEnabled(ctx, callerID, true),
		r.AddBinding(ctx, callerID, "newone", targetID, ""),
		r.UpdateNotes(ctx, callerID, map[string]string{"existing": "changed"}),
		r.DeleteBinding(ctx, callerID, "existing"),
	}
}

func TestSubAgent_DraftHappyPath(t *testing.T) {
	db := openTestDB(t)
	r := NewSubAgentRepository(db)
	ctx := context.Background()
	_, caller := seedVersionWithStatus(t, db, types.AgentStatusDraft)
	_, target := seedVersionWithStatus(t, db, types.AgentStatusReady)

	if err := r.SetAgentToolEnabled(ctx, caller, true); err != nil {
		t.Fatalf("SetAgentToolEnabled: %v", err)
	}
	v, err := NewAgentVersionRepository(db).GetByID(ctx, caller)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !v.AgentToolEnabled {
		t.Fatal("GetByID: AgentToolEnabled = false, want true")
	}
	v, _, err = NewAgentVersionRepository(db).GetWithAgent(ctx, caller)
	if err != nil {
		t.Fatalf("GetWithAgent: %v", err)
	}
	if !v.AgentToolEnabled {
		t.Fatal("GetWithAgent: AgentToolEnabled = false, want true")
	}

	if err := r.AddBinding(ctx, caller, "researcher", target, "finds facts"); err != nil {
		t.Fatalf("AddBinding: %v", err)
	}
	bs, err := r.ListBindings(ctx, caller)
	if err != nil {
		t.Fatalf("ListBindings: %v", err)
	}
	if len(bs) != 1 || bs[0].Name != "researcher" || bs[0].TargetVersionID != target ||
		bs[0].Note != "finds facts" || bs[0].CallerVersionID != caller {
		t.Fatalf("ListBindings = %+v", bs)
	}
	before := bs[0].UpdatedAt

	enabled, cfg, err := r.GetAgentToolConfig(ctx, caller)
	if err != nil || !enabled || len(cfg) != 1 {
		t.Fatalf("GetAgentToolConfig = %v, %+v, %v", enabled, cfg, err)
	}

	time.Sleep(5 * time.Millisecond)
	if err := r.UpdateNotes(ctx, caller, map[string]string{"researcher": "new", "ghost": "x"}); err != nil {
		t.Fatalf("UpdateNotes: %v", err)
	}
	bs, _ = r.ListBindings(ctx, caller)
	if len(bs) != 1 || bs[0].Note != "new" {
		t.Fatalf("after UpdateNotes = %+v", bs)
	}
	if !bs[0].UpdatedAt.After(before) {
		t.Fatalf("updated_at not advanced: %v -> %v", before, bs[0].UpdatedAt)
	}

	if err := r.DeleteBinding(ctx, caller, "researcher"); err != nil {
		t.Fatalf("DeleteBinding: %v", err)
	}
	if err := r.DeleteBinding(ctx, caller, "researcher"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("second DeleteBinding = %v, want ErrNotFound", err)
	}
	bs, err = r.ListBindings(ctx, caller)
	if err != nil || bs == nil || len(bs) != 0 {
		t.Fatalf("ListBindings empty = %#v, %v (want non-nil empty)", bs, err)
	}
}

func TestSubAgent_MissingVersions(t *testing.T) {
	db := openTestDB(t)
	r := NewSubAgentRepository(db)
	ctx := context.Background()
	const ghost = "00000000-0000-0000-0000-000000000001"
	_, caller := seedVersionWithStatus(t, db, types.AgentStatusDraft)

	if err := r.SetAgentToolEnabled(ctx, ghost, true); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("SetAgentToolEnabled(ghost) = %v", err)
	}
	if err := r.AddBinding(ctx, caller, "x", ghost, ""); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("AddBinding(ghost target) = %v", err)
	}
	if _, _, err := r.GetAgentToolConfig(ctx, ghost); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("GetAgentToolConfig(ghost) = %v", err)
	}
	if err := r.ConfigWriteBlock(ctx, ghost); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("ConfigWriteBlock(ghost) = %v", err)
	}
}

func TestSubAgent_InitializingEditable(t *testing.T) {
	db := openTestDB(t)
	r := NewSubAgentRepository(db)
	ctx := context.Background()
	_, caller := seedVersionWithStatus(t, db, types.AgentStatusInitializing)
	_, target := seedVersionWithStatus(t, db, types.AgentStatusReady)
	rawBind(t, db, caller, "existing", seedVersionStatusOnly(t, db, types.AgentStatusReady))

	for i, err := range allWrites(ctx, r, caller, target) {
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := r.ConfigWriteBlock(ctx, caller); err != nil {
		t.Fatalf("ConfigWriteBlock = %v, want nil", err)
	}
}

// seedVersionStatusOnly is seedVersionWithStatus returning only the version id.
func seedVersionStatusOnly(t *testing.T, db *sql.DB, status types.AgentStatus) string {
	t.Helper()
	_, v := seedVersionWithStatus(t, db, status)
	return v
}

func TestSubAgent_LockedCaller(t *testing.T) {
	for _, st := range []types.AgentStatus{
		types.AgentStatusPending, types.AgentStatusReady, types.AgentStatusDeployed, types.AgentStatusTraining,
	} {
		t.Run(string(st), func(t *testing.T) {
			db := openTestDB(t)
			r := NewSubAgentRepository(db)
			ctx := context.Background()
			_, caller := seedVersionWithStatus(t, db, types.AgentStatusDraft)
			_, target := seedVersionWithStatus(t, db, types.AgentStatusReady)
			rawBind(t, db, caller, "existing", seedVersionStatusOnly(t, db, types.AgentStatusReady))
			if _, err := db.Exec(`UPDATE agent_versions SET status=$2 WHERE id=$1::uuid`, caller, string(st)); err != nil {
				t.Fatal(err)
			}

			for i, err := range allWrites(ctx, r, caller, target) {
				if !errors.Is(err, types.ErrVersionLocked) {
					t.Fatalf("write %d = %v, want ErrVersionLocked", i, err)
				}
			}
			if toolEnabled(t, db, caller) {
				t.Fatal("agent_tool_enabled changed")
			}
			bs, _ := r.ListBindings(ctx, caller)
			if len(bs) != 1 || bs[0].Name != "existing" || bs[0].Note != "" {
				t.Fatalf("bindings changed: %+v", bs)
			}
			if err := r.ConfigWriteBlock(ctx, caller); !errors.Is(err, types.ErrVersionLocked) {
				t.Fatalf("ConfigWriteBlock = %v", err)
			}
		})
	}
}

func TestSubAgent_ActiveEvalOrTraining(t *testing.T) {
	t.Run("eval", func(t *testing.T) {
		db := openTestDB(t)
		r := NewSubAgentRepository(db)
		ctx := context.Background()
		_, caller := seedVersionWithStatus(t, db, types.AgentStatusDraft)
		_, target := seedVersionWithStatus(t, db, types.AgentStatusReady)
		rawBind(t, db, caller, "existing", seedVersionStatusOnly(t, db, types.AgentStatusReady))
		dv := seedDatasetVersion(t, db)
		var evalID string
		if err := db.QueryRow(`INSERT INTO evals (agent_version_id, dataset_version_id, status)
			VALUES ($1::uuid, $2::uuid, 'PENDING') RETURNING id::text`, caller, dv).Scan(&evalID); err != nil {
			t.Fatal(err)
		}
		assertActiveBlocks(t, ctx, r, db, caller, target, `UPDATE evals SET status=$2 WHERE id=$1::uuid`, evalID)
	})
	t.Run("training", func(t *testing.T) {
		db := openTestDB(t)
		r := NewSubAgentRepository(db)
		ctx := context.Background()
		_, caller := seedVersionWithStatus(t, db, types.AgentStatusDraft)
		_, target := seedVersionWithStatus(t, db, types.AgentStatusReady)
		rawBind(t, db, caller, "existing", seedVersionStatusOnly(t, db, types.AgentStatusReady))
		dv := seedDatasetVersion(t, db)
		var evalID, tsID string
		if err := db.QueryRow(`INSERT INTO evals (agent_version_id, dataset_version_id, status)
			VALUES ($1::uuid, $2::uuid, 'DONE') RETURNING id::text`, caller, dv).Scan(&evalID); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`INSERT INTO training_sessions (source_eval_id, parent_version_id, status)
			VALUES ($1::uuid, $2::uuid, 'PENDING') RETURNING id::text`, evalID, caller).Scan(&tsID); err != nil {
			t.Fatal(err)
		}
		assertActiveBlocks(t, ctx, r, db, caller, target, `UPDATE training_sessions SET status=$2 WHERE id=$1::uuid`, tsID)
	})
}

// assertActiveBlocks checks that the work row (starting PENDING) blocks every
// write, still blocks once IN_PROGRESS, and stops blocking once DONE.
func assertActiveBlocks(t *testing.T, ctx context.Context, r *SubAgentRepository, db *sql.DB, caller, target, flip, rowID string) {
	t.Helper()
	for _, st := range []string{"PENDING", "IN_PROGRESS"} {
		if _, err := db.Exec(flip, rowID, st); err != nil {
			t.Fatal(err)
		}
		for i, err := range allWrites(ctx, r, caller, target) {
			if !errors.Is(err, types.ErrEvalOrTrainingActive) {
				t.Fatalf("%s write %d = %v, want ErrEvalOrTrainingActive", st, i, err)
			}
		}
		if err := r.ConfigWriteBlock(ctx, caller); !errors.Is(err, types.ErrEvalOrTrainingActive) {
			t.Fatalf("%s ConfigWriteBlock = %v", st, err)
		}
		if toolEnabled(t, db, caller) || countBindings(t, db, caller) != 1 {
			t.Fatalf("%s: DB changed", st)
		}
	}
	if _, err := db.Exec(flip, rowID, "DONE"); err != nil {
		t.Fatal(err)
	}
	for i, err := range allWrites(ctx, r, caller, target) {
		if err != nil {
			t.Fatalf("DONE write %d = %v", i, err)
		}
	}
}

func TestSubAgent_UnrunnableTarget(t *testing.T) {
	db := openTestDB(t)
	r := NewSubAgentRepository(db)
	ctx := context.Background()
	_, caller := seedVersionWithStatus(t, db, types.AgentStatusDraft)
	for _, st := range []types.AgentStatus{
		types.AgentStatusInitializing, types.AgentStatusPending, types.AgentStatusDraft, types.AgentStatusTraining,
	} {
		target := seedVersionStatusOnly(t, db, st)
		if err := r.AddBinding(ctx, caller, "sub", target, ""); !errors.Is(err, types.ErrTargetNotRunnable) {
			t.Fatalf("target %s: %v, want ErrTargetNotRunnable", st, err)
		}
	}
	if n := countBindings(t, db, caller); n != 0 {
		t.Fatalf("rows written: %d", n)
	}
	target := seedVersionStatusOnly(t, db, types.AgentStatusDeployed)
	if err := r.AddBinding(ctx, caller, "sub", target, ""); err != nil {
		t.Fatalf("DEPLOYED target: %v", err)
	}
}

func TestSubAgent_Duplicates(t *testing.T) {
	db := openTestDB(t)
	r := NewSubAgentRepository(db)
	ctx := context.Background()
	_, caller := seedVersionWithStatus(t, db, types.AgentStatusDraft)
	t1 := seedVersionStatusOnly(t, db, types.AgentStatusReady)
	t2 := seedVersionStatusOnly(t, db, types.AgentStatusReady)
	if err := r.AddBinding(ctx, caller, "a", t1, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.AddBinding(ctx, caller, "a", t2, ""); !errors.Is(err, types.ErrBindingConflict) {
		t.Fatalf("dup name = %v", err)
	}
	if err := r.AddBinding(ctx, caller, "b", t1, ""); !errors.Is(err, types.ErrBindingConflict) {
		t.Fatalf("dup target = %v", err)
	}
	if n := countBindings(t, db, caller); n != 1 {
		t.Fatalf("rows = %d", n)
	}
}

func TestSubAgent_Cycle(t *testing.T) {
	t.Run("self", func(t *testing.T) {
		db := openTestDB(t)
		r := NewSubAgentRepository(db)
		_, a := seedVersionWithStatus(t, db, types.AgentStatusDraft)
		if err := r.AddBinding(context.Background(), a, "me", a, ""); !errors.Is(err, types.ErrTopologyCycle) {
			t.Fatalf("self = %v", err)
		}
	})
	t.Run("direct", func(t *testing.T) {
		db := openTestDB(t)
		r := NewSubAgentRepository(db)
		_, a := seedVersionWithStatus(t, db, types.AgentStatusDraft)
		b := seedVersionStatusOnly(t, db, types.AgentStatusReady)
		rawBind(t, db, b, "back", a)
		if err := r.AddBinding(context.Background(), a, "b", b, ""); !errors.Is(err, types.ErrTopologyCycle) {
			t.Fatalf("direct = %v", err)
		}
		if n := countBindings(t, db, a); n != 0 {
			t.Fatalf("rows = %d", n)
		}
	})
	t.Run("transitive", func(t *testing.T) {
		db := openTestDB(t)
		r := NewSubAgentRepository(db)
		_, a := seedVersionWithStatus(t, db, types.AgentStatusDraft)
		b := seedVersionStatusOnly(t, db, types.AgentStatusReady)
		c := seedVersionStatusOnly(t, db, types.AgentStatusReady)
		rawBind(t, db, b, "c", c)
		rawBind(t, db, c, "a", a)
		if err := r.AddBinding(context.Background(), a, "b", b, ""); !errors.Is(err, types.ErrTopologyCycle) {
			t.Fatalf("transitive = %v", err)
		}
		if n := countBindings(t, db, a); n != 0 {
			t.Fatalf("rows = %d", n)
		}
	})
}

func TestSubAgent_ToolNameCollision(t *testing.T) {
	db := openTestDB(t)
	r := NewSubAgentRepository(db)
	ctx := context.Background()
	agentID, caller := seedVersionWithStatus(t, db, types.AgentStatusDraft)
	seedArchitecture(t, db, agentID, `{"tools":[{"id":"t1","name":"agent"}]}`)
	target := seedVersionStatusOnly(t, db, types.AgentStatusReady)

	if err := r.SetAgentToolEnabled(ctx, caller, true); !errors.Is(err, types.ErrToolNameCollision) {
		t.Fatalf("enable = %v", err)
	}
	if err := r.AddBinding(ctx, caller, "sub", target, ""); !errors.Is(err, types.ErrToolNameCollision) {
		t.Fatalf("add = %v", err)
	}
	if err := r.SetAgentToolEnabled(ctx, caller, false); err != nil {
		t.Fatalf("disable = %v", err)
	}
	if toolEnabled(t, db, caller) || countBindings(t, db, caller) != 0 {
		t.Fatal("DB changed")
	}
}

func TestSubAgent_InvalidName(t *testing.T) {
	db := openTestDB(t)
	r := NewSubAgentRepository(db)
	ctx := context.Background()
	_, caller := seedVersionWithStatus(t, db, types.AgentStatusDraft)
	target := seedVersionStatusOnly(t, db, types.AgentStatusReady)
	for _, name := range []string{"", "Bad Name", "a" + strings.Repeat("b", 40), "1abc"} {
		if err := r.AddBinding(ctx, caller, name, target, ""); !errors.Is(err, types.ErrNameRequired) {
			t.Fatalf("name %q = %v, want ErrNameRequired", name, err)
		}
	}
	if n := countBindings(t, db, caller); n != 0 {
		t.Fatalf("rows = %d", n)
	}
	// 40 chars is the maximum and is accepted.
	if err := r.AddBinding(ctx, caller, "a"+strings.Repeat("b", 39), target, ""); err != nil {
		t.Fatalf("40-char name: %v", err)
	}
}

// TestSubAgent_BindRacesFinalize runs AddBinding on one connection against
// Finalize's INITIALIZING→PENDING update on another. The caller-row FOR
// NO KEY UPDATE lock must serialise them: either the bind commits first (row exists,
// status then moves to PENDING) or Finalize commits first and the bind sees
// PENDING and is refused with no row written.
func TestSubAgent_BindRacesFinalize(t *testing.T) {
	db := openTestDB(t)
	r := NewSubAgentRepository(db)
	ctx := context.Background()
	var bound, refused int
	for i := 0; i < 20; i++ {
		_, caller := seedVersionWithStatus(t, db, types.AgentStatusInitializing)
		target := seedVersionStatusOnly(t, db, types.AgentStatusReady)

		var start, done sync.WaitGroup
		start.Add(1)
		done.Add(2)
		var addErr, finErr error
		go func() {
			defer done.Done()
			start.Wait()
			addErr = r.AddBinding(ctx, caller, "sub", target, "")
		}()
		go func() {
			defer done.Done()
			start.Wait()
			_, finErr = db.ExecContext(ctx,
				`UPDATE agent_versions SET status='PENDING' WHERE id=$1::uuid AND status='INITIALIZING'`, caller)
		}()
		start.Done()
		done.Wait()

		if finErr != nil {
			t.Fatalf("iter %d finalize: %v", i, finErr)
		}
		var status string
		if err := db.QueryRow(`SELECT status FROM agent_versions WHERE id=$1::uuid`, caller).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "PENDING" {
			t.Fatalf("iter %d: status %s, want PENDING", i, status)
		}
		n := countBindings(t, db, caller)
		switch {
		case addErr == nil && n == 1:
			bound++
		case errors.Is(addErr, types.ErrVersionLocked) && n == 0:
			refused++
		default:
			t.Fatalf("iter %d: addErr=%v rows=%d", i, addErr, n)
		}
	}
	t.Logf("bind-first=%d finalize-first=%d", bound, refused)
}

// TestSubAgent_LockDoesNotBlockFKInserts proves the config lock is FOR NO KEY
// UPDATE, not FOR UPDATE: while a tx holds lockVersions on caller and target,
// an FK insert referencing the target (a new chat session — the same
// FOR KEY SHARE live traffic takes) on another connection must not block.
func TestSubAgent_LockDoesNotBlockFKInserts(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	_, caller := seedVersionWithStatus(t, db, types.AgentStatusDraft)
	target := seedVersionStatusOnly(t, db, types.AgentStatusReady)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockVersions(ctx, tx, caller, target); err != nil {
		t.Fatalf("lockVersions: %v", err)
	}

	insCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := db.ExecContext(insCtx,
		`INSERT INTO chat_sessions (id, agent_version_id) VALUES (gen_random_uuid(), $1::uuid)`, target); err != nil {
		t.Fatalf("FK insert blocked or failed while config lock held: %v", err)
	}
}

// AgentVersionRepository.ListSubAgentBindings (the orchestrator's read) sees
// the same rows, in the same order, as SubAgentRepository.ListBindings.
func TestAgentVersion_ListSubAgentBindings(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	_, caller := seedVersionWithStatus(t, db, types.AgentStatusDraft)
	_, target := seedVersionWithStatus(t, db, types.AgentStatusReady)
	_, target2 := seedVersionWithStatus(t, db, types.AgentStatusReady)
	rawBind(t, db, caller, "writer", target2)
	rawBind(t, db, caller, "researcher", target)

	got, err := NewAgentVersionRepository(db).ListSubAgentBindings(ctx, caller)
	if err != nil {
		t.Fatalf("ListSubAgentBindings: %v", err)
	}
	want, _ := NewSubAgentRepository(db).ListBindings(ctx, caller)
	if len(got) != 2 || got[0].Name != "researcher" || got[1].Name != "writer" || got[0].TargetVersionID != target {
		t.Fatalf("ListSubAgentBindings = %+v", got)
	}
	for i := range got {
		if got[i].Name != want[i].Name || got[i].TargetVersionID != want[i].TargetVersionID {
			t.Fatalf("row %d differs from ListBindings: %+v vs %+v", i, got[i], want[i])
		}
	}
	empty, err := NewAgentVersionRepository(db).ListSubAgentBindings(ctx, target)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("no bindings = %#v, %v", empty, err)
	}
}

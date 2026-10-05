package repository

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// Temporary multi-agent eval/training gate (spec § REST — eval and training
// gate; acceptance "Eval and training gate").

func setToolFlag(t *testing.T, db *sql.DB, versionID string, on bool) {
	t.Helper()
	if _, err := db.Exec(`UPDATE agent_versions SET agent_tool_enabled = $2 WHERE id = $1::uuid`, versionID, on); err != nil {
		t.Fatalf("setToolFlag: %v", err)
	}
}

// rawEval inserts an eval in the given status, bypassing the repository gate.
func rawEval(t *testing.T, db *sql.DB, versionID, status string) string {
	t.Helper()
	dv := seedDatasetVersion(t, db)
	var id string
	if err := db.QueryRow(`
		INSERT INTO evals (agent_version_id, dataset_version_id, status)
		VALUES ($1::uuid, $2::uuid, $3) RETURNING id::text`, versionID, dv, status).Scan(&id); err != nil {
		t.Fatalf("rawEval: %v", err)
	}
	return id
}

// rawTraining inserts a PENDING training session, bypassing the gate.
func rawTraining(t *testing.T, db *sql.DB, evalID, parentVersionID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`
		INSERT INTO training_sessions (source_eval_id, parent_version_id)
		VALUES ($1::uuid, $2::uuid) RETURNING id::text`, evalID, parentVersionID).Scan(&id); err != nil {
		t.Fatalf("rawTraining: %v", err)
	}
	return id
}

type gateRow struct {
	status    string
	errMsg    sql.NullString
	completed bool
}

func evalRow(t *testing.T, db *sql.DB, id string) gateRow {
	t.Helper()
	var g gateRow
	if err := db.QueryRow(`SELECT status, error_message, completed_at IS NOT NULL FROM evals WHERE id = $1::uuid`, id).
		Scan(&g.status, &g.errMsg, &g.completed); err != nil {
		t.Fatalf("evalRow: %v", err)
	}
	return g
}

func trainingRow(t *testing.T, db *sql.DB, id string) gateRow {
	t.Helper()
	var g gateRow
	if err := db.QueryRow(`SELECT status, error_message, completed_at IS NOT NULL FROM training_sessions WHERE id = $1::uuid`, id).
		Scan(&g.status, &g.errMsg, &g.completed); err != nil {
		t.Fatalf("trainingRow: %v", err)
	}
	return g
}

func countEvals(t *testing.T, db *sql.DB, versionID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM evals WHERE agent_version_id = $1::uuid`, versionID).Scan(&n); err != nil {
		t.Fatalf("countEvals: %v", err)
	}
	return n
}

func assertFailedWithSentinel(t *testing.T, what string, g gateRow) {
	t.Helper()
	want := types.ErrMultiAgentEvalUnsupported.Error()
	if g.status != "FAILED" || !g.errMsg.Valid || g.errMsg.String != want || !g.completed {
		t.Fatalf("%s = %+v, want FAILED with sentinel text and completed_at", what, g)
	}
}

func TestEvalGate_CreateRefusedWhenToolEnabled(t *testing.T) {
	db := openTestDB(t)
	repo := NewEvalRepository(db)
	ctx := context.Background()
	_, v := seedVersionWithStatus(t, db, types.AgentStatusReady)
	dv := seedDatasetVersion(t, db)

	setToolFlag(t, db, v, true)
	if _, err := repo.Create(ctx, v, dv, false, nil); !errors.Is(err, types.ErrMultiAgentEvalUnsupported) {
		t.Fatalf("Create (tool on) = %v, want ErrMultiAgentEvalUnsupported", err)
	}
	if n := countEvals(t, db, v); n != 0 {
		t.Fatalf("evals rows = %d, want 0", n)
	}

	setToolFlag(t, db, v, false)
	e, err := repo.Create(ctx, v, dv, false, nil)
	if err != nil {
		t.Fatalf("Create (tool off): %v", err)
	}
	if e.Status != types.EvalStatusPending {
		t.Fatalf("status = %q, want PENDING", e.Status)
	}

	if _, err := repo.Create(ctx, "00000000-0000-0000-0000-000000000000", dv, false, nil); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("Create (unknown version) = %v, want ErrNotFound", err)
	}
}

// TestEvalGate_CreateRacesEnable runs EvalRepository.Create against
// SubAgentRepository.SetAgentToolEnabled(true) on separate connections. The
// FOR SHARE (create) / FOR UPDATE (config write) locks on the version row
// must serialise them into exactly one of the two legal outcomes.
func TestEvalGate_CreateRacesEnable(t *testing.T) {
	db := openTestDB(t)
	evals := NewEvalRepository(db)
	subs := NewSubAgentRepository(db)
	ctx := context.Background()
	var createFirst, enableFirst int
	for i := 0; i < 20; i++ {
		_, v := seedVersionWithStatus(t, db, types.AgentStatusDraft)
		dv := seedDatasetVersion(t, db)

		var start, done sync.WaitGroup
		start.Add(1)
		done.Add(2)
		var createErr, enableErr error
		go func() {
			defer done.Done()
			start.Wait()
			_, createErr = evals.Create(ctx, v, dv, false, nil)
		}()
		go func() {
			defer done.Done()
			start.Wait()
			enableErr = subs.SetAgentToolEnabled(ctx, v, true)
		}()
		start.Done()
		done.Wait()

		n := countEvals(t, db, v)
		on := toolEnabled(t, db, v)
		switch {
		case createErr == nil && n == 1 && errors.Is(enableErr, types.ErrEvalOrTrainingActive) && !on:
			createFirst++
		case enableErr == nil && on && errors.Is(createErr, types.ErrMultiAgentEvalUnsupported) && n == 0:
			enableFirst++
		default:
			t.Fatalf("iter %d: createErr=%v enableErr=%v evals=%d enabled=%v", i, createErr, enableErr, n, on)
		}
	}
	t.Logf("create-first=%d enable-first=%d", createFirst, enableFirst)
}

func TestEvalGate_StartFailsForward(t *testing.T) {
	db := openTestDB(t)
	repo := NewEvalRepository(db)
	ctx := context.Background()
	_, v := seedVersionWithStatus(t, db, types.AgentStatusReady)

	setToolFlag(t, db, v, true)
	gated := rawEval(t, db, v, "PENDING")
	if err := repo.Start(ctx, gated); !errors.Is(err, types.ErrMultiAgentEvalUnsupported) {
		t.Fatalf("Start (gated) = %v, want ErrMultiAgentEvalUnsupported", err)
	}
	assertFailedWithSentinel(t, "gated eval", evalRow(t, db, gated))

	// Gated but not PENDING: refused, nothing written.
	running := rawEval(t, db, v, "IN_PROGRESS")
	if err := repo.Start(ctx, running); !errors.Is(err, types.ErrMultiAgentEvalUnsupported) {
		t.Fatalf("Start (gated, IN_PROGRESS) = %v, want ErrMultiAgentEvalUnsupported", err)
	}
	if g := evalRow(t, db, running); g.status != "IN_PROGRESS" || g.errMsg.String != "" {
		t.Fatalf("IN_PROGRESS eval changed: %+v", g)
	}

	setToolFlag(t, db, v, false)
	ok := rawEval(t, db, v, "PENDING")
	if err := repo.Start(ctx, ok); err != nil {
		t.Fatalf("Start (tool off): %v", err)
	}
	if g := evalRow(t, db, ok); g.status != "IN_PROGRESS" {
		t.Fatalf("status = %q, want IN_PROGRESS", g.status)
	}
	if err := repo.Start(ctx, ok); !errors.Is(err, types.ErrEvalNotPending) {
		t.Fatalf("second Start = %v, want ErrEvalNotPending", err)
	}
	if err := repo.Start(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("Start (unknown) = %v, want ErrNotFound", err)
	}
}

func TestEvalGate_FailIfMultiAgent(t *testing.T) {
	db := openTestDB(t)
	repo := NewEvalRepository(db)
	ctx := context.Background()

	t.Run("gated pending", func(t *testing.T) {
		_, v := seedVersionWithStatus(t, db, types.AgentStatusReady)
		setToolFlag(t, db, v, true)
		e := rawEval(t, db, v, "PENDING")
		ts := rawTraining(t, db, e, v)
		if err := repo.FailIfMultiAgent(ctx, e); !errors.Is(err, types.ErrMultiAgentEvalUnsupported) {
			t.Fatalf("FailIfMultiAgent = %v, want ErrMultiAgentEvalUnsupported", err)
		}
		assertFailedWithSentinel(t, "eval", evalRow(t, db, e))
		assertFailedWithSentinel(t, "training", trainingRow(t, db, ts))

		var evalAt, tsAt sql.NullTime
		_ = db.QueryRow(`SELECT completed_at FROM evals WHERE id=$1::uuid`, e).Scan(&evalAt)
		_ = db.QueryRow(`SELECT completed_at FROM training_sessions WHERE id=$1::uuid`, ts).Scan(&tsAt)
		if err := repo.FailIfMultiAgent(ctx, e); !errors.Is(err, types.ErrMultiAgentEvalUnsupported) {
			t.Fatalf("second FailIfMultiAgent = %v, want ErrMultiAgentEvalUnsupported", err)
		}
		var evalAt2, tsAt2 sql.NullTime
		_ = db.QueryRow(`SELECT completed_at FROM evals WHERE id=$1::uuid`, e).Scan(&evalAt2)
		_ = db.QueryRow(`SELECT completed_at FROM training_sessions WHERE id=$1::uuid`, ts).Scan(&tsAt2)
		if !evalAt.Time.Equal(evalAt2.Time) || !tsAt.Time.Equal(tsAt2.Time) {
			t.Fatalf("second call rewrote rows: eval %v→%v ts %v→%v", evalAt, evalAt2, tsAt, tsAt2)
		}
	})

	t.Run("not gated", func(t *testing.T) {
		_, v := seedVersionWithStatus(t, db, types.AgentStatusReady)
		e := rawEval(t, db, v, "PENDING")
		ts := rawTraining(t, db, e, v)
		if err := repo.FailIfMultiAgent(ctx, e); err != nil {
			t.Fatalf("FailIfMultiAgent = %v, want nil", err)
		}
		if g := evalRow(t, db, e); g.status != "PENDING" || g.errMsg.String != "" || g.completed {
			t.Fatalf("eval changed: %+v", g)
		}
		if g := trainingRow(t, db, ts); g.status != "PENDING" || g.errMsg.String != "" || g.completed {
			t.Fatalf("training changed: %+v", g)
		}
	})

	t.Run("gated done", func(t *testing.T) {
		_, v := seedVersionWithStatus(t, db, types.AgentStatusReady)
		setToolFlag(t, db, v, true)
		e := rawEval(t, db, v, "DONE")
		ts := rawTraining(t, db, e, v)
		if err := repo.FailIfMultiAgent(ctx, e); !errors.Is(err, types.ErrMultiAgentEvalUnsupported) {
			t.Fatalf("FailIfMultiAgent = %v, want ErrMultiAgentEvalUnsupported", err)
		}
		if g := evalRow(t, db, e); g.status != "DONE" || g.errMsg.String != "" {
			t.Fatalf("DONE eval changed: %+v", g)
		}
		// The PENDING training sourced from a DONE eval is the normal train
		// case — it is still failed forward.
		assertFailedWithSentinel(t, "training", trainingRow(t, db, ts))
	})

	t.Run("unknown", func(t *testing.T) {
		if err := repo.FailIfMultiAgent(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, types.ErrNotFound) {
			t.Fatalf("FailIfMultiAgent = %v, want ErrNotFound", err)
		}
	})
}

func TestTrainingGate_CreateRefused(t *testing.T) {
	db := openTestDB(t)
	repo := NewTrainingSessionRepository(db)
	ctx := context.Background()
	evalID, v := seedEvalForTraining(t, db)

	setToolFlag(t, db, v, true)
	if _, err := repo.Create(ctx, evalID, v); !errors.Is(err, types.ErrMultiAgentEvalUnsupported) {
		t.Fatalf("Create (gated) = %v, want ErrMultiAgentEvalUnsupported", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM training_sessions WHERE source_eval_id=$1::uuid`, evalID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("training rows = %d, want 0", n)
	}

	setToolFlag(t, db, v, false)
	if _, err := repo.Create(ctx, evalID, v); err != nil {
		t.Fatalf("Create (tool off): %v", err)
	}
	if _, err := repo.Create(ctx, evalID, v); !errors.Is(err, types.ErrTrainingSessionConflict) {
		t.Fatalf("second Create = %v, want ErrTrainingSessionConflict", err)
	}
}

func TestTrainingGate_StartFailsForward(t *testing.T) {
	db := openTestDB(t)
	repo := NewTrainingSessionRepository(db)
	ctx := context.Background()
	evalID, v := seedEvalForTraining(t, db)

	ts := rawTraining(t, db, evalID, v)
	setToolFlag(t, db, v, true)
	if err := repo.Start(ctx, ts, 3, 1); !errors.Is(err, types.ErrMultiAgentEvalUnsupported) {
		t.Fatalf("Start (gated) = %v, want ErrMultiAgentEvalUnsupported", err)
	}
	assertFailedWithSentinel(t, "training", trainingRow(t, db, ts))

	// Gated but not PENDING: refused, nothing written.
	running := rawTraining(t, db, evalID, v)
	if _, err := db.Exec(`UPDATE training_sessions SET status='IN_PROGRESS', started_at=now() WHERE id=$1::uuid`, running); err != nil {
		t.Fatal(err)
	}
	if err := repo.Start(ctx, running, 3, 1); !errors.Is(err, types.ErrMultiAgentEvalUnsupported) {
		t.Fatalf("Start (gated, IN_PROGRESS) = %v, want ErrMultiAgentEvalUnsupported", err)
	}
	if g := trainingRow(t, db, running); g.status != "IN_PROGRESS" || g.errMsg.String != "" || g.completed {
		t.Fatalf("IN_PROGRESS training changed: %+v", g)
	}
	if _, err := db.Exec(`UPDATE training_sessions SET status='FAILED', completed_at=now() WHERE id=$1::uuid`, running); err != nil {
		t.Fatal(err)
	}

	setToolFlag(t, db, v, false)
	ok := rawTraining(t, db, evalID, v)
	if err := repo.Start(ctx, ok, 3, 1); err != nil {
		t.Fatalf("Start (tool off): %v", err)
	}
	if g := trainingRow(t, db, ok); g.status != "IN_PROGRESS" {
		t.Fatalf("status = %q, want IN_PROGRESS", g.status)
	}
	if err := repo.Start(ctx, ok, 3, 1); !errors.Is(err, types.ErrTrainingSessionConflict) {
		t.Fatalf("second Start = %v, want ErrTrainingSessionConflict", err)
	}
	if err := repo.Start(ctx, "00000000-0000-0000-0000-000000000000", 3, 1); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("Start (unknown) = %v, want ErrNotFound", err)
	}
}

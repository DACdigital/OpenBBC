package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// seedEvalForTraining creates an eval + returns (evalID, agentVersionID).
// Reuses seedAgent + seedDatasetVersion helpers from other _test.go files.
func seedEvalForTraining(t *testing.T, db *sql.DB) (evalID, agentVersionID string) {
	t.Helper()
	_, agentVersionID = seedAgent(t, db)
	datasetVersionID := seedDatasetVersion(t, db)
	err := db.QueryRow(`
		INSERT INTO evals (agent_version_id, dataset_version_id, status, score, completed_at)
		VALUES ($1::uuid, $2::uuid, 'DONE', 0.4, now())
		RETURNING id::text
	`, agentVersionID, datasetVersionID).Scan(&evalID)
	if err != nil {
		t.Fatalf("seed eval: %v", err)
	}
	return evalID, agentVersionID
}

func TestCreateTrainingSession(t *testing.T) {
	db := openTestDB(t)
	repo := NewTrainingSessionRepository(db)
	ctx := context.Background()

	evalID, versionID := seedEvalForTraining(t, db)

	id, err := repo.Create(ctx, evalID, versionID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty session id")
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.RequestedAt.IsZero() {
		t.Error("requested_at should be set by DB default now()")
	}
	if got.Status != types.TrainingSessionStatusPending {
		t.Errorf("status = %q, want PENDING", got.Status)
	}
	if got.SourceEvalID != evalID {
		t.Errorf("source_eval_id = %q, want %q", got.SourceEvalID, evalID)
	}
	if got.ParentVersionID != versionID {
		t.Errorf("parent_version_id = %q, want %q", got.ParentVersionID, versionID)
	}
	if got.NewVersionID != nil {
		t.Errorf("new_version_id should be nil, got %v", got.NewVersionID)
	}
	if got.StartedAt != nil || got.CompletedAt != nil {
		t.Errorf("timestamps should be nil for PENDING: started=%v completed=%v", got.StartedAt, got.CompletedAt)
	}
}

func TestOneActiveSessionPerEval(t *testing.T) {
	db := openTestDB(t)
	repo := NewTrainingSessionRepository(db)
	ctx := context.Background()

	evalA, versionA := seedEvalForTraining(t, db)
	evalB, _ := seedEvalForTraining(t, db)

	if _, err := repo.Create(ctx, evalA, versionA); err != nil {
		t.Fatalf("first Create for eval A: %v", err)
	}

	// Second PENDING for same eval → conflict.
	_, err := repo.Create(ctx, evalA, versionA)
	if !errors.Is(err, types.ErrTrainingSessionConflict) {
		t.Fatalf("expected ErrTrainingSessionConflict, got %v", err)
	}

	// Different eval → allowed.
	if _, err := repo.Create(ctx, evalB, versionA); err != nil {
		t.Fatalf("Create for eval B should succeed: %v", err)
	}

	// Mark first session FAILED, then a new one for eval A is allowed.
	first, _ := repo.GetActiveByEval(ctx, evalA)
	if _, err := db.Exec(`UPDATE training_sessions SET status='FAILED' WHERE id = $1::uuid`, first.ID); err != nil {
		t.Fatalf("manual FAILED update: %v", err)
	}
	if _, err := repo.Create(ctx, evalA, versionA); err != nil {
		t.Fatalf("Create after FAILED should succeed: %v", err)
	}
}

func TestGetActiveByEval(t *testing.T) {
	db := openTestDB(t)
	repo := NewTrainingSessionRepository(db)
	ctx := context.Background()

	evalID, versionID := seedEvalForTraining(t, db)

	// No sessions → nil, no error.
	got, err := repo.GetActiveByEval(ctx, evalID)
	if err != nil {
		t.Fatalf("GetActiveByEval empty: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for no active session, got %+v", got)
	}

	// One PENDING → returned.
	id, _ := repo.Create(ctx, evalID, versionID)
	got, err = repo.GetActiveByEval(ctx, evalID)
	if err != nil {
		t.Fatalf("GetActiveByEval PENDING: %v", err)
	}
	if got == nil || got.ID != id {
		t.Errorf("expected PENDING session %s, got %+v", id, got)
	}

	// Mark FAILED → nil again (terminal statuses don't count).
	if _, err := db.Exec(`UPDATE training_sessions SET status='FAILED' WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("manual FAILED: %v", err)
	}
	got, err = repo.GetActiveByEval(ctx, evalID)
	if err != nil {
		t.Fatalf("GetActiveByEval after FAILED: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after FAILED, got %+v", got)
	}
}

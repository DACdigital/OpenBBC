package repository

import (
	"context"
	"database/sql"
	"encoding/json"
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

func TestStart_PromotesPendingToInProgress(t *testing.T) {
	db := openTestDB(t)
	repo := NewTrainingSessionRepository(db)
	ctx := context.Background()

	evalID, versionID := seedEvalForTraining(t, db)
	id, _ := repo.Create(ctx, evalID, versionID)

	if err := repo.Start(ctx, id, 5, 3); err != nil {
		t.Fatalf("Start: %v", err)
	}

	got, _ := repo.GetByID(ctx, id)
	if got.Status != types.TrainingSessionStatusInProgress {
		t.Errorf("status = %q, want IN_PROGRESS", got.Status)
	}
	if got.StartedAt == nil {
		t.Error("started_at should be non-nil")
	}
	if got.Epochs == nil || *got.Epochs != 5 {
		t.Errorf("epochs = %v, want 5", got.Epochs)
	}
	if got.Patience == nil || *got.Patience != 3 {
		t.Errorf("patience = %v, want 3", got.Patience)
	}

	// Starting again → conflict.
	if err := repo.Start(ctx, id, 5, 3); !errors.Is(err, types.ErrTrainingSessionConflict) {
		t.Errorf("second Start should conflict, got %v", err)
	}
}

func TestComplete_AtomicVersionCreation(t *testing.T) {
	db := openTestDB(t)
	repo := NewTrainingSessionRepository(db)
	vrepo := NewAgentVersionRepository(db)
	ctx := context.Background()

	evalID, versionID := seedEvalForTraining(t, db)
	id, _ := repo.Create(ctx, evalID, versionID)
	_ = repo.Start(ctx, id, 5, 3)

	prompts, _ := json.Marshal(types.Prompts{MainPrompt: "trained", SkillPrompts: map[string]string{}})
	report := json.RawMessage(`{"schema_version":"training-report-v1","initial_score":0.4,"final_score":0.7,"total_epochs_run":3,"stopped_reason":"max_epochs","epochs":[]}`)

	newVersionID, err := repo.Complete(ctx, vrepo, id, prompts, report, CompleteSummary{
		InitialScore: 0.4, FinalScore: 0.7, TotalEpochsRun: 3, StoppedReason: "max_epochs",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if newVersionID == "" {
		t.Fatal("expected non-empty new version id")
	}

	// New agent_version exists with status READY.
	var status string
	if err := db.QueryRow(`SELECT status FROM agent_versions WHERE id = $1::uuid`, newVersionID).Scan(&status); err != nil {
		t.Fatalf("read new version: %v", err)
	}
	if status != "READY" {
		t.Errorf("new version status = %q, want READY", status)
	}

	// Session is DONE and linked.
	got, _ := repo.GetByID(ctx, id)
	if got.Status != types.TrainingSessionStatusDone {
		t.Errorf("session status = %q, want DONE", got.Status)
	}
	if got.NewVersionID == nil || *got.NewVersionID != newVersionID {
		t.Errorf("new_version_id = %v, want %s", got.NewVersionID, newVersionID)
	}
	if got.FinalScore == nil || *got.FinalScore != 0.7 {
		t.Errorf("final_score = %v, want 0.7", got.FinalScore)
	}
	if got.CompletedAt == nil {
		t.Error("completed_at should be set")
	}
}

func TestComplete_FromWrongStatus(t *testing.T) {
	db := openTestDB(t)
	repo := NewTrainingSessionRepository(db)
	vrepo := NewAgentVersionRepository(db)
	ctx := context.Background()

	evalID, versionID := seedEvalForTraining(t, db)
	id, _ := repo.Create(ctx, evalID, versionID)
	// Not calling Start — session is still PENDING.

	prompts, _ := json.Marshal(types.Prompts{MainPrompt: "x", SkillPrompts: map[string]string{}})
	_, err := repo.Complete(ctx, vrepo, id, prompts, json.RawMessage(`{}`), CompleteSummary{})
	if !errors.Is(err, types.ErrTrainingSessionConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}

	// No new version should have been created.
	got, _ := repo.GetByID(ctx, id)
	if got.NewVersionID != nil {
		t.Errorf("new_version_id should be nil after failed Complete, got %v", got.NewVersionID)
	}
}

func TestFail_FromActiveStates(t *testing.T) {
	db := openTestDB(t)
	repo := NewTrainingSessionRepository(db)
	ctx := context.Background()

	evalID, versionID := seedEvalForTraining(t, db)

	// PENDING → FAILED.
	id1, _ := repo.Create(ctx, evalID, versionID)
	if err := repo.Fail(ctx, id1, "operator declined"); err != nil {
		t.Fatalf("Fail from PENDING: %v", err)
	}
	got, _ := repo.GetByID(ctx, id1)
	if got.Status != types.TrainingSessionStatusFailed {
		t.Errorf("status = %q, want FAILED", got.Status)
	}
	if got.ErrorMessage != "operator declined" {
		t.Errorf("error_message = %q", got.ErrorMessage)
	}

	// IN_PROGRESS → FAILED.
	evalID2, _ := seedEvalForTraining(t, db)
	id2, _ := repo.Create(ctx, evalID2, versionID)
	_ = repo.Start(ctx, id2, 5, 3)
	if err := repo.Fail(ctx, id2, "crashed"); err != nil {
		t.Fatalf("Fail from IN_PROGRESS: %v", err)
	}

	// Failing a terminal session → conflict.
	if err := repo.Fail(ctx, id1, "again"); !errors.Is(err, types.ErrTrainingSessionConflict) {
		t.Errorf("second Fail should conflict, got %v", err)
	}
}

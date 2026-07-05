package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestEval_CreateGetList(t *testing.T) {
	_, _, db := withRepo(t)
	var agentID, versionID string
	_ = db.QueryRow(`INSERT INTO agents (name) VALUES ('e-a') RETURNING id::text`).Scan(&agentID)
	_ = db.QueryRow(`INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY') RETURNING id::text`, agentID).Scan(&versionID)
	var datasetID, dvID string
	_ = db.QueryRow(`INSERT INTO datasets (name) VALUES ('e-ds') RETURNING id::text`).Scan(&datasetID)
	_ = db.QueryRow(`INSERT INTO dataset_versions (dataset_id, status, version_num, closed_at) VALUES ($1::uuid, 'CLOSED', 1, now()) RETURNING id::text`, datasetID).Scan(&dvID)

	repo := NewEvalRepository(db)
	e, err := repo.Create(context.Background(), versionID, dvID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if e.Status != types.EvalStatusPending {
		t.Errorf("status = %q, want PENDING", e.Status)
	}
	got, err := repo.GetByID(context.Background(), e.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != e.ID {
		t.Errorf("GetByID id mismatch: %q vs %q", got.ID, e.ID)
	}
	all, _ := repo.ListByAgentVersion(context.Background(), versionID)
	if len(all) != 1 {
		t.Errorf("ListByAgentVersion size = %d, want 1", len(all))
	}
}

func TestEval_StartStateMachine(t *testing.T) {
	_, _, db := withRepo(t)
	var agentID, versionID, datasetID, dvID string
	_ = db.QueryRow(`INSERT INTO agents (name) VALUES ('e-s') RETURNING id::text`).Scan(&agentID)
	_ = db.QueryRow(`INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY') RETURNING id::text`, agentID).Scan(&versionID)
	_ = db.QueryRow(`INSERT INTO datasets (name) VALUES ('e-ds-s') RETURNING id::text`).Scan(&datasetID)
	_ = db.QueryRow(`INSERT INTO dataset_versions (dataset_id, status, version_num, closed_at) VALUES ($1::uuid, 'CLOSED', 1, now()) RETURNING id::text`, datasetID).Scan(&dvID)

	repo := NewEvalRepository(db)
	e, _ := repo.Create(context.Background(), versionID, dvID)
	if err := repo.Start(context.Background(), e.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := repo.Start(context.Background(), e.ID); !errors.Is(err, types.ErrEvalNotPending) {
		t.Fatalf("second Start = %v, want ErrEvalNotPending", err)
	}
	if err := repo.Start(context.Background(), "00000000-0000-0000-0000-000000000000"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("missing Start = %v, want ErrNotFound", err)
	}
}

func TestEval_SubmitDone(t *testing.T) {
	_, _, db := withRepo(t)
	var agentID, versionID, datasetID, dvID, sessionID, msgID string
	_ = db.QueryRow(`INSERT INTO agents (name) VALUES ('e-sub') RETURNING id::text`).Scan(&agentID)
	_ = db.QueryRow(`INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY') RETURNING id::text`, agentID).Scan(&versionID)
	_ = db.QueryRow(`INSERT INTO datasets (name) VALUES ('e-ds-sub') RETURNING id::text`).Scan(&datasetID)
	_ = db.QueryRow(`INSERT INTO dataset_versions (dataset_id, status, version_num, closed_at) VALUES ($1::uuid, 'CLOSED', 1, now()) RETURNING id::text`, datasetID).Scan(&dvID)
	_ = db.QueryRow(`INSERT INTO chat_sessions (agent_version_id, locked_at) VALUES ($1::uuid, now()) RETURNING id::text`, versionID).Scan(&sessionID)
	_ = db.QueryRow(`INSERT INTO chat_messages (session_id, role, content, seq) VALUES ($1::uuid, 'assistant', '[]'::jsonb, 1) RETURNING id::text`, sessionID).Scan(&msgID)
	_, _ = db.Exec(`INSERT INTO chat_message_feedback (message_id, rating, judge_criteria) VALUES ($1::uuid, 'up', '["c"]'::jsonb)`, msgID)
	_, _ = db.Exec(`INSERT INTO dataset_version_sessions (dataset_version_id, session_id) VALUES ($1::uuid, $2::uuid)`, dvID, sessionID)

	repo := NewEvalRepository(db)
	e, _ := repo.Create(context.Background(), versionID, dvID)
	_ = repo.Start(context.Background(), e.ID)

	result := &types.EvalResult{
		SchemaVersion:  "eval-result-v1",
		Status:         types.EvalStatusDone,
		Score:          0.5,
		TotalCriteria:  2,
		PassedCriteria: 1,
		AikdmMeta:      json.RawMessage(`{"judge_model":"claude-haiku-4-5"}`),
		Sessions: []types.EvalResultSession{{
			SessionID: sessionID, Score: 0.5, TotalCriteria: 2, PassedCriteria: 1,
			Transcript: json.RawMessage(`[]`),
			Judgments:  json.RawMessage(`[]`),
		}},
	}
	if err := repo.Submit(context.Background(), e.ID, result); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got, _ := repo.GetByID(context.Background(), e.ID)
	if got.Status != types.EvalStatusDone {
		t.Errorf("status = %q, want DONE", got.Status)
	}
	if got.Score == nil || *got.Score != 0.5 {
		t.Errorf("score = %v, want 0.5", got.Score)
	}
	if err := repo.Submit(context.Background(), e.ID, result); !errors.Is(err, types.ErrEvalAlreadyFinal) {
		t.Errorf("second Submit = %v, want ErrEvalAlreadyFinal", err)
	}
	sessions, _ := repo.ListSessions(context.Background(), e.ID)
	if len(sessions) != 1 {
		t.Errorf("ListSessions size = %d, want 1", len(sessions))
	}
}

func TestEval_SubmitFailed(t *testing.T) {
	_, _, db := withRepo(t)
	var agentID, versionID, datasetID, dvID string
	_ = db.QueryRow(`INSERT INTO agents (name) VALUES ('e-fail') RETURNING id::text`).Scan(&agentID)
	_ = db.QueryRow(`INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY') RETURNING id::text`, agentID).Scan(&versionID)
	_ = db.QueryRow(`INSERT INTO datasets (name) VALUES ('e-ds-fail') RETURNING id::text`).Scan(&datasetID)
	_ = db.QueryRow(`INSERT INTO dataset_versions (dataset_id, status, version_num, closed_at) VALUES ($1::uuid, 'CLOSED', 1, now()) RETURNING id::text`, datasetID).Scan(&dvID)

	repo := NewEvalRepository(db)
	e, _ := repo.Create(context.Background(), versionID, dvID)
	_ = repo.Start(context.Background(), e.ID)
	if err := repo.Fail(context.Background(), e.ID, "aikdm crashed"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	got, _ := repo.GetByID(context.Background(), e.ID)
	if got.Status != types.EvalStatusFailed || got.ErrorMessage != "aikdm crashed" {
		t.Errorf("bad terminal state: %+v", got)
	}
}

package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestFeedbackUpsert_Up(t *testing.T) {
	_, _, db := withRepo(t)
	// Seed: minimal agent + version + session + one assistant message.
	var agentID, versionID, sessionID, messageID string
	if err := db.QueryRow(`
		INSERT INTO agents (name) VALUES ('fb-test')
		RETURNING id::text
	`).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY')
		RETURNING id::text
	`, agentID).Scan(&versionID); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO chat_sessions (agent_version_id) VALUES ($1::uuid)
		RETURNING id::text
	`, versionID).Scan(&sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO chat_messages (session_id, role, content, seq)
		VALUES ($1::uuid, 'assistant', '[{"type":"text","text":"hi"}]'::jsonb, 1)
		RETURNING id::text
	`, sessionID).Scan(&messageID); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	repo := NewFeedbackRepository(db)
	if err := repo.Upsert(context.Background(), messageID, types.FeedbackRatingUp, "", ""); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	fb, err := repo.Get(context.Background(), messageID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fb.Rating != types.FeedbackRatingUp {
		t.Errorf("rating = %q, want 'up'", fb.Rating)
	}
	if fb.Comment != "" || fb.ExpectedOutput != "" {
		t.Errorf("comment/expected should be empty on plain thumbs-up, got %+v", fb)
	}
}

func TestFeedbackUpsert_DownRequiresComment(t *testing.T) {
	_, _, db := withRepo(t)
	repo := NewFeedbackRepository(db)
	err := repo.Upsert(context.Background(), "00000000-0000-0000-0000-000000000000",
		types.FeedbackRatingDown, "", "")
	if !errors.Is(err, types.ErrFeedbackCommentRequired) {
		t.Fatalf("err = %v, want ErrFeedbackCommentRequired", err)
	}
}

func TestFeedbackUpsert_RefusesUserMessage(t *testing.T) {
	_, _, db := withRepo(t)
	var agentID, versionID, sessionID, messageID string
	_ = db.QueryRow(`INSERT INTO agents (name) VALUES ('fb-user') RETURNING id::text`).Scan(&agentID)
	_ = db.QueryRow(`INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY') RETURNING id::text`, agentID).Scan(&versionID)
	_ = db.QueryRow(`INSERT INTO chat_sessions (agent_version_id) VALUES ($1::uuid) RETURNING id::text`, versionID).Scan(&sessionID)
	_ = db.QueryRow(`INSERT INTO chat_messages (session_id, role, content, seq)
		VALUES ($1::uuid, 'user', '[]'::jsonb, 1) RETURNING id::text`, sessionID).Scan(&messageID)

	repo := NewFeedbackRepository(db)
	err := repo.Upsert(context.Background(), messageID, types.FeedbackRatingUp, "", "")
	if !errors.Is(err, types.ErrFeedbackNotAssistant) {
		t.Fatalf("err = %v, want ErrFeedbackNotAssistant", err)
	}
}

func TestFeedback_DeleteAndGetForSession(t *testing.T) {
	_, _, db := withRepo(t)
	var agentID, versionID, sessionID, messageID string
	_ = db.QueryRow(`INSERT INTO agents (name) VALUES ('fb-multi') RETURNING id::text`).Scan(&agentID)
	_ = db.QueryRow(`INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY') RETURNING id::text`, agentID).Scan(&versionID)
	_ = db.QueryRow(`INSERT INTO chat_sessions (agent_version_id) VALUES ($1::uuid) RETURNING id::text`, versionID).Scan(&sessionID)
	_ = db.QueryRow(`INSERT INTO chat_messages (session_id, role, content, seq)
		VALUES ($1::uuid, 'assistant', '[]'::jsonb, 1) RETURNING id::text`, sessionID).Scan(&messageID)

	repo := NewFeedbackRepository(db)
	if err := repo.Upsert(context.Background(), messageID, types.FeedbackRatingDown, "bad", "the answer"); err != nil {
		t.Fatalf("seed feedback: %v", err)
	}
	m, err := repo.GetForSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetForSession: %v", err)
	}
	if len(m) != 1 {
		t.Fatalf("map size = %d, want 1", len(m))
	}
	if fb := m[messageID]; fb.Rating != types.FeedbackRatingDown || fb.Comment != "bad" {
		t.Errorf("unexpected fb: %+v", fb)
	}
	if err := repo.Delete(context.Background(), messageID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	m2, _ := repo.GetForSession(context.Background(), sessionID)
	if len(m2) != 0 {
		t.Errorf("expected 0 rows after Delete, got %d", len(m2))
	}
}

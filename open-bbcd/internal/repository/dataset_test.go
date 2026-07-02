package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestDataset_CreateGetList(t *testing.T) {
	_, _, db := withRepo(t)
	repo := NewDatasetRepository(db)

	d, err := repo.Create(context.Background(), "ds-a", "desc")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if d.Name != "ds-a" || d.Description != "desc" || d.ID == "" {
		t.Fatalf("bad Create result: %+v", d)
	}

	got, err := repo.GetByID(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != d.ID || got.Name != "ds-a" {
		t.Errorf("GetByID = %+v", got)
	}

	if _, err := repo.Create(context.Background(), "ds-b", ""); err != nil {
		t.Fatalf("Create 2nd: %v", err)
	}

	all, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) < 2 {
		t.Errorf("expected >=2 datasets, got %d", len(all))
	}

	if _, err := repo.Create(context.Background(), "", ""); !errors.Is(err, types.ErrDatasetNameRequired) {
		t.Errorf("expected ErrDatasetNameRequired for empty name, got %v", err)
	}
}

func TestDataset_EnsureDraft_CreatesWhenNone(t *testing.T) {
	_, _, db := withRepo(t)
	repo := NewDatasetRepository(db)
	d, _ := repo.Create(context.Background(), "ds-draft", "")

	v1, err := repo.EnsureDraft(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("EnsureDraft (first): %v", err)
	}
	if v1.Status != types.DatasetVersionDraft || v1.VersionNum != 1 {
		t.Fatalf("first EnsureDraft = %+v; want DRAFT v1", v1)
	}
	v2, _ := repo.EnsureDraft(context.Background(), d.ID)
	if v2.ID != v1.ID {
		t.Errorf("second EnsureDraft returned different version: %s vs %s", v2.ID, v1.ID)
	}
}

func TestDataset_CloseDraft_LocksSessions(t *testing.T) {
	_, _, db := withRepo(t)
	repo := NewDatasetRepository(db)
	d, _ := repo.Create(context.Background(), "ds-close", "")
	draft, _ := repo.EnsureDraft(context.Background(), d.ID)

	// Seed a session WITH feedback so CloseDraft's purge step keeps it in.
	var agentID, versionID, sessionID, messageID string
	_ = db.QueryRow(`INSERT INTO agents (name) VALUES ('close-a') RETURNING id::text`).Scan(&agentID)
	_ = db.QueryRow(`INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY') RETURNING id::text`, agentID).Scan(&versionID)
	_ = db.QueryRow(`INSERT INTO chat_sessions (agent_version_id) VALUES ($1::uuid) RETURNING id::text`, versionID).Scan(&sessionID)
	_ = db.QueryRow(`INSERT INTO chat_messages (session_id, role, content, seq) VALUES ($1::uuid, 'assistant', '[]'::jsonb, 1) RETURNING id::text`, sessionID).Scan(&messageID)
	_, _ = db.Exec(`INSERT INTO chat_message_feedback (message_id, rating) VALUES ($1::uuid, 'up')`, messageID)
	if _, err := db.Exec(`INSERT INTO dataset_version_sessions (dataset_version_id, session_id) VALUES ($1::uuid, $2::uuid)`, draft.ID, sessionID); err != nil {
		t.Fatalf("seed dvs: %v", err)
	}

	if err := repo.CloseDraft(context.Background(), draft.ID, "release note"); err != nil {
		t.Fatalf("CloseDraft: %v", err)
	}
	var status, closeNote string
	_ = db.QueryRow(`SELECT status, close_note FROM dataset_versions WHERE id = $1::uuid`, draft.ID).Scan(&status, &closeNote)
	if status != "CLOSED" || closeNote != "release note" {
		t.Errorf("post-close = status %q, note %q", status, closeNote)
	}
	var locked sql.NullTime
	_ = db.QueryRow(`SELECT locked_at FROM chat_sessions WHERE id = $1::uuid`, sessionID).Scan(&locked)
	if !locked.Valid {
		t.Errorf("session locked_at should be set, is NULL")
	}
}

func TestDataset_ListVersionsAndSessions(t *testing.T) {
	_, _, db := withRepo(t)
	repo := NewDatasetRepository(db)
	d, _ := repo.Create(context.Background(), "ds-vsl", "")

	draft, _ := repo.EnsureDraft(context.Background(), d.ID)

	// Add a session with source agent+version + one thumbs-up feedback.
	var agentID, versionID, sessionID, messageID string
	_ = db.QueryRow(`INSERT INTO agents (name) VALUES ('vsl-a') RETURNING id::text`).Scan(&agentID)
	_ = db.QueryRow(`INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY') RETURNING id::text`, agentID).Scan(&versionID)
	_ = db.QueryRow(`INSERT INTO chat_sessions (agent_version_id, title) VALUES ($1::uuid, 'sess-title') RETURNING id::text`, versionID).Scan(&sessionID)
	_ = db.QueryRow(`INSERT INTO chat_messages (session_id, role, content, seq) VALUES ($1::uuid, 'assistant', '[]'::jsonb, 1) RETURNING id::text`, sessionID).Scan(&messageID)
	_, _ = db.Exec(`INSERT INTO chat_message_feedback (message_id, rating) VALUES ($1::uuid, 'up')`, messageID)
	_, _ = db.Exec(`INSERT INTO dataset_version_sessions (dataset_version_id, session_id) VALUES ($1::uuid, $2::uuid)`, draft.ID, sessionID)

	versions, err := repo.ListVersions(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 1 || versions[0].Status != types.DatasetVersionDraft {
		t.Errorf("versions = %+v", versions)
	}

	got, err := repo.GetVersion(context.Background(), draft.ID)
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if got.ID != draft.ID {
		t.Errorf("GetVersion id mismatch: %s vs %s", got.ID, draft.ID)
	}

	refs, err := repo.GetVersionSessions(context.Background(), draft.ID)
	if err != nil {
		t.Fatalf("GetVersionSessions: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs len = %d", len(refs))
	}
	if refs[0].SessionTitle != "sess-title" || refs[0].AgentName != "vsl-a" || refs[0].ThumbsUpCount != 1 || refs[0].ThumbsDownCount != 0 {
		t.Errorf("ref = %+v", refs[0])
	}
}

func TestDataset_AssignAndUnassign(t *testing.T) {
	_, _, db := withRepo(t)
	repo := NewDatasetRepository(db)
	fb := NewFeedbackRepository(db)
	d, _ := repo.Create(context.Background(), "ds-assign", "")

	// Seed a session with one feedback row.
	var agentID, versionID, sessionID, messageID string
	_ = db.QueryRow(`INSERT INTO agents (name) VALUES ('as-a') RETURNING id::text`).Scan(&agentID)
	_ = db.QueryRow(`INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY') RETURNING id::text`, agentID).Scan(&versionID)
	_ = db.QueryRow(`INSERT INTO chat_sessions (agent_version_id) VALUES ($1::uuid) RETURNING id::text`, versionID).Scan(&sessionID)
	_ = db.QueryRow(`INSERT INTO chat_messages (session_id, role, content, seq) VALUES ($1::uuid, 'assistant', '[]'::jsonb, 1) RETURNING id::text`, sessionID).Scan(&messageID)
	if err := fb.Upsert(context.Background(), messageID, types.FeedbackRatingUp, "", "", nil); err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	v, err := repo.AssignSessionToDraft(context.Background(), d.ID, sessionID)
	if err != nil {
		t.Fatalf("AssignSessionToDraft: %v", err)
	}
	if v.Status != types.DatasetVersionDraft {
		t.Errorf("expected DRAFT, got %s", v.Status)
	}

	d2, _ := repo.Create(context.Background(), "ds-other", "")
	if _, err := repo.AssignSessionToDraft(context.Background(), d2.ID, sessionID); !errors.Is(err, types.ErrSessionAlreadyInDataset) {
		t.Errorf("expected ErrSessionAlreadyInDataset, got %v", err)
	}

	if err := repo.UnassignSession(context.Background(), sessionID); err != nil {
		t.Fatalf("UnassignSession: %v", err)
	}

	if _, err := repo.AssignSessionToDraft(context.Background(), d2.ID, sessionID); err != nil {
		t.Errorf("re-assign after unassign: %v", err)
	}

	// A session with no feedback rejects.
	var otherSessionID string
	_ = db.QueryRow(`INSERT INTO chat_sessions (agent_version_id) VALUES ($1::uuid) RETURNING id::text`, versionID).Scan(&otherSessionID)
	if _, err := repo.AssignSessionToDraft(context.Background(), d.ID, otherSessionID); !errors.Is(err, types.ErrSessionNoFeedback) {
		t.Errorf("expected ErrSessionNoFeedback, got %v", err)
	}
}

func TestDataset_CloseDraft_PurgesSessionsWithoutFeedback(t *testing.T) {
	_, _, db := withRepo(t)
	repo := NewDatasetRepository(db)
	fb := NewFeedbackRepository(db)
	d, _ := repo.Create(context.Background(), "ds-purge", "")

	// Seed two sessions, both with feedback initially.
	var agentID, versionID string
	_ = db.QueryRow(`INSERT INTO agents (name) VALUES ('purge-a') RETURNING id::text`).Scan(&agentID)
	_ = db.QueryRow(`INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY') RETURNING id::text`, agentID).Scan(&versionID)

	seedSess := func() (sessionID, msgID string) {
		_ = db.QueryRow(`INSERT INTO chat_sessions (agent_version_id) VALUES ($1::uuid) RETURNING id::text`, versionID).Scan(&sessionID)
		_ = db.QueryRow(`INSERT INTO chat_messages (session_id, role, content, seq) VALUES ($1::uuid, 'assistant', '[]'::jsonb, 1) RETURNING id::text`, sessionID).Scan(&msgID)
		return
	}
	keepSess, keepMsg := seedSess()
	dropSess, dropMsg := seedSess()
	if err := fb.Upsert(context.Background(), keepMsg, types.FeedbackRatingUp, "", "", nil); err != nil {
		t.Fatalf("seed keep feedback: %v", err)
	}
	if err := fb.Upsert(context.Background(), dropMsg, types.FeedbackRatingUp, "", "", nil); err != nil {
		t.Fatalf("seed drop feedback: %v", err)
	}

	// Assign both to the draft.
	draft, _ := repo.AssignSessionToDraft(context.Background(), d.ID, keepSess)
	if _, err := repo.AssignSessionToDraft(context.Background(), d.ID, dropSess); err != nil {
		t.Fatalf("assign drop: %v", err)
	}

	// User changes mind and removes feedback from dropSess.
	if err := fb.Delete(context.Background(), dropMsg); err != nil {
		t.Fatalf("delete drop feedback: %v", err)
	}

	// Close — dropSess should be purged, keepSess should be locked in.
	if err := repo.CloseDraft(context.Background(), draft.ID, "purge test"); err != nil {
		t.Fatalf("CloseDraft: %v", err)
	}

	var keepStill bool
	_ = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM dataset_version_sessions WHERE session_id=$1::uuid)`, keepSess).Scan(&keepStill)
	if !keepStill {
		t.Errorf("keep session should still be in the closed version")
	}
	var dropStill bool
	_ = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM dataset_version_sessions WHERE session_id=$1::uuid)`, dropSess).Scan(&dropStill)
	if dropStill {
		t.Errorf("drop session should have been purged")
	}

	// keep locked, drop unlocked
	var keepLocked, dropLocked sql.NullTime
	_ = db.QueryRow(`SELECT locked_at FROM chat_sessions WHERE id=$1::uuid`, keepSess).Scan(&keepLocked)
	_ = db.QueryRow(`SELECT locked_at FROM chat_sessions WHERE id=$1::uuid`, dropSess).Scan(&dropLocked)
	if !keepLocked.Valid {
		t.Errorf("keep session should be locked after close")
	}
	if dropLocked.Valid {
		t.Errorf("drop session should NOT be locked (it was purged before the lock step)")
	}
}

func TestDataset_NextDraftInheritsFromPreviousClosed(t *testing.T) {
	_, _, db := withRepo(t)
	repo := NewDatasetRepository(db)
	fb := NewFeedbackRepository(db)
	d, _ := repo.Create(context.Background(), "ds-inherit", "")

	// Seed two sessions with feedback, assign both to v1 draft, close.
	var agentID, versionID string
	_ = db.QueryRow(`INSERT INTO agents (name) VALUES ('inh-a') RETURNING id::text`).Scan(&agentID)
	_ = db.QueryRow(`INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY') RETURNING id::text`, agentID).Scan(&versionID)
	mkSess := func() (sessionID string) {
		var msgID string
		_ = db.QueryRow(`INSERT INTO chat_sessions (agent_version_id) VALUES ($1::uuid) RETURNING id::text`, versionID).Scan(&sessionID)
		_ = db.QueryRow(`INSERT INTO chat_messages (session_id, role, content, seq) VALUES ($1::uuid, 'assistant', '[]'::jsonb, 1) RETURNING id::text`, sessionID).Scan(&msgID)
		_ = fb.Upsert(context.Background(), msgID, types.FeedbackRatingUp, "", "", nil)
		return
	}
	sessA := mkSess()
	sessB := mkSess()
	v1, err := repo.AssignSessionToDraft(context.Background(), d.ID, sessA)
	if err != nil {
		t.Fatalf("assign A: %v", err)
	}
	if _, err := repo.AssignSessionToDraft(context.Background(), d.ID, sessB); err != nil {
		t.Fatalf("assign B: %v", err)
	}
	if err := repo.CloseDraft(context.Background(), v1.ID, "v1"); err != nil {
		t.Fatalf("close v1: %v", err)
	}

	// A new session for v2 (fresh, unlocked).
	sessC := mkSess()

	// Assigning C creates v2 draft. It must inherit A and B.
	v2, err := repo.AssignSessionToDraft(context.Background(), d.ID, sessC)
	if err != nil {
		t.Fatalf("assign C to new draft: %v", err)
	}
	if v2.ID == v1.ID {
		t.Fatalf("expected a new draft, got same version id as v1")
	}
	refs, _ := repo.GetVersionSessions(context.Background(), v2.ID)
	if len(refs) != 3 {
		t.Fatalf("v2 draft should have 3 sessions (2 inherited + 1 new), got %d", len(refs))
	}
	seen := map[string]bool{}
	for _, r := range refs {
		seen[r.SessionID] = true
	}
	if !seen[sessA] || !seen[sessB] || !seen[sessC] {
		t.Errorf("v2 missing an expected session; got %v", seen)
	}

	// Closing v2 should not re-lock A and B (they were locked at v1 close).
	// Grab their existing lock times.
	var lockA1, lockB1 sql.NullTime
	_ = db.QueryRow(`SELECT locked_at FROM chat_sessions WHERE id=$1::uuid`, sessA).Scan(&lockA1)
	_ = db.QueryRow(`SELECT locked_at FROM chat_sessions WHERE id=$1::uuid`, sessB).Scan(&lockB1)
	if !lockA1.Valid || !lockB1.Valid {
		t.Fatalf("expected A and B locked from v1 close")
	}
	if err := repo.CloseDraft(context.Background(), v2.ID, "v2"); err != nil {
		t.Fatalf("close v2: %v", err)
	}
	var lockA2, lockB2, lockC sql.NullTime
	_ = db.QueryRow(`SELECT locked_at FROM chat_sessions WHERE id=$1::uuid`, sessA).Scan(&lockA2)
	_ = db.QueryRow(`SELECT locked_at FROM chat_sessions WHERE id=$1::uuid`, sessB).Scan(&lockB2)
	_ = db.QueryRow(`SELECT locked_at FROM chat_sessions WHERE id=$1::uuid`, sessC).Scan(&lockC)
	if lockA1.Time != lockA2.Time || lockB1.Time != lockB2.Time {
		t.Errorf("A/B lock times should be preserved from v1 close, not overwritten by v2 close")
	}
	if !lockC.Valid {
		t.Errorf("C should be locked after v2 close")
	}
}

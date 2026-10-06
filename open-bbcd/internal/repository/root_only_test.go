package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

// Root-only rule (spec § REST — root-only rule and child transcripts): every
// per-session read/write used by a route resolves root sessions only; a
// child id is ErrNotFound with no side effect.

func insertChatRoot(t *testing.T, db *sql.DB, versionID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(
		`INSERT INTO chat_sessions (agent_version_id) VALUES ($1::uuid) RETURNING id::text`, versionID,
	).Scan(&id); err != nil {
		t.Fatalf("insert chat root: %v", err)
	}
	return id
}

func insertChatMessage(t *testing.T, db *sql.DB, sessionID string, seq int) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`
		INSERT INTO chat_messages (session_id, role, content, seq)
		VALUES ($1::uuid, 'assistant', '[{"type":"text","text":"hi"}]'::jsonb, $2)
		RETURNING id::text`, sessionID, seq,
	).Scan(&id); err != nil {
		t.Fatalf("insert chat message: %v", err)
	}
	return id
}

func TestChatRepository_RootOnly_GetSession(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	v := seedAgentVersion(t, db)
	r := insertChatRoot(t, db, v)
	c := insertChatChild(t, db, r, "call-1", v)
	repo := NewChatRepository(db)

	if _, err := repo.GetSession(ctx, c, v); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("GetSession(child) err = %v, want ErrNotFound", err)
	}
	got, err := repo.GetSession(ctx, r, v)
	if err != nil {
		t.Fatalf("GetSession(root): %v", err)
	}
	if got.ID != r || got.Depth != 0 || got.ParentSessionID != nil {
		t.Fatalf("root = %+v", got)
	}
}

func TestChatRepository_RootOnly_ListSessions(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	v := seedAgentVersion(t, db)
	other := seedAgentVersion(t, db)
	r := insertChatRoot(t, db, v)
	_ = insertChatChild(t, db, r, "call-1", v)
	// Child of another version's root, pinned to v.
	otherRoot := insertChatRoot(t, db, other)
	_ = insertChatChild(t, db, otherRoot, "call-2", v)
	repo := NewChatRepository(db)

	for _, limit := range []int{0, 10} {
		list, total, err := repo.ListSessions(ctx, v, limit, 0)
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		if total != 1 || len(list) != 1 || list[0].ID != r {
			t.Fatalf("limit=%d: total=%d list=%+v, want only root %s", limit, total, list, r)
		}
	}
}

func TestChatRepository_RootOnly_UpdateSessionTitle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	v := seedAgentVersion(t, db)
	r := insertChatRoot(t, db, v)
	c := insertChatChild(t, db, r, "call-1", v)
	repo := NewChatRepository(db)

	if err := repo.UpdateSessionTitle(ctx, c, v, "x"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("UpdateSessionTitle(child) err = %v, want ErrNotFound", err)
	}
	// Wrong version for a child is still NotFound (no existence leak).
	if err := repo.UpdateSessionTitle(ctx, c, seedAgentVersion(t, db), "x"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("UpdateSessionTitle(child, other version) err = %v, want ErrNotFound", err)
	}
	var title sql.NullString
	if err := db.QueryRow(`SELECT title FROM chat_sessions WHERE id = $1::uuid`, c).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title.Valid {
		t.Fatalf("child title changed to %q", title.String)
	}
	if err := repo.UpdateSessionTitle(ctx, r, v, "root"); err != nil {
		t.Fatalf("UpdateSessionTitle(root): %v", err)
	}
}

func TestChatRepository_RootOnly_SetSessionHeaderOverrides(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	v := seedAgentVersion(t, db)
	r := insertChatRoot(t, db, v)
	c := insertChatChild(t, db, r, "call-1", v)
	repo := NewChatRepository(db)

	ovr := map[string]map[string]string{"b": {"X": "1"}}
	if err := repo.SetSessionHeaderOverrides(ctx, c, ovr); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("SetSessionHeaderOverrides(child) err = %v, want ErrNotFound", err)
	}
	got, err := repo.GetSessionHeaderOverrides(ctx, c)
	if err != nil {
		t.Fatalf("GetSessionHeaderOverrides: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("child overrides changed: %v", got)
	}
	if err := repo.SetSessionHeaderOverrides(ctx, r, ovr); err != nil {
		t.Fatalf("SetSessionHeaderOverrides(root): %v", err)
	}
}

func TestChatRepository_IsChildSession(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	v := seedAgentVersion(t, db)
	r := insertChatRoot(t, db, v)
	c := insertChatChild(t, db, r, "call-1", v)
	repo := NewChatRepository(db)

	for _, tc := range []struct {
		id   string
		want bool
	}{{c, true}, {r, false}, {uuid.NewString(), false}} {
		got, err := repo.IsChildSession(ctx, tc.id)
		if err != nil {
			t.Fatalf("IsChildSession(%s): %v", tc.id, err)
		}
		if got != tc.want {
			t.Fatalf("IsChildSession(%s) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestFeedbackRepository_MessageInRootSession(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	v := seedAgentVersion(t, db)
	r := insertChatRoot(t, db, v)
	r2 := insertChatRoot(t, db, v)
	c := insertChatChild(t, db, r, "call-1", v)
	mR := insertChatMessage(t, db, r, 1)
	mC := insertChatMessage(t, db, c, 1)
	repo := NewFeedbackRepository(db)

	for _, tc := range []struct {
		name, session, msg string
		want               bool
	}{
		{"root own message", r, mR, true},
		{"child message under root path", r, mC, false},
		{"other root's message", r2, mR, false},
		{"child path", c, mC, false},
	} {
		got, err := repo.MessageInRootSession(ctx, tc.session, tc.msg)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDataset_AssignSessionToDraft_ChildNotFound(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	v := seedAgentVersion(t, db)
	r := insertChatRoot(t, db, v)
	c := insertChatChild(t, db, r, "call-1", v)
	mC := insertChatMessage(t, db, c, 1)
	if _, err := db.Exec(`INSERT INTO chat_message_feedback (message_id, rating, judge_criteria)
		VALUES ($1::uuid, 'up', '["c"]'::jsonb)`, mC); err != nil {
		t.Fatalf("seed feedback: %v", err)
	}
	repo := NewDatasetRepository(db)
	d, err := repo.Create(ctx, "ds-"+uuid.NewString()[:8], "")
	if err != nil {
		t.Fatalf("Create dataset: %v", err)
	}
	if _, err := repo.AssignSessionToDraft(ctx, d.ID, c); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("AssignSessionToDraft(child) err = %v, want ErrNotFound", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dataset_version_sessions WHERE session_id = $1::uuid`, c).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("child assigned: %d rows", n)
	}
}

// --- deployed ---

type deployedTree struct {
	repo                  *DeployedRepository
	db                    *sql.DB
	agentID, versionID    string
	root, child, grandkid string
}

func newDeployedTree(t *testing.T) deployedTree {
	t.Helper()
	repo, versionRepo, agentID := newDeployedRepoTest(t)
	ctx := context.Background()
	versionID, err := versionRepo.CurrentDeployedID(ctx, agentID)
	if err != nil {
		t.Fatalf("CurrentDeployedID: %v", err)
	}
	root, err := repo.CreateSession(ctx, agentID, "user-A", "root")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	c := insertDeployedChild(t, repo.db, root.ID, "call-1", versionID)
	g := insertDeployedChild(t, repo.db, c, "call-2", versionID)
	return deployedTree{repo: repo, db: repo.db, agentID: agentID, versionID: versionID, root: root.ID, child: c, grandkid: g}
}

func TestDeployedRepository_RootOnly_GetAndList(t *testing.T) {
	tr := newDeployedTree(t)
	ctx := context.Background()

	if _, err := tr.repo.GetSession(ctx, tr.child, "user-A"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("GetSession(child) err = %v, want ErrNotFound", err)
	}
	got, err := tr.repo.GetSession(ctx, tr.root, "user-A")
	if err != nil || got.ID != tr.root || got.Depth != 0 || got.ParentSessionID != nil || got.AgentVersionID != nil {
		t.Fatalf("GetSession(root) = %+v, %v", got, err)
	}
	list, err := tr.repo.ListSessions(ctx, tr.agentID, "user-A")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 || list[0].ID != tr.root {
		t.Fatalf("ListSessions = %+v, want only root", list)
	}
}

func TestDeployedRepository_GetSessionByID_ChildExempt(t *testing.T) {
	tr := newDeployedTree(t)
	got, err := tr.repo.GetSessionByID(context.Background(), tr.child)
	if err != nil {
		t.Fatalf("GetSessionByID(child): %v", err)
	}
	if got.Depth != 1 || got.ParentSessionID == nil || *got.ParentSessionID != tr.root ||
		got.ParentToolCallID != "call-1" || got.AgentVersionID == nil || *got.AgentVersionID != tr.versionID {
		t.Fatalf("child = %+v", got)
	}
	if got.UserID != "user-A" || got.AgentID != tr.agentID {
		t.Fatalf("child scope = %+v", got)
	}
}

func TestDeployedRepository_RootOnly_UpdateSessionTitle(t *testing.T) {
	tr := newDeployedTree(t)
	ctx := context.Background()
	if err := tr.repo.UpdateSessionTitle(ctx, tr.agentID, tr.child, "user-A", "x"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("UpdateSessionTitle(child) err = %v, want ErrNotFound", err)
	}
	child, _ := tr.repo.GetSessionByID(ctx, tr.child)
	if child.Title != "" {
		t.Fatalf("child title changed: %q", child.Title)
	}
	if err := tr.repo.UpdateSessionTitle(ctx, uuid.NewString(), tr.root, "user-A", "x"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("UpdateSessionTitle(other agent) err = %v, want ErrNotFound", err)
	}
	if err := tr.repo.UpdateSessionTitle(ctx, tr.agentID, tr.root, "user-A", "renamed"); err != nil {
		t.Fatalf("UpdateSessionTitle(root): %v", err)
	}
}

func deployedExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var ok bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM deployed_sessions WHERE id = $1::uuid)`, id).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestDeployedRepository_RootOnly_DeleteSession(t *testing.T) {
	tr := newDeployedTree(t)
	ctx := context.Background()

	if err := tr.repo.DeleteSession(ctx, tr.agentID, tr.child, "user-A"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("DeleteSession(child) err = %v, want ErrNotFound", err)
	}
	if !deployedExists(t, tr.db, tr.child) {
		t.Fatal("child deleted")
	}
	otherAgentID, _ := seedAgent(t, tr.db)
	if err := tr.repo.DeleteSession(ctx, otherAgentID, tr.root, "user-A"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("DeleteSession(other agent) err = %v, want ErrNotFound", err)
	}
	if !deployedExists(t, tr.db, tr.root) {
		t.Fatal("root deleted under wrong agent")
	}

	// Messages + artifacts on child and grandchild must cascade with the root.
	for i, s := range []string{tr.child, tr.grandkid} {
		if err := tr.repo.AppendMessages(ctx, []types.DeployedMessage{{
			SessionID: s, AgentVersionID: tr.versionID, Role: types.ChatRoleUser,
			Content: json.RawMessage(`[]`), Seq: 1,
		}}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if _, err := tr.db.Exec(`
			INSERT INTO deployed_session_artifacts (session_id, origin, store_id, uri, mime, size_bytes, sha256)
			VALUES ($1::uuid, 'upload', 'local', 'blob://x', 'text/plain', 1, 'abc')`, s); err != nil {
			t.Fatalf("artifact %d: %v", i, err)
		}
	}

	if err := tr.repo.DeleteSession(ctx, tr.agentID, tr.root, "user-A"); err != nil {
		t.Fatalf("DeleteSession(root): %v", err)
	}
	for _, id := range []string{tr.root, tr.child, tr.grandkid} {
		if deployedExists(t, tr.db, id) {
			t.Fatalf("session %s survived delete", id)
		}
	}
	var msgs, arts int
	if err := tr.db.QueryRow(`SELECT COUNT(*) FROM deployed_messages WHERE session_id = ANY($1::uuid[])`,
		"{"+tr.child+","+tr.grandkid+"}").Scan(&msgs); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if err := tr.db.QueryRow(`SELECT COUNT(*) FROM deployed_session_artifacts WHERE session_id = ANY($1::uuid[])`,
		"{"+tr.child+","+tr.grandkid+"}").Scan(&arts); err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	if msgs != 0 || arts != 0 {
		t.Fatalf("descendant rows survived: messages=%d artifacts=%d", msgs, arts)
	}
}

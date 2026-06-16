package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

func newDeployedRepoTest(t *testing.T) (*DeployedRepository, *AgentRepository, string) {
	t.Helper()
	agentRepo, db := withRepo(t)
	ctx := context.Background()
	root, _ := agentRepo.Create(ctx, types.CreateAgentOpts{Name: "depl-" + uuid.NewString()[:8]})
	if _, err := db.ExecContext(ctx, `UPDATE agents SET status='READY' WHERE id=$1`, root.ID); err != nil {
		t.Fatalf("seed READY: %v", err)
	}
	if _, err := agentRepo.Deploy(ctx, root.ID); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	return NewDeployedRepository(db), agentRepo, root.ID
}

func TestDeployedRepository_CreateAndGetSession(t *testing.T) {
	repo, _, chainRoot := newDeployedRepoTest(t)
	ctx := context.Background()

	sess, err := repo.CreateSession(ctx, chainRoot, "user-A", "first")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.UserID != "user-A" || sess.AgentID != chainRoot {
		t.Fatalf("scope: %+v", sess)
	}

	got, err := repo.GetSession(ctx, sess.ID, "user-A")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != sess.ID {
		t.Fatalf("got %q want %q", got.ID, sess.ID)
	}
}

func TestDeployedRepository_GetSession_WrongUser_NotFound(t *testing.T) {
	repo, _, chainRoot := newDeployedRepoTest(t)
	ctx := context.Background()

	sess, _ := repo.CreateSession(ctx, chainRoot, "user-A", "")
	_, err := repo.GetSession(ctx, sess.ID, "user-B")
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("got %v want ErrNotFound", err)
	}
}

func TestDeployedRepository_ListSessions_ScopedByUser(t *testing.T) {
	repo, _, chainRoot := newDeployedRepoTest(t)
	ctx := context.Background()

	_, _ = repo.CreateSession(ctx, chainRoot, "user-A", "A1")
	_, _ = repo.CreateSession(ctx, chainRoot, "user-A", "A2")
	_, _ = repo.CreateSession(ctx, chainRoot, "user-B", "B1")

	got, err := repo.ListSessions(ctx, chainRoot, "user-A")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got))
	}
}

func TestDeployedRepository_AppendMessages_StampsVersion(t *testing.T) {
	repo, agentRepo, chainRoot := newDeployedRepoTest(t)
	ctx := context.Background()

	versionID, _ := agentRepo.CurrentDeployedVersionID(ctx, chainRoot)
	sess, _ := repo.CreateSession(ctx, chainRoot, "user-A", "")

	err := repo.AppendMessages(ctx, []types.DeployedMessage{
		{SessionID: sess.ID, AgentVersionID: versionID, Role: types.ChatRoleUser,
			Content: json.RawMessage(`[{"type":"text","text":"hi"}]`), Seq: 1},
		{SessionID: sess.ID, AgentVersionID: versionID, Role: types.ChatRoleAssistant,
			Content: json.RawMessage(`[{"type":"text","text":"hello"}]`), Seq: 2},
	})
	if err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	got, err := repo.LoadMessages(ctx, sess.ID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d msgs, want 2", len(got))
	}
	if got[0].AgentVersionID != versionID {
		t.Fatalf("got version %q, want %q", got[0].AgentVersionID, versionID)
	}
}

func TestDeployedRepository_DeleteSession_CascadesMessages(t *testing.T) {
	repo, agentRepo, chainRoot := newDeployedRepoTest(t)
	ctx := context.Background()
	versionID, _ := agentRepo.CurrentDeployedVersionID(ctx, chainRoot)
	sess, _ := repo.CreateSession(ctx, chainRoot, "user-A", "")
	_ = repo.AppendMessages(ctx, []types.DeployedMessage{
		{SessionID: sess.ID, AgentVersionID: versionID, Role: types.ChatRoleUser,
			Content: json.RawMessage(`[]`), Seq: 1},
	})

	if err := repo.DeleteSession(ctx, sess.ID, "user-A"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	_, err := repo.GetSession(ctx, sess.ID, "user-A")
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("expected NotFound after delete, got %v", err)
	}
	msgs, _ := repo.LoadMessages(ctx, sess.ID)
	if len(msgs) != 0 {
		t.Fatalf("messages survived delete: %d", len(msgs))
	}
}

func TestDeployedRepository_UpdateSessionTitle(t *testing.T) {
	repo, _, chainRoot := newDeployedRepoTest(t)
	ctx := context.Background()
	sess, _ := repo.CreateSession(ctx, chainRoot, "user-A", "")
	if err := repo.UpdateSessionTitle(ctx, sess.ID, "user-A", "Renamed"); err != nil {
		t.Fatalf("UpdateSessionTitle: %v", err)
	}
	got, _ := repo.GetSession(ctx, sess.ID, "user-A")
	if got.Title != "Renamed" {
		t.Fatalf("title: %q", got.Title)
	}
}

func TestDeployedRepository_NextSeq(t *testing.T) {
	repo, _, chainRoot := newDeployedRepoTest(t)
	ctx := context.Background()
	sess, _ := repo.CreateSession(ctx, chainRoot, "user-A", "")

	n, err := repo.NextSeq(ctx, sess.ID)
	if err != nil {
		t.Fatalf("NextSeq empty: %v", err)
	}
	if n != 1 {
		t.Fatalf("first seq = %d, want 1", n)
	}
}

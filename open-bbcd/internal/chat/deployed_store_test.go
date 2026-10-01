package chat

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// fakeDeployedRepo is a minimal in-memory impl of DeployedRepositoryAPI.
type fakeDeployedRepo struct {
	sessions map[string]*types.DeployedSession
	messages map[string][]*types.DeployedMessage // by session id
	toolRefs []llm.ArtifactRefBlock
}

func newFakeDeployedRepo() *fakeDeployedRepo {
	return &fakeDeployedRepo{
		sessions: map[string]*types.DeployedSession{},
		messages: map[string][]*types.DeployedMessage{},
	}
}

func (f *fakeDeployedRepo) GetSessionByID(ctx context.Context, sessionID string) (*types.DeployedSession, error) {
	s, ok := f.sessions[sessionID]
	if !ok {
		return nil, types.ErrNotFound
	}
	return s, nil
}

func (f *fakeDeployedRepo) AppendMessages(ctx context.Context, msgs []types.DeployedMessage) error {
	for _, m := range msgs {
		mc := m
		mc.CreatedAt = time.Now()
		f.messages[m.SessionID] = append(f.messages[m.SessionID], &mc)
	}
	return nil
}

func (f *fakeDeployedRepo) AppendUserTurn(ctx context.Context, m types.DeployedMessage) ([]llm.ArtifactRefBlock, error) {
	mc := m
	f.messages[m.SessionID] = append(f.messages[m.SessionID], &mc)
	return nil, nil
}

func (f *fakeDeployedRepo) AppendToolMessage(ctx context.Context, m types.DeployedMessage, refs []llm.ArtifactRefBlock) error {
	mc := m
	f.messages[m.SessionID] = append(f.messages[m.SessionID], &mc)
	f.toolRefs = append(f.toolRefs, refs...)
	return nil
}

func (f *fakeDeployedRepo) LoadMessages(ctx context.Context, sessionID string) ([]*types.DeployedMessage, error) {
	return f.messages[sessionID], nil
}

func (f *fakeDeployedRepo) NextSeq(ctx context.Context, sessionID string) (int, error) {
	return len(f.messages[sessionID]) + 1, nil
}

func TestDeployedChatStore_EnsureSession_ExistingSession(t *testing.T) {
	f := newFakeDeployedRepo()
	f.sessions["s1"] = &types.DeployedSession{ID: "s1", AgentID: "chain-A", UserID: "u"}
	store := NewDeployedChatStore(f)

	// Scope check is done upstream in the deployed handler — EnsureSession
	// here only confirms the row exists, regardless of the second arg.
	if err := store.EnsureSession(context.Background(), "s1", "ignored"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
}

func TestDeployedChatStore_EnsureSession_MissingSession_NotFound(t *testing.T) {
	f := newFakeDeployedRepo()
	store := NewDeployedChatStore(f)
	err := store.EnsureSession(context.Background(), "no-such-session", "ignored")
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDeployedChatStore_AppendMessages_StampsAgentVersionID(t *testing.T) {
	f := newFakeDeployedRepo()
	f.sessions["s1"] = &types.DeployedSession{ID: "s1", AgentID: "chain-A", UserID: "u"}
	store := NewDeployedChatStore(f)

	err := store.AppendMessages(context.Background(), "v-7", []types.ChatMessage{
		{SessionID: "s1", Role: types.ChatRoleUser, Content: json.RawMessage(`[]`), Seq: 1},
	})
	if err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	if f.messages["s1"][0].AgentVersionID != "v-7" {
		t.Fatalf("got %q want v-7", f.messages["s1"][0].AgentVersionID)
	}
}

func TestDeployedChatStore_AppendMessages_PassesID(t *testing.T) {
	f := newFakeDeployedRepo()
	store := NewDeployedChatStore(f)

	err := store.AppendMessages(context.Background(), "v-7", []types.ChatMessage{
		{ID: "m-asst", SessionID: "s1", Role: types.ChatRoleAssistant, Content: json.RawMessage(`[]`), Seq: 2},
	})
	if err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	if got := f.messages["s1"][0].ID; got != "m-asst" {
		t.Fatalf("got ID %q want m-asst", got)
	}
}

func TestDeployedChatStore_LoadMessages_TranslatesShape(t *testing.T) {
	f := newFakeDeployedRepo()
	f.messages["s1"] = []*types.DeployedMessage{
		{ID: "m1", SessionID: "s1", Role: types.ChatRoleUser, Content: json.RawMessage(`[]`), Seq: 1, AgentVersionID: "v-7"},
	}
	store := NewDeployedChatStore(f)

	got, err := store.LoadMessages(context.Background(), "s1")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(got) != 1 || got[0].ID != "m1" || got[0].Seq != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestDeployedChatStore_NewAppends_PassIDAndVersion(t *testing.T) {
	f := newFakeDeployedRepo()
	store := NewDeployedChatStore(f)
	ctx := context.Background()
	if _, err := store.AppendUserTurn(ctx, "v-7", types.ChatMessage{ID: "m1", SessionID: "s1", Role: types.ChatRoleUser, Content: json.RawMessage(`[]`), Seq: 1}); err != nil {
		t.Fatal(err)
	}
	ref := llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/x"}
	if err := store.AppendToolMessage(ctx, "v-7", types.ChatMessage{ID: "m2", SessionID: "s1", Role: types.ChatRoleTool, Content: json.RawMessage(`[]`), Seq: 2}, []llm.ArtifactRefBlock{ref}); err != nil {
		t.Fatal(err)
	}
	got := f.messages["s1"]
	if got[0].ID != "m1" || got[0].AgentVersionID != "v-7" || got[1].ID != "m2" || len(f.toolRefs) != 1 {
		t.Fatalf("got %+v refs %+v", got, f.toolRefs)
	}
}

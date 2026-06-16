package chat

import (
	"context"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// DeployedRepositoryAPI is the narrow subset of repository.DeployedRepository
// used by DeployedChatStore. Defined here so the chat package doesn't import
// the repository package's full surface.
type DeployedRepositoryAPI interface {
	GetSessionByID(ctx context.Context, sessionID string) (*types.DeployedSession, error)
	AppendMessages(ctx context.Context, msgs []types.DeployedMessage) error
	LoadMessages(ctx context.Context, sessionID string) ([]*types.DeployedMessage, error)
	NextSeq(ctx context.Context, sessionID string) (int, error)
}

// DeployedChatStore adapts DeployedRepository to the orchestrator's ChatStore
// interface. Sessions must already exist — EnsureSession only verifies
// existence (deployed runtime requires explicit POST /sessions before /turn).
type DeployedChatStore struct {
	repo DeployedRepositoryAPI
}

func NewDeployedChatStore(repo DeployedRepositoryAPI) *DeployedChatStore {
	return &DeployedChatStore{repo: repo}
}

// EnsureSession in the deployed runtime only verifies the session row exists.
// The deployed handler validates (session_id, user_id) and the session's
// agent_id BEFORE invoking the orchestrator, so the scope check is already
// done by the time this runs. The orchestrator's "agentID" parameter in the
// deployed runtime is actually a version row's id (the resolved currently-
// deployed version), which is why a literal sess.AgentID comparison would
// fail.
func (s *DeployedChatStore) EnsureSession(ctx context.Context, sessionID, _ string) error {
	_, err := s.repo.GetSessionByID(ctx, sessionID)
	return err
}

// LoadMessages translates DeployedMessage rows to ChatMessage shape for the
// orchestrator. AgentVersionID is dropped on the way out — the orchestrator
// doesn't read it, only the persisted rows on the way in carry it.
func (s *DeployedChatStore) LoadMessages(ctx context.Context, sessionID string) ([]*types.ChatMessage, error) {
	depl, err := s.repo.LoadMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]*types.ChatMessage, len(depl))
	for i, m := range depl {
		out[i] = &types.ChatMessage{
			ID:        m.ID,
			SessionID: m.SessionID,
			Role:      m.Role,
			Content:   m.Content,
			Seq:       m.Seq,
			CreatedAt: m.CreatedAt,
		}
	}
	return out, nil
}

// AppendMessages stamps each row with agentVersionID before persisting.
func (s *DeployedChatStore) AppendMessages(ctx context.Context, agentVersionID string, msgs []types.ChatMessage) error {
	depl := make([]types.DeployedMessage, len(msgs))
	for i, m := range msgs {
		depl[i] = types.DeployedMessage{
			SessionID:      m.SessionID,
			AgentVersionID: agentVersionID,
			Role:           m.Role,
			Content:        m.Content,
			Seq:            m.Seq,
		}
	}
	return s.repo.AppendMessages(ctx, depl)
}

func (s *DeployedChatStore) NextSeq(ctx context.Context, sessionID string) (int, error) {
	return s.repo.NextSeq(ctx, sessionID)
}

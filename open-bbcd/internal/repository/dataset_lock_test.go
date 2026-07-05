package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestAgentVersionDelete_RefusesWhenSessionInClosedDataset(t *testing.T) {
	agentRepo, versionRepo, db := withRepo(t)
	fb := NewFeedbackRepository(db)
	ds := NewDatasetRepository(db)

	// Seed an agent + version + session + assistant message with feedback,
	// assigned to a draft dataset, then close the draft (locks sessions).
	var agentID, versionID, sessionID, messageID string
	_ = db.QueryRow(`INSERT INTO agents (name) VALUES ('lock-a') RETURNING id::text`).Scan(&agentID)
	_ = db.QueryRow(`INSERT INTO agent_versions (agent_id, status) VALUES ($1::uuid, 'READY') RETURNING id::text`, agentID).Scan(&versionID)
	_ = db.QueryRow(`INSERT INTO chat_sessions (agent_version_id) VALUES ($1::uuid) RETURNING id::text`, versionID).Scan(&sessionID)
	_ = db.QueryRow(`INSERT INTO chat_messages (session_id, role, content, seq) VALUES ($1::uuid, 'assistant', '[]'::jsonb, 1) RETURNING id::text`, sessionID).Scan(&messageID)
	_ = fb.Upsert(context.Background(), messageID, types.FeedbackRatingUp, "", "", []string{"c"})
	dset, _ := ds.Create(context.Background(), "ds-lockdel", "")
	draft, _ := ds.AssignSessionToDraft(context.Background(), dset.ID, sessionID)
	_ = ds.CloseDraft(context.Background(), draft.ID, "")

	// Delete the version — must refuse with ErrSessionInDataset.
	if err := versionRepo.Delete(context.Background(), versionID); !errors.Is(err, types.ErrSessionInDataset) {
		t.Errorf("version delete err = %v, want ErrSessionInDataset", err)
	}
	// Delete the agent — must also refuse.
	if err := agentRepo.Delete(context.Background(), agentID); !errors.Is(err, types.ErrSessionInDataset) {
		t.Errorf("agent delete err = %v, want ErrSessionInDataset", err)
	}
}

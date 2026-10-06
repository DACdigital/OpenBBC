package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// TestMultiAgent_DeployedChildTranscript exercises GET
// /deployed/{agent_id}/sessions/{root_id}/children/{child_id} against the
// real repository (spec § REST — root-only rule and child transcripts).
func TestMultiAgent_DeployedChildTranscript(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newMultiAgentAPI(t, db)
	ctx := context.Background()

	agentID, depVID := seedAgentVersion(t, db, true)
	otherAgentID, _ := seedAgentVersion(t, db, true)
	deployed := repository.NewDeployedRepository(db)
	root, err := deployed.CreateSession(ctx, agentID, "u1", "root")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	otherRoot, err := deployed.CreateSession(ctx, agentID, "u1", "other")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	child := rawDeployedChild(t, db, root.ID, depVID)
	grandchild := rawDeployedChild(t, db, child, depVID)
	otherChild := rawDeployedChild(t, db, otherRoot.ID, depVID)
	if _, err := db.Exec(`
		INSERT INTO deployed_messages (session_id, agent_version_id, role, content, seq)
		VALUES ($1::uuid, $2::uuid, 'assistant',
		        '[{"type":"artifact_ref","store_id":"MAIN","uri":"sha256/`+strings.Repeat("0", 64)+`"}]'::jsonb, 1)`,
		child, depVID); err != nil {
		t.Fatalf("seed child message: %v", err)
	}

	url := func(agent, rootID, childID, user string) string {
		return "/deployed/" + agent + "/sessions/" + rootID + "/children/" + childID + "?user_id=" + user
	}

	for name, c := range map[string]struct {
		id, parent string
		depth      int
	}{"child": {child, root.ID, 1}, "grandchild": {grandchild, child, 2}} {
		rec := serve(api, http.MethodGet, url(agentID, root.ID, c.id, "u1"), "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		var got struct {
			Session  types.DeployedSession    `json:"session"`
			Messages []*types.DeployedMessage `json:"messages"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s decode: %v", name, err)
		}
		s := got.Session
		if s.ID != c.id || s.ParentSessionID == nil || *s.ParentSessionID != c.parent ||
			s.ParentToolCallID == "" || s.Depth != c.depth || s.AgentVersionID == nil || *s.AgentVersionID != depVID {
			t.Errorf("%s session = %+v", name, s)
		}
		if name == "child" {
			if len(got.Messages) != 1 || !strings.Contains(string(got.Messages[0].Content), `"artifact_ref"`) {
				t.Errorf("child messages = %s, want the artifact_ref block as data", rec.Body.String())
			}
		}
	}

	for name, target := range map[string]string{
		"wrong user":         url(agentID, root.ID, child, "u2"),
		"non-descendant":     url(agentID, root.ID, otherChild, "u1"),
		"root id is a child": url(agentID, child, grandchild, "u1"),
		"other agent":        url(otherAgentID, root.ID, child, "u1"),
		"child id is root":   url(agentID, root.ID, root.ID, "u1"),
	} {
		assertHandler404(t, name, serve(api, http.MethodGet, target, "", ""))
	}

	// Child artifact routes stay 404 even though the transcript lists the ref.
	assertHandler404(t, "child artifact GET", serve(api, http.MethodGet,
		"/deployed/"+agentID+"/sessions/"+child+"/artifacts/MAIN/sha256/"+strings.Repeat("0", 64)+"?user_id=u1", "", ""))
}

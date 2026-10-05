package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport/jsonl"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// deployedTree seeds root -> child -> grandchild for (agentID, "u1") plus an
// unrelated root (with its own child) of the same user.
type deployedTree struct {
	root, child, grandchild, otherRoot, otherChild string
}

func seedDeployedTree(store *stubDeployedStore, agentID string) deployedTree {
	add := func(parent *string, depth int) string {
		id := uuid.NewString()
		vid := uuid.NewString()
		sess := &types.DeployedSession{ID: id, AgentID: agentID, UserID: "u1", ParentSessionID: parent, Depth: depth}
		if parent != nil {
			sess.ParentToolCallID = "toolu_" + id[:8]
			sess.AgentVersionID = &vid
		}
		store.sessions[id] = sess
		return id
	}
	var tr deployedTree
	tr.root = add(nil, 0)
	tr.child = add(&tr.root, 1)
	tr.grandchild = add(&tr.child, 2)
	tr.otherRoot = add(nil, 0)
	tr.otherChild = add(&tr.otherRoot, 1)
	store.messages[tr.child] = []*types.DeployedMessage{{ID: "m-child", SessionID: tr.child, Role: "assistant",
		Content: json.RawMessage(`[{"type":"artifact_ref","store_id":"MAIN","uri":"sha256/x"}]`)}}
	return tr
}

func childURL(agentID, rootID, childID, userID string) string {
	u := "/deployed/" + agentID + "/sessions/" + rootID + "/children/" + childID
	if userID != "" {
		u += "?user_id=" + userID
	}
	return u
}

func TestDeployedChildSession_OK(t *testing.T) {
	store := newStubDeployedStore()
	tr := seedDeployedTree(store, testAgentID)
	mux := newDeployedMux(&stubDeployedAgentReader{deployedID: "v1"}, store, &stubTurnRunner{}, jsonl.NewFactory())

	for name, id := range map[string]string{"child": tr.child, "grandchild": tr.grandchild} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, childURL(testAgentID, tr.root, id, "u1"), nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: got %d body=%s", name, rr.Code, rr.Body.String())
		}
		var got struct {
			Session  map[string]any    `json:"session"`
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if got.Session["id"] != id {
			t.Errorf("%s: session id = %v", name, got.Session["id"])
		}
		for _, k := range []string{"parent_session_id", "parent_tool_call_id", "depth", "agent_version_id"} {
			if _, ok := got.Session[k]; !ok {
				t.Errorf("%s: session missing %q: %s", name, k, rr.Body.String())
			}
		}
		if name == "child" && len(got.Messages) != 1 {
			t.Errorf("child messages = %d, want 1 (artifact_ref returned as data)", len(got.Messages))
		}
	}
}

func TestDeployedChildSession_404s(t *testing.T) {
	store := newStubDeployedStore()
	tr := seedDeployedTree(store, testAgentID)
	otherAgent := uuid.NewString()
	mux := newDeployedMux(&stubDeployedAgentReader{deployedID: "v1"}, store, &stubTurnRunner{}, jsonl.NewFactory())

	cases := map[string]string{
		"wrong user":           childURL(testAgentID, tr.root, tr.child, "u2"),
		"non-descendant":       childURL(testAgentID, tr.root, tr.otherChild, "u1"),
		"root id is a child":   childURL(testAgentID, tr.child, tr.grandchild, "u1"),
		"child id is the root": childURL(testAgentID, tr.root, tr.root, "u1"),
		"other agent":          childURL(otherAgent, tr.root, tr.child, "u1"),
		"malformed child id":   childURL(testAgentID, tr.root, "not-a-uuid", "u1"),
		"unknown child id":     childURL(testAgentID, tr.root, uuid.NewString(), "u1"),
	}
	for name, target := range cases {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404: %s", name, rr.Code, rr.Body.String())
		}
	}

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, childURL(testAgentID, tr.root, tr.child, ""), nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing user_id: got %d, want 400", rr.Code)
	}

	none := newDeployedMux(&stubDeployedAgentReader{deployedID: ""}, store, &stubTurnRunner{}, jsonl.NewFactory())
	rr = httptest.NewRecorder()
	none.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, childURL(testAgentID, tr.root, tr.child, "u1"), nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("not deployed: got %d, want 404", rr.Code)
	}
}

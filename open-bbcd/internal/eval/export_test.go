package eval

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"gopkg.in/yaml.v3"
)

type fakeStore struct {
	eval     *types.Eval
	bundle   []byte
	refs     []*types.DatasetSessionRef
	msgs     map[string][]*types.ChatMessage
	feedback map[string]map[string]*types.ChatMessageFeedback
}

func (f *fakeStore) GetEval(_ context.Context, _ string) (*types.Eval, error) { return f.eval, nil }
func (f *fakeStore) GetBundle(_ context.Context, _ string) ([]byte, error)    { return f.bundle, nil }
func (f *fakeStore) GetSessionRefs(_ context.Context, _ string) ([]*types.DatasetSessionRef, error) {
	return f.refs, nil
}
func (f *fakeStore) GetMessages(_ context.Context, sid string) ([]*types.ChatMessage, error) {
	return f.msgs[sid], nil
}
func (f *fakeStore) GetFeedbackForSession(_ context.Context, sid string) (map[string]*types.ChatMessageFeedback, error) {
	return f.feedback[sid], nil
}

func TestExportBuild_ShapesPayload(t *testing.T) {
	fs := &fakeStore{
		eval:   &types.Eval{ID: "e-1", AgentVersionID: "av-1", DatasetVersionID: "dv-1"},
		bundle: []byte(`{"main_prompt":"hi","tools":[]}`),
		refs: []*types.DatasetSessionRef{
			{SessionID: "s-1", SessionTitle: "greet flow"},
		},
		msgs: map[string][]*types.ChatMessage{
			"s-1": {
				{ID: "m-1", Role: types.ChatRoleUser, Content: json.RawMessage(`[{"type":"text","text":"hi"}]`)},
				{ID: "m-2", Role: types.ChatRoleAssistant, Content: json.RawMessage(`[{"type":"text","text":"hello"}]`)},
			},
		},
		feedback: map[string]map[string]*types.ChatMessageFeedback{
			"s-1": {
				"m-2": {MessageID: "m-2", Rating: types.FeedbackRatingUp, JudgeCriteria: []string{"greets", "polite"}},
			},
		},
	}
	got, err := Build(context.Background(), fs, "e-1")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got.SchemaVersion != "eval-input-v1" || got.EvalID != "e-1" {
		t.Errorf("bad header: %+v", got)
	}
	if len(got.DatasetVersion.Sessions) != 1 || got.DatasetVersion.Sessions[0].SessionID != "s-1" {
		t.Errorf("session ref lost: %+v", got.DatasetVersion.Sessions)
	}
	if len(got.DatasetVersion.Sessions[0].Criteria) != 1 {
		t.Errorf("criteria count: %d, want 1", len(got.DatasetVersion.Sessions[0].Criteria))
	}
	if got.DatasetVersion.Sessions[0].Criteria[0].Items[0] != "greets" {
		t.Errorf("criterion item: %+v", got.DatasetVersion.Sessions[0].Criteria[0])
	}
	if _, err := yaml.Marshal(got); err != nil {
		t.Errorf("yaml.Marshal: %v", err)
	}
}

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

type stubBackend struct {
	name  string
	tools []llm.ToolDef
	calls map[string]Result
}

func (s *stubBackend) Name() string { return s.name }
func (s *stubBackend) Tools(ctx context.Context) ([]llm.ToolDef, error) {
	return s.tools, nil
}
func (s *stubBackend) Call(ctx context.Context, name string, input json.RawMessage) (Result, error) {
	return s.calls[name], nil
}

func TestComposite_MergesToolsAndAlwaysExposesSkillMetaTool(t *testing.T) {
	bundle := json.RawMessage(`{"skills":[{"name":"s1","prompt":"p1"}]}`)
	be := &stubBackend{name: "x", tools: []llm.ToolDef{{Name: "a"}}}
	h := NewComposite([]Backend{be})

	defs, err := h.Tools(bundle)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("want 2 tools (a + Skill), got %d", len(defs))
	}
	// First emitted tool must be Skill so the LLM sees it consistently.
	if defs[0].Name != "Skill" {
		t.Fatalf("Skill must be first, got %s", defs[0].Name)
	}
}

func TestComposite_RoutesCallToBackendByName(t *testing.T) {
	be := &stubBackend{
		name:  "x",
		tools: []llm.ToolDef{{Name: "a"}},
		calls: map[string]Result{"a": {Output: json.RawMessage(`"ok"`)}},
	}
	h := NewComposite([]Backend{be})
	r, err := h.Call(context.Background(), nil, Call{ID: "1", Name: "a", Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(r.Output) != `"ok"` {
		t.Fatalf("got %s", string(r.Output))
	}
}

func TestComposite_StripsBackendPrefixAndRoutes(t *testing.T) {
	be := &stubBackend{
		name:  "slack",
		tools: []llm.ToolDef{{Name: "slack__send_message"}},
		calls: map[string]Result{"send_message": {Output: json.RawMessage(`"ok"`)}},
	}
	h := NewComposite([]Backend{be})
	r, err := h.Call(context.Background(), nil, Call{ID: "1", Name: "slack__send_message", Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(r.Output) != `"ok"` {
		t.Fatalf("got %s", string(r.Output))
	}
}

func TestComposite_SkillMetaToolStillResolvesFromBundle(t *testing.T) {
	bundle := json.RawMessage(`{"skills":[{"name":"foo","prompt":"PROMPT"}]}`)
	h := NewComposite(nil)
	r, err := h.Call(context.Background(), bundle, Call{ID: "1", Name: "Skill", Input: json.RawMessage(`{"name":"foo"}`)})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(r.Output, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["prompt"] != "PROMPT" {
		t.Fatalf("got %v", got)
	}
}

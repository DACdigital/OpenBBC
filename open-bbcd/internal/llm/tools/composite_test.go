package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// prodSkills builds the skill list the way the orchestrator does in
// production: architecture.skills_meta joined with prompts.skill_prompts.
// There is no "skills" key on the architecture.
func prodSkills(t *testing.T, arch, prompts string) []types.BundleSkill {
	t.Helper()
	var a types.Architecture
	if err := json.Unmarshal([]byte(arch), &a); err != nil {
		t.Fatalf("arch: %v", err)
	}
	var p types.Prompts
	if err := json.Unmarshal([]byte(prompts), &p); err != nil {
		t.Fatalf("prompts: %v", err)
	}
	return types.JoinSkills(a, p)
}

const (
	prodArch    = `{"tools":[],"skills_meta":[{"name":"foo","description":"d"},{"name":"bar"},{"name":"empty"}]}`
	prodPrompts = `{"main_prompt":"M","skill_prompts":{"foo":"PROMPT","bar":"BAR","empty":"  "}}`
)

func skillEnum(t *testing.T, def llm.ToolDef) []string {
	t.Helper()
	var schema struct {
		Properties struct {
			Name struct {
				Enum []string `json:"enum"`
			} `json:"name"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "name" {
		t.Fatalf("required = %v", schema.Required)
	}
	return schema.Properties.Name.Enum
}

func skillCall(t *testing.T, h Handler, skills []types.BundleSkill, input string) (map[string]string, bool) {
	t.Helper()
	r, err := h.Call(context.Background(), nil, skills, Call{ID: "s1", Name: "Skill", Input: json.RawMessage(input)})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if r.ToolUseID != "s1" {
		t.Fatalf("ToolUseID = %q", r.ToolUseID)
	}
	var got map[string]string
	if err := json.Unmarshal(r.Output, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return got, r.IsError
}

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

func TestComposite_MergesToolsWithSkillFirst(t *testing.T) {
	be := &stubBackend{name: "x", tools: []llm.ToolDef{{Name: "a"}}}
	h := NewComposite([]Backend{be})

	defs, err := h.Tools(json.RawMessage(prodArch), prodSkills(t, prodArch, prodPrompts))
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("want 2 tools (Skill + a), got %d", len(defs))
	}
	// First emitted tool must be Skill so the LLM sees it consistently.
	if defs[0].Name != "Skill" {
		t.Fatalf("Skill must be first, got %s", defs[0].Name)
	}
	// Enum = loadable names in skills_meta order; "empty" (whitespace prompt) is not loadable.
	if got := skillEnum(t, defs[0]); len(got) != 2 || got[0] != "foo" || got[1] != "bar" {
		t.Fatalf("enum = %v, want [foo bar]", got)
	}
}

func TestComposite_NoLoadableSkills_OmitsSkillTool(t *testing.T) {
	be := &stubBackend{name: "x", tools: []llm.ToolDef{{Name: "a"}}}
	h := NewComposite([]Backend{be})
	for name, skills := range map[string][]types.BundleSkill{
		"nil":        nil,
		"only-empty": prodSkills(t, `{"skills_meta":[{"name":"e"}]}`, `{"main_prompt":"M","skill_prompts":{"e":""}}`),
		"no-prompts": prodSkills(t, prodArch, `{"main_prompt":"M"}`),
	} {
		defs, err := h.Tools(json.RawMessage(prodArch), skills)
		if err != nil {
			t.Fatalf("%s: Tools: %v", name, err)
		}
		if len(defs) != 1 || defs[0].Name != "a" {
			t.Fatalf("%s: want only [a], got %+v", name, defs)
		}
	}
}

func TestComposite_RoutesCallToBackendByName(t *testing.T) {
	be := &stubBackend{
		name:  "x",
		tools: []llm.ToolDef{{Name: "a"}},
		calls: map[string]Result{"a": {Output: json.RawMessage(`"ok"`)}},
	}
	h := NewComposite([]Backend{be})
	r, err := h.Call(context.Background(), nil, nil, Call{ID: "1", Name: "a", Input: json.RawMessage(`{}`)})
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
	r, err := h.Call(context.Background(), nil, nil, Call{ID: "1", Name: "slack__send_message", Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(r.Output) != `"ok"` {
		t.Fatalf("got %s", string(r.Output))
	}
}

func TestComposite_Skill_HitReturnsVersionPrompt(t *testing.T) {
	got, isErr := skillCall(t, NewComposite(nil), prodSkills(t, prodArch, prodPrompts), `{"name":"foo"}`)
	if isErr || got["prompt"] != "PROMPT" || len(got) != 1 {
		t.Fatalf("got %v isErr=%v, want {prompt: PROMPT}", got, isErr)
	}
}

func TestComposite_Skill_MissReturnsUnknownSkill(t *testing.T) {
	skills := prodSkills(t, prodArch, prodPrompts)
	for _, tc := range []struct {
		name   string
		skills []types.BundleSkill
		input  string
		want   string
	}{
		{"unknown name", skills, `{"name":"nope"}`, "unknown skill: nope"},
		{"empty prompt", skills, `{"name":"empty"}`, "unknown skill: empty"},
		{"prompt only in skill_prompts", prodSkills(t, prodArch, `{"main_prompt":"M","skill_prompts":{"orphan":"P"}}`), `{"name":"orphan"}`, "unknown skill: orphan"},
		// Skill not offered at all: still intercepted, never routed to a backend.
		{"no loadable skills", nil, `{"name":"foo"}`, "unknown skill: foo"},
	} {
		be := &stubBackend{name: "x", tools: []llm.ToolDef{{Name: "Skill"}}, calls: map[string]Result{"Skill": {Output: json.RawMessage(`"backend"`)}}}
		got, isErr := skillCall(t, NewComposite([]Backend{be}), tc.skills, tc.input)
		if !isErr || got["error"] != tc.want || len(got) != 1 {
			t.Fatalf("%s: got %v isErr=%v, want {error: %q}", tc.name, got, isErr, tc.want)
		}
	}
}

func TestComposite_Skill_MalformedInputIsError(t *testing.T) {
	_, err := NewComposite(nil).Call(context.Background(), nil, prodSkills(t, prodArch, prodPrompts), Call{ID: "s1", Name: "Skill", Input: json.RawMessage(`"foo"`)})
	if err == nil {
		t.Fatal("want error for non-object input")
	}
}

func TestComposite_RoutesMixedHTTPAndMCP(t *testing.T) {
	httpBE := &stubBackend{name: "api", tools: []llm.ToolDef{{Name: "orders_create"}}, calls: map[string]Result{"orders_create": {Output: json.RawMessage(`"http-ok"`)}}}
	mcpBE := &stubBackend{name: "slack", tools: []llm.ToolDef{{Name: "slack__send_message"}}, calls: map[string]Result{"send_message": {Output: json.RawMessage(`"mcp-ok"`)}}}

	h := NewComposite([]Backend{httpBE, mcpBE})

	defs, err := h.Tools(nil, prodSkills(t, prodArch, prodPrompts))
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(defs) != 3 {
		t.Fatalf("want 3 tools (Skill + 2 backends), got %d", len(defs))
	}

	// HTTP (no prefix) routing
	r, _ := h.Call(context.Background(), nil, nil, Call{ID: "1", Name: "orders_create", Input: json.RawMessage(`{}`)})
	if string(r.Output) != `"http-ok"` {
		t.Fatalf("http route: got %s", string(r.Output))
	}

	// MCP (prefixed) routing
	r, _ = h.Call(context.Background(), nil, nil, Call{ID: "2", Name: "slack__send_message", Input: json.RawMessage(`{}`)})
	if string(r.Output) != `"mcp-ok"` {
		t.Fatalf("mcp route: got %s", string(r.Output))
	}
}

type closingStubBackend struct {
	stubBackend
	closes int
	err    error
}

func (c *closingStubBackend) Close() error { c.closes++; return c.err }

func TestComposite_Close_ClosesCloserBackendsOnceAndJoinsErrors(t *testing.T) {
	boom := errors.New("boom")
	closer := &closingStubBackend{stubBackend: stubBackend{name: "mcp"}, err: boom}
	plain := &stubBackend{name: "http"}
	h := NewComposite([]Backend{plain, closer})

	err := h.Close()
	if closer.closes != 1 {
		t.Fatalf("closer.Close calls = %d, want 1", closer.closes)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("Close err = %v, want wrapping %v", err, boom)
	}
}

func TestComposite_Close_NoClosers_NilError(t *testing.T) {
	h := NewComposite([]Backend{&stubBackend{name: "http"}})
	if err := h.Close(); err != nil {
		t.Fatalf("Close err = %v, want nil", err)
	}
}

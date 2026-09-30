package chat

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"sync"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type fakeAgentRepo struct {
	version *types.AgentVersion
	agent   *types.Agent
	err     error
}

func (f *fakeAgentRepo) GetWithAgent(ctx context.Context, id string) (*types.AgentVersion, *types.Agent, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	agent := f.agent
	if agent == nil && f.version != nil {
		agent = &types.Agent{ID: f.version.AgentID}
	}
	return f.version, agent, nil
}

// fakeToolRow is one origin='tool_result' row recorded by AppendToolMessage.
type fakeToolRow struct {
	MessageID string
	Ref       llm.ArtifactRefBlock
}

type fakeChatRepo struct {
	mu       sync.Mutex
	ensured  map[string]string
	messages []types.ChatMessage
	nextSeq  int
	failRole types.ChatRole
	// pending: sessionID → pending uploads, in claim order. AppendUserTurn
	// consumes them (unless it returns ErrEmptyTurn).
	pending map[string][]llm.ArtifactRefBlock
	// toolRows: refs recorded by AppendToolMessage, in call order, with
	// the id of the tool message they were recorded against.
	toolRows []fakeToolRow
	// beforeClaim runs at the start of AppendUserTurn, outside the lock;
	// tests use it to simulate a DELETE racing the claim.
	beforeClaim func()
}

func (f *fakeChatRepo) EnsureSession(ctx context.Context, sessionID, scopeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ensured == nil {
		f.ensured = map[string]string{}
	}
	if cur, ok := f.ensured[sessionID]; ok && cur != scopeID {
		return types.ErrSessionAgentMismatch
	}
	f.ensured[sessionID] = scopeID
	return nil
}

func (f *fakeChatRepo) LoadMessages(ctx context.Context, sessionID string) ([]*types.ChatMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*types.ChatMessage
	for i := range f.messages {
		m := f.messages[i]
		if m.SessionID == sessionID {
			out = append(out, &m)
		}
	}
	return out, nil
}

func (f *fakeChatRepo) AppendMessages(ctx context.Context, agentVersionID string, msgs []types.ChatMessage) error {
	_ = agentVersionID
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range msgs {
		if f.failRole != "" && m.Role == f.failRole {
			return errors.New("fake: append failed")
		}
	}
	f.messages = append(f.messages, msgs...)
	return nil
}

func (f *fakeChatRepo) AppendUserTurn(ctx context.Context, agentVersionID string, msg types.ChatMessage) ([]llm.ArtifactRefBlock, error) {
	if f.beforeClaim != nil {
		f.beforeClaim()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRole == types.ChatRoleUser {
		return nil, errors.New("fake: append failed")
	}
	var raw []json.RawMessage
	_ = json.Unmarshal(msg.Content, &raw)
	blocks := parseBlocks(raw)
	claimed := f.pending[msg.SessionID]
	hasText := false
	for _, b := range blocks {
		if tb, ok := b.(llm.TextBlock); ok && tb.Text != "" {
			hasText = true
		}
	}
	if len(claimed) == 0 && !hasText {
		return nil, types.ErrEmptyTurn
	}
	delete(f.pending, msg.SessionID)
	for _, r := range claimed {
		blocks = append(blocks, r)
	}
	content, err := blocksToJSON(blocks)
	if err != nil {
		return nil, err
	}
	msg.Content = content
	f.messages = append(f.messages, msg)
	return claimed, nil
}

func (f *fakeChatRepo) AppendToolMessage(ctx context.Context, agentVersionID string, msg types.ChatMessage, refs []llm.ArtifactRefBlock) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRole == types.ChatRoleTool {
		return errors.New("fake: append failed")
	}
	f.messages = append(f.messages, msg)
	for _, r := range refs {
		f.toolRows = append(f.toolRows, fakeToolRow{MessageID: msg.ID, Ref: r})
	}
	return nil
}

func (f *fakeChatRepo) NextSeq(ctx context.Context, sessionID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextSeq++
	return f.nextSeq, nil
}

// fakeLLM emits a scripted event sequence. Each call to Generate consumes
// the next slice from `script` and yields its events. Every request is
// recorded in `requests` so tests can assert what the model was sent.
type fakeLLM struct {
	name     string
	script   [][]llm.Event
	calls    int
	requests []llm.Request
	// failCall: call index → error yielded instead of the script slot.
	failCall map[int]error
}

func (f *fakeLLM) Name() string { return f.name }

func (f *fakeLLM) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	f.requests = append(f.requests, req)
	return func(yield func(llm.Event, error) bool) {
		if err, ok := f.failCall[f.calls]; ok {
			f.calls++
			yield(nil, err)
			return
		}
		if f.calls >= len(f.script) {
			return
		}
		events := f.script[f.calls]
		f.calls++
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// fakeBuilder wraps a fakeTools (or any tools.Handler) so existing tests can
// satisfy the new ToolHandlerBuilder dependency without rewriting per-test
// fixture setup.
type fakeBuilder struct {
	handler tools.Handler
	err     error
}

func (f *fakeBuilder) Build(ctx context.Context, agentID, versionID string, architecture json.RawMessage) (tools.Handler, error) {
	return f.handler, f.err
}

// fakeTools returns one canned tool def and logs all calls.
type fakeTools struct {
	callLog []tools.Call
	results []tools.Result
}

func (f *fakeTools) Tools(bundle json.RawMessage) ([]llm.ToolDef, error) {
	return []llm.ToolDef{
		{Name: "Skill", Description: "x", InputSchema: []byte(`{"type":"object"}`)},
	}, nil
}

func (f *fakeTools) Call(ctx context.Context, bundle json.RawMessage, c tools.Call) (tools.Result, error) {
	f.callLog = append(f.callLog, c)
	if len(f.results) == 0 {
		return tools.Result{ToolUseID: c.ID, Output: []byte(`{}`)}, nil
	}
	res := f.results[0]
	f.results = f.results[1:]
	return res, nil
}

// recordingSink collects all sent events (without writing them anywhere).
type recordingSink struct {
	events []transport.Event
}

func (r *recordingSink) Send(_ context.Context, e transport.Event) error {
	r.events = append(r.events, e)
	return nil
}

func (r *recordingSink) Close() error { return nil }

// mmFakeLLM is a scripted fakeLLM that also renders image/png natively
// with a configurable budget.
type mmFakeLLM struct {
	*fakeLLM
	budget llm.RenderBudget
}

func (m *mmFakeLLM) NativeRenderBudget() llm.RenderBudget { return m.budget }
func (m *mmFakeLLM) SupportsNative(ref llm.ArtifactRefBlock) bool {
	return budgetLLM{}.SupportsNative(ref)
}
func (m *mmFakeLLM) RenderArtifactAsBlock(ctx context.Context, ref llm.ArtifactRefBlock, fetch llm.ArtifactFetcher) (llm.Block, error) {
	return budgetLLM{}.RenderArtifactAsBlock(ctx, ref, fetch)
}

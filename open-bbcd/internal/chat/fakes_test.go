package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sync"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/chat/chattest"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
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

func (f *fakeAgentRepo) ListSubAgentBindings(ctx context.Context, versionID string) ([]types.SubAgentBinding, error) {
	return nil, nil
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
	// nextSeq is per session so concurrent child turns cannot collide.
	nextSeq  map[string]int
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
	// userTurnErr, if set, is returned by AppendUserTurn (nothing persisted).
	userTurnErr error
	// userTurnErrFor, if set, overrides userTurnErr per session id.
	userTurnErrFor func(sessionID string) error

	// childCalls records CreateChildSession arguments in call order;
	// children maps parentID+"/"+toolCallID to the created child id.
	childCalls []childCall
	children   map[string]string
	// childErr, if set, is returned by CreateChildSession (no child).
	childErr error
}

// childCall is one recorded CreateChildSession call.
type childCall struct {
	RootID, ParentID, ToolCallID, TargetVersionID string
	ChildID                                       string
}

func (f *fakeChatRepo) CreateChildSession(ctx context.Context, rootID, parentID, parentToolCallID, targetVersionID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.childErr != nil {
		f.childCalls = append(f.childCalls, childCall{RootID: rootID, ParentID: parentID, ToolCallID: parentToolCallID, TargetVersionID: targetVersionID})
		return "", f.childErr
	}
	key := parentID + "/" + parentToolCallID
	if f.children == nil {
		f.children = map[string]string{}
	}
	if _, dup := f.children[key]; dup {
		return "", errors.New("fake: duplicate (parent_session_id, parent_tool_call_id)")
	}
	id := uuid.NewString()
	f.children[key] = id
	f.childCalls = append(f.childCalls, childCall{RootID: rootID, ParentID: parentID, ToolCallID: parentToolCallID, TargetVersionID: targetVersionID, ChildID: id})
	return id, nil
}

// checkSeqLocked enforces UNIQUE(session_id, seq) like the real tables.
// Caller holds f.mu.
func (f *fakeChatRepo) checkSeqLocked(msgs ...types.ChatMessage) error {
	for _, m := range msgs {
		for _, e := range f.messages {
			if e.SessionID == m.SessionID && e.Seq == m.Seq {
				return fmt.Errorf("fake: UNIQUE(session_id, seq) violation: %s/%d", m.SessionID, m.Seq)
			}
		}
	}
	return nil
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
	if err := f.checkSeqLocked(msgs...); err != nil {
		return err
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
	if f.userTurnErrFor != nil {
		if err := f.userTurnErrFor(msg.SessionID); err != nil {
			return nil, err
		}
	}
	if f.userTurnErr != nil {
		return nil, f.userTurnErr
	}
	if err := f.checkSeqLocked(msg); err != nil {
		return nil, err
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
	if err := f.checkSeqLocked(msg); err != nil {
		return err
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
	if f.nextSeq == nil {
		f.nextSeq = map[string]int{}
	}
	f.nextSeq[sessionID]++
	return f.nextSeq[sessionID], nil
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
	closes int
}

func (r *recordingSink) Send(_ context.Context, e transport.Event) error {
	r.events = append(r.events, e)
	return nil
}

func (r *recordingSink) Close() error { r.closes++; return nil }

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

// ---- multi-agent fakes (concurrency-safe) ----

// multiAgentRepo serves several versions (each with its own agent) and their
// sub-agent bindings. Concurrency-safe.
type multiAgentRepo struct {
	mu       sync.Mutex
	versions map[string]*types.AgentVersion
	agents   map[string]*types.Agent // by version id
	bindings map[string][]types.SubAgentBinding
}

func newMultiAgentRepo() *multiAgentRepo {
	return &multiAgentRepo{
		versions: map[string]*types.AgentVersion{},
		agents:   map[string]*types.Agent{},
		bindings: map[string][]types.SubAgentBinding{},
	}
}

// add registers versionID with main prompt system. A non-empty bindings
// list also turns the agent tool on; each binding is "name→target".
func (r *multiAgentRepo) add(versionID, system string, bindings ...types.SubAgentBinding) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prompts, _ := json.Marshal(map[string]string{"main_prompt": system})
	r.versions[versionID] = &types.AgentVersion{
		ID:               versionID,
		AgentID:          "agent-" + versionID,
		Prompts:          prompts,
		AgentToolEnabled: len(bindings) > 0,
	}
	r.agents[versionID] = &types.Agent{ID: "agent-" + versionID, Architecture: []byte(`{}`)}
	for i := range bindings {
		bindings[i].CallerVersionID = versionID
	}
	r.bindings[versionID] = bindings
}

func (r *multiAgentRepo) setArchitecture(versionID string, arch string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[versionID].Architecture = []byte(arch)
}

func (r *multiAgentRepo) setToolEnabled(versionID string, on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.versions[versionID].AgentToolEnabled = on
}

func (r *multiAgentRepo) GetWithAgent(ctx context.Context, versionID string) (*types.AgentVersion, *types.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.versions[versionID]
	if !ok {
		return nil, nil, types.ErrNotFound
	}
	vc, ac := *v, *r.agents[versionID]
	return &vc, &ac, nil
}

func (r *multiAgentRepo) ListSubAgentBindings(ctx context.Context, versionID string) ([]types.SubAgentBinding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]types.SubAgentBinding(nil), r.bindings[versionID]...), nil
}

func bind(name, target, note string) types.SubAgentBinding {
	return types.SubAgentBinding{Name: name, TargetVersionID: target, Note: note}
}

// The scripted multi-agent LLM lives in chattest (shared with the handler
// integration tests); these aliases keep the chat tests terse.
type (
	llmStep      = chattest.Step
	routedLLM    = chattest.RoutedLLM
	toolCallSpec = chattest.ToolCall
)

var (
	newRoutedLLM  = chattest.NewRoutedLLM
	textStep      = chattest.TextStep
	echoStep      = chattest.EchoStep
	firstUserText = chattest.FirstUserText
	agentCall     = chattest.AgentCall
	callsStep     = chattest.CallsStep
)

// multiBuilder builds a fresh closable handler per Build call and records
// the build contexts. Concurrency-safe.
type multiBuilder struct {
	mu       sync.Mutex
	handlers []*multiHandler
	builds   []builtCtx
	// override: versionID → handler returned instead of a multiHandler.
	override map[string]tools.Handler
	// result: tool name → result producer (default `{}`).
	result func(versionID string, c tools.Call) tools.Result
	// onCall runs on every handler Call (outside the lock).
	onCall func(versionID, name string)
}

type builtCtx struct {
	versionID string
	ctx       context.Context
}

func (b *multiBuilder) Build(ctx context.Context, agentID, versionID string, architecture json.RawMessage) (tools.Handler, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.builds = append(b.builds, builtCtx{versionID: versionID, ctx: ctx})
	if h, ok := b.override[versionID]; ok {
		return h, nil
	}
	h := &multiHandler{b: b, versionID: versionID}
	b.handlers = append(b.handlers, h)
	return h, nil
}

func (b *multiBuilder) allHandlers() []*multiHandler {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*multiHandler(nil), b.handlers...)
}

type multiHandler struct {
	b         *multiBuilder
	versionID string
	mu        sync.Mutex
	calls     []tools.Call
	closes    int
}

func (h *multiHandler) Tools(json.RawMessage) ([]llm.ToolDef, error) {
	return []llm.ToolDef{
		{Name: "Skill", Description: "skills", InputSchema: []byte(`{"type":"object"}`)},
		{Name: "search", Description: "search", InputSchema: []byte(`{"type":"object"}`)},
	}, nil
}

func (h *multiHandler) Call(ctx context.Context, _ json.RawMessage, c tools.Call) (tools.Result, error) {
	h.mu.Lock()
	h.calls = append(h.calls, c)
	h.mu.Unlock()
	if h.b.onCall != nil {
		h.b.onCall(h.versionID, c.Name)
	}
	if h.b.result != nil {
		return h.b.result(h.versionID, c), nil
	}
	return tools.Result{ToolUseID: c.ID, Output: []byte(`{}`)}, nil
}

func (h *multiHandler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closes++
	return nil
}

// spyRunner wraps a subAgentRunner and records every request and outcome.
type spyRunner struct {
	inner   subAgentRunner
	mu      sync.Mutex
	reqs    []subAgentRequest
	results []subAgentResult
	errs    []error
}

func (s *spyRunner) Run(ctx context.Context, req subAgentRequest) (subAgentResult, error) {
	res, err := s.inner.Run(ctx, req)
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.results = append(s.results, res)
	s.errs = append(s.errs, err)
	s.mu.Unlock()
	return res, err
}

// syncRecordingSink is a recordingSink safe for concurrent Send.
type syncRecordingSink struct {
	mu     sync.Mutex
	events []transport.Event
}

func (r *syncRecordingSink) Send(_ context.Context, e transport.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *syncRecordingSink) Close() error { return nil }

func (r *syncRecordingSink) snapshot() []transport.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]transport.Event(nil), r.events...)
}

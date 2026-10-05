package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport/agui"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

const (
	sysRoot = "root-sys"
	sysW    = "worker-sys"
	sysX    = "x-sys"
	sysY    = "y-sys"
)

// maEnv is one orchestrator over the concurrency-safe multi-agent fakes,
// with a spy around the production runner.
type maEnv struct {
	repo    *multiAgentRepo
	chats   *fakeChatRepo
	llm     *routedLLM
	builder *multiBuilder
	o       *Orchestrator
	spy     *spyRunner
}

func newMAEnv(t *testing.T) *maEnv {
	t.Helper()
	return newMAEnvWithLogger(t, testLogger())
}

func newMAEnvWithLogger(t *testing.T, logger *slog.Logger) *maEnv {
	t.Helper()
	e := &maEnv{
		repo:    newMultiAgentRepo(),
		chats:   &fakeChatRepo{},
		llm:     newRoutedLLM(),
		builder: &multiBuilder{},
	}
	e.o = NewOrchestrator(e.repo, e.chats, e.llm, e.builder, logger)
	e.spy = &spyRunner{inner: e.o.runner}
	e.o.runner = e.spy
	return e
}

// turn runs a root turn of version vR on session R.
func (e *maEnv) turn(ctx context.Context, sink transport.Sink) (string, error) {
	return e.o.Turn(ctx, "vR", "R", []llm.Block{llm.TextBlock{Text: "go"}}, sink, TurnOpts{})
}

func (e *maEnv) childCalls() []childCall {
	e.chats.mu.Lock()
	defer e.chats.mu.Unlock()
	return append([]childCall(nil), e.chats.childCalls...)
}

func (e *maEnv) msgs(sessionID string) []*types.ChatMessage {
	m, _ := e.chats.LoadMessages(context.Background(), sessionID)
	return m
}

func blocksOf(m *types.ChatMessage) []llm.Block {
	var raw []json.RawMessage
	_ = json.Unmarshal(m.Content, &raw)
	return parseBlocks(raw)
}

// toolUses returns every tool_use block of the session's assistant
// messages, in order.
func (e *maEnv) toolUses(sessionID string) []llm.ToolUseBlock {
	var out []llm.ToolUseBlock
	for _, m := range e.msgs(sessionID) {
		if m.Role != types.ChatRoleAssistant {
			continue
		}
		for _, b := range blocksOf(m) {
			if tu, ok := b.(llm.ToolUseBlock); ok {
				out = append(out, tu)
			}
		}
	}
	return out
}

// toolMsgs returns the session's tool-role messages in order.
func (e *maEnv) toolMsgs(sessionID string) []*types.ChatMessage {
	var out []*types.ChatMessage
	for _, m := range e.msgs(sessionID) {
		if m.Role == types.ChatRoleTool {
			out = append(out, m)
		}
	}
	return out
}

func resultsOf(m *types.ChatMessage) []llm.ToolResultBlock {
	var out []llm.ToolResultBlock
	for _, b := range blocksOf(m) {
		if r, ok := b.(llm.ToolResultBlock); ok {
			out = append(out, r)
		}
	}
	return out
}

func hasArtifactRef(m *types.ChatMessage) bool {
	for _, b := range blocksOf(m) {
		if _, ok := b.(llm.ArtifactRefBlock); ok {
			return true
		}
	}
	return false
}

// resultString decodes an agent tool result (always a JSON string).
func resultString(t *testing.T, r llm.ToolResultBlock) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(r.Result, &s); err != nil {
		t.Fatalf("agent result is not a JSON string: %s", r.Result)
	}
	return s
}

// lastText concatenates the text blocks of the session's last assistant message.
func (e *maEnv) lastText(sessionID string) string {
	var last *types.ChatMessage
	for _, m := range e.msgs(sessionID) {
		if m.Role == types.ChatRoleAssistant {
			last = m
		}
	}
	if last == nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocksOf(last) {
		if tb, ok := b.(llm.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

// requestToolResults returns the tool_result blocks of the request's last
// tool-role message.
func requestToolResults(req llm.Request) []llm.ToolResultBlock {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != llm.RoleTool {
			continue
		}
		var out []llm.ToolResultBlock
		for _, b := range req.Messages[i].Content {
			if r, ok := b.(llm.ToolResultBlock); ok {
				out = append(out, r)
			}
		}
		return out
	}
	return nil
}

// standardRoot wires vR (binding researcher → vW) and vW (a plain worker).
func (e *maEnv) standardRoot() {
	e.repo.add("vR", sysRoot, bind("researcher", "vW", "finds facts"))
	e.repo.add("vW", sysW)
}

// 1. Exposure.
func TestMultiAgent_Exposure(t *testing.T) {
	agentDef := func(t *testing.T, e *maEnv) (int, *llm.ToolDef) {
		t.Helper()
		reqs := e.llm.reqs(sysRoot)
		if len(reqs) == 0 {
			t.Fatal("root LLM never called")
		}
		idx, n := -1, 0
		var def *llm.ToolDef
		for i, d := range reqs[0].Tools {
			if d.Name == tools.AgentToolName {
				n++
				idx = i
				dd := d
				def = &dd
			}
		}
		if n > 1 {
			t.Fatalf("%d agent defs, want at most 1", n)
		}
		return idx, def
	}
	run := func(t *testing.T, e *maEnv) {
		t.Helper()
		e.llm.route(sysRoot, textStep("hi"))
		if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
			t.Fatalf("Turn: %v", err)
		}
	}

	t.Run("tool off", func(t *testing.T) {
		e := newMAEnv(t)
		e.repo.add("vR", sysRoot, bind("researcher", "vW", "n"))
		e.repo.setToolEnabled("vR", false)
		run(t, e)
		if idx, _ := agentDef(t, e); idx != -1 {
			t.Fatal("agent tool exposed with agent_tool_enabled=false")
		}
	})
	t.Run("on, zero bindings", func(t *testing.T) {
		e := newMAEnv(t)
		e.repo.add("vR", sysRoot)
		e.repo.setToolEnabled("vR", true)
		run(t, e)
		if idx, _ := agentDef(t, e); idx != -1 {
			t.Fatal("agent tool exposed with zero bindings")
		}
	})
	t.Run("two bindings", func(t *testing.T) {
		e := newMAEnv(t)
		e.repo.add("vR", sysRoot, bind("writer", "vW2", "writes prose"), bind("researcher", "vW", "finds facts"))
		run(t, e)
		idx, def := agentDef(t, e)
		if idx != 1 {
			t.Fatalf("agent def at index %d, want 1 (right after Skill)", idx)
		}
		if e.llm.reqs(sysRoot)[0].Tools[0].Name != "Skill" {
			t.Fatal("Skill is not first")
		}
		var schema struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(schema.Properties["subagent"].Enum, ","); got != "researcher,writer" {
			t.Fatalf("enum = %q", got)
		}
		if _, ok := schema.Properties["artifacts"]; ok {
			t.Fatal("schema must have no artifacts property")
		}
		if !strings.Contains(def.Description, "finds facts") || !strings.Contains(def.Description, "writes prose") {
			t.Fatalf("description lacks notes: %q", def.Description)
		}
	})
	t.Run("endpoint name collision", func(t *testing.T) {
		var buf syncBuffer
		e := newMAEnvWithLogger(t, slog.New(slog.NewTextHandler(&buf, nil)))
		e.repo.add("vR", sysRoot, bind("researcher", "vW", "n"))
		e.repo.setArchitecture("vR", `{"tools":[{"name":"agent"}]}`)
		run(t, e)
		if idx, _ := agentDef(t, e); idx != -1 {
			t.Fatal("agent tool exposed despite endpoint collision")
		}
		if out := buf.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "agent tool omitted") {
			t.Fatalf("no collision warning logged: %s", out)
		}
	})
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// 2. Root delegation.
func TestMultiAgent_RootDelegation(t *testing.T) {
	e := newMAEnv(t)
	e.standardRoot()
	e.llm.route(sysRoot, callsStep("", agentCall("researcher", "look up", "P")), textStep("root done"))
	e.llm.route(sysW, textStep("W says hi"))

	stop, err := e.turn(context.Background(), &syncRecordingSink{})
	if err != nil || stop != "end_turn" {
		t.Fatalf("Turn = %q, %v", stop, err)
	}

	uses := e.toolUses("R")
	if len(uses) != 1 {
		t.Fatalf("root tool_uses = %d", len(uses))
	}
	calls := e.childCalls()
	if len(calls) != 1 {
		t.Fatalf("CreateChildSession calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.RootID != "R" || c.ParentID != "R" || c.ToolCallID != uses[0].ID || c.TargetVersionID != "vW" {
		t.Fatalf("CreateChildSession(%+v), want (R, R, %s, vW)", c, uses[0].ID)
	}

	childMsgs := e.msgs(c.ChildID)
	if len(childMsgs) == 0 || childMsgs[0].Role != types.ChatRoleUser || string(childMsgs[0].Content) != `[{"text":"P","type":"text"}]` {
		t.Fatalf("child first message = %+v", childMsgs)
	}
	wReqs := e.llm.reqs(sysW)
	if len(wReqs) != 1 || len(wReqs[0].Messages) != 1 || firstUserText(wReqs[0]) != "P" {
		t.Fatalf("worker request must hold only the prompt: %+v", wReqs)
	}

	tms := e.toolMsgs("R")
	if len(tms) != 1 {
		t.Fatalf("root tool messages = %d", len(tms))
	}
	rs := resultsOf(tms[0])
	if len(rs) != 1 || rs[0].IsError || string(rs[0].Result) != `"W says hi"` || rs[0].ToolUseID != uses[0].ID {
		t.Fatalf("root tool result = %+v (%s)", rs, rs[0].Result)
	}
	if hasArtifactRef(tms[0]) {
		t.Fatal("agent tool message must carry no artifact_ref")
	}

	if len(e.spy.reqs) != 1 || e.spy.reqs[0].ParentDepth != 0 || e.spy.reqs[0].RootSessionID != "R" || e.spy.reqs[0].ParentSessionID != "R" {
		t.Fatalf("runner request = %+v", e.spy.reqs)
	}
	if e.spy.results[0].ChildSessionID != c.ChildID || e.spy.results[0].StopReason != "end_turn" {
		t.Fatalf("runner result = %+v", e.spy.results[0])
	}
	if got := e.lastText("R"); got != "root done" {
		t.Fatalf("root final text = %q", got)
	}
}

// nestedEnv: R --researcher--> W --x--> X.
func nestedEnv(t *testing.T) *maEnv {
	e := newMAEnv(t)
	e.repo.add("vR", sysRoot, bind("researcher", "vW", ""))
	e.repo.add("vW", sysW, bind("x", "vX", ""))
	e.repo.add("vX", sysX)
	e.llm.route(sysRoot, callsStep("", agentCall("researcher", "d", "do W")), textStep("root done"))
	e.llm.route(sysW, callsStep("", agentCall("x", "d", "do X")), textStep("W done"))
	e.llm.route(sysX, textStep("X done"))
	return e
}

// 3. Nesting.
func TestMultiAgent_Nesting(t *testing.T) {
	e := nestedEnv(t)
	if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
		t.Fatal(err)
	}
	calls := e.childCalls()
	if len(calls) != 2 {
		t.Fatalf("children = %d", len(calls))
	}
	w := calls[0]
	wUses := e.toolUses(w.ChildID)
	if len(wUses) != 1 {
		t.Fatalf("W tool_uses = %d", len(wUses))
	}
	x := calls[1]
	if x.RootID != "R" || x.ParentID != w.ChildID || x.ToolCallID != wUses[0].ID || x.TargetVersionID != "vX" {
		t.Fatalf("grandchild CreateChildSession = %+v", x)
	}
	var depths []int
	for _, r := range e.spy.reqs {
		depths = append(depths, r.ParentDepth)
	}
	// the grandchild call comes from W at depth 1 (it finishes first).
	if len(depths) != 2 || depths[0] != 1 || depths[1] != 0 {
		t.Fatalf("parent depths = %v, want [1 0]", depths)
	}
	if got := resultString(t, resultsOf(e.toolMsgs(w.ChildID)[0])[0]); got != "X done" {
		t.Fatalf("W's agent result = %q", got)
	}
	if got := resultString(t, resultsOf(e.toolMsgs("R")[0])[0]); got != "W done" {
		t.Fatalf("root agent result = %q", got)
	}
}

// 4. Depth cap.
func TestMultiAgent_DepthCap(t *testing.T) {
	e := newMAEnv(t)
	e.o.MaxDepth = 2
	e.repo.add("vR", sysRoot, bind("researcher", "vW", ""))
	e.repo.add("vW", sysW, bind("x", "vX", ""))
	e.repo.add("vX", sysX, bind("y", "vY", ""))
	e.repo.add("vY", sysY)
	e.llm.route(sysRoot, callsStep("", agentCall("researcher", "d", "do W")), textStep("root done"))
	e.llm.route(sysW, callsStep("", agentCall("x", "d", "do X")), textStep("W done"))
	e.llm.route(sysX, callsStep("", agentCall("y", "d", "do Y")), textStep("X final"))
	e.llm.route(sysY, textStep("never"))

	if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.childCalls()); n != 2 {
		t.Fatalf("children = %d, want 2 (no session for the refused call)", n)
	}
	if len(e.llm.reqs(sysY)) != 0 {
		t.Fatal("refused sub-agent ran")
	}
	xReqs := e.llm.reqs(sysX)
	if len(xReqs) != 2 {
		t.Fatalf("X LLM calls = %d, want 2 (continues after the error)", len(xReqs))
	}
	rs := requestToolResults(xReqs[1])
	if len(rs) != 1 || !rs[0].IsError || !strings.HasPrefix(resultString(t, rs[0]), "max_depth_exceeded: ") {
		t.Fatalf("X tool result = %+v", rs)
	}
	if got := resultString(t, resultsOf(e.toolMsgs("R")[0])[0]); got != "W done" {
		t.Fatalf("root result = %q", got)
	}
}

// 5. Unknown sub-agent / invalid input.
func TestMultiAgent_UnknownAndInvalid(t *testing.T) {
	cases := []struct {
		name, input, prefix string
	}{
		{"unknown", mustAgentInput("nobody", "d", "p"), "unknown_subagent: "},
		{"missing prompt", `{"subagent":"researcher","description":"d"}`, "invalid_input: "},
		{"unparsable", `{bad`, "invalid_input: tool input is not valid JSON"},
		{"truncated", `{"subagent":"researcher","descr`, "invalid_input: tool input is not valid JSON"},
		{"not an object", `["researcher"]`, "invalid_input: "},
		{"unknown property", `{"subagent":"researcher","description":"d","prompt":"p","x":1}`, "invalid_input: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newMAEnv(t)
			e.standardRoot()
			e.llm.route(sysRoot, callsStep("", toolCallSpec{Name: tools.AgentToolName, Input: tc.input}), textStep("root done"))
			e.llm.route(sysW, textStep("never"))
			if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
				t.Fatal(err)
			}
			if n := len(e.childCalls()); n != 0 {
				t.Fatalf("children = %d", n)
			}
			reqs := e.llm.reqs(sysRoot)
			if len(reqs) != 2 {
				t.Fatalf("root LLM calls = %d", len(reqs))
			}
			rs := requestToolResults(reqs[1])
			if len(rs) != 1 || !rs[0].IsError || !strings.HasPrefix(resultString(t, rs[0]), tc.prefix) {
				t.Fatalf("result = %+v", rs)
			}
			if e.lastText("R") != "root done" {
				t.Fatal("root did not continue")
			}
		})
	}
}

func mustAgentInput(sub, desc, prompt string) string {
	return agentCall(sub, desc, prompt).Input
}

// 6. Parallel fan-out.
func TestMultiAgent_ParallelFanOut(t *testing.T) {
	e := newMAEnv(t)
	e.o.MaxParallel = 4
	e.standardRoot()
	var specs []toolCallSpec
	for i := 0; i < 6; i++ {
		specs = append(specs, agentCall("researcher", "d", "P"+string(rune('0'+i))))
	}
	e.llm.route(sysRoot, callsStep("", specs...), textStep("root done"))
	e.llm.route(sysW, echoStep("echo:")).delay = 50 * time.Millisecond

	if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
		t.Fatal(err)
	}
	if m := e.llm.max(sysW); m > 4 || m < 2 {
		t.Fatalf("max concurrent children = %d, want 2..4", m)
	}
	if n := len(e.childCalls()); n != 6 {
		t.Fatalf("children = %d", n)
	}
	uses := e.toolUses("R")
	rs := resultsOf(e.toolMsgs("R")[0])
	if len(rs) != 6 {
		t.Fatalf("results = %d", len(rs))
	}
	for i, r := range rs {
		if r.ToolUseID != uses[i].ID || r.IsError || resultString(t, r) != "echo:P"+string(rune('0'+i)) {
			t.Fatalf("result %d = %+v (%s)", i, r, r.Result)
		}
	}
	for _, h := range e.builder.allHandlers() {
		for _, c := range h.calls {
			if c.Name == tools.AgentToolName {
				t.Fatal("agent call went through the tool handler")
			}
		}
	}
}

// 7. Mixed step.
func TestMultiAgent_MixedStep(t *testing.T) {
	e := newMAEnv(t)
	e.standardRoot()
	var mu sync.Mutex
	var log []string
	e.builder.onCall = func(v, name string) {
		mu.Lock()
		log = append(log, v+":"+name)
		mu.Unlock()
	}
	e.llm.onCall = func(system string) {
		mu.Lock()
		log = append(log, "llm:"+system)
		mu.Unlock()
	}
	e.llm.route(sysRoot, callsStep("",
		toolCallSpec{Name: "Skill", Input: `{"name":"s"}`},
		agentCall("researcher", "d", "P"),
		toolCallSpec{Name: "search", Input: `{"q":"x"}`},
	), textStep("root done"))
	e.llm.route(sysW, textStep("W"))

	if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"llm:" + sysRoot, "vR:Skill", "vR:search", "llm:" + sysW, "llm:" + sysRoot}
	if strings.Join(log, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", log, want)
	}
	uses := e.toolUses("R")
	rs := resultsOf(e.toolMsgs("R")[0])
	if len(rs) != 3 {
		t.Fatalf("results = %d", len(rs))
	}
	for i := range rs {
		if rs[i].ToolUseID != uses[i].ID {
			t.Fatalf("result %d answers %s, want %s", i, rs[i].ToolUseID, uses[i].ID)
		}
	}
	if resultString(t, rs[1]) != "W" {
		t.Fatalf("agent result = %s", rs[1].Result)
	}
}

// 8. Child failure.
func TestMultiAgent_ChildFailure(t *testing.T) {
	e := newMAEnv(t)
	e.standardRoot()
	e.llm.route(sysRoot, callsStep("", agentCall("researcher", "d", "P")), textStep("root done"))
	e.llm.route(sysW, textStep("x")).failAt = map[int]error{0: errors.New("boom")}

	if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.childCalls()); n != 1 {
		t.Fatalf("children = %d", n)
	}
	rs := resultsOf(e.toolMsgs("R")[0])
	if len(rs) != 1 || !rs[0].IsError || !strings.HasPrefix(resultString(t, rs[0]), "subagent_failed: ") {
		t.Fatalf("result = %+v", rs)
	}
	if e.lastText("R") != "root done" {
		t.Fatal("root did not continue")
	}
}

// 9. Child round cap.
func TestMultiAgent_ChildRoundCap(t *testing.T) {
	e := newMAEnv(t)
	e.o.MaxToolRounds = 2
	e.standardRoot()
	e.llm.route(sysRoot, callsStep("", agentCall("researcher", "d", "P")), textStep("root done"))
	e.llm.route(sysW, callsStep("thinking", toolCallSpec{Name: "search", Input: `{}`}))

	if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
		t.Fatal(err)
	}
	rs := resultsOf(e.toolMsgs("R")[0])
	if len(rs) != 1 || !rs[0].IsError || resultString(t, rs[0]) != "subagent_max_tool_rounds: thinking" {
		t.Fatalf("result = %+v (%s)", rs, rs[0].Result)
	}
	if e.spy.results[0].StopReason != "max_tool_rounds" {
		t.Fatalf("stop reason = %q", e.spy.results[0].StopReason)
	}
	if e.lastText("R") != "root done" {
		t.Fatal("root did not continue")
	}
}

// 10. Locked root, causes (a) and (b).
func TestMultiAgent_SessionLocked(t *testing.T) {
	t.Run("a: CreateChildSession refuses", func(t *testing.T) {
		e := newMAEnv(t)
		e.standardRoot()
		e.chats.childErr = types.ErrSessionLocked
		e.llm.route(sysRoot, callsStep("", agentCall("researcher", "d", "P")), textStep("root done"))
		e.llm.route(sysW, textStep("never"))
		if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
			t.Fatal(err)
		}
		if len(e.llm.reqs(sysW)) != 0 {
			t.Fatal("child turn ran")
		}
		rs := resultsOf(e.toolMsgs("R")[0])
		if len(rs) != 1 || !rs[0].IsError || !strings.HasPrefix(resultString(t, rs[0]), "session_locked: ") {
			t.Fatalf("result = %+v", rs)
		}
	})
	t.Run("b: child AppendUserTurn refuses", func(t *testing.T) {
		e := newMAEnv(t)
		e.standardRoot()
		e.chats.userTurnErrFor = func(id string) error {
			if id != "R" {
				return types.ErrSessionLocked
			}
			return nil
		}
		e.llm.route(sysRoot, callsStep("", agentCall("researcher", "d", "P")), textStep("root done"))
		e.llm.route(sysW, textStep("never"))
		if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
			t.Fatal(err)
		}
		if len(e.childCalls()) != 1 {
			t.Fatal("child row not created")
		}
		rs := resultsOf(e.toolMsgs("R")[0])
		if len(rs) != 1 || !rs[0].IsError || !strings.HasPrefix(resultString(t, rs[0]), "session_locked: ") {
			t.Fatalf("result = %+v", rs)
		}
	})
}

// 11. Cancellation.
func TestMultiAgent_Cancellation(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	e := newMAEnv(t)
	e.standardRoot()
	e.llm.route(sysRoot, callsStep("", agentCall("researcher", "d", "P1"), agentCall("researcher", "d", "P2")), textStep("root done"))
	e.llm.route(sysW, textStep("late")).delay = 10 * time.Second

	started := make(chan struct{}, 2)
	e.llm.onCall = func(system string) {
		if system == sysW {
			started <- struct{}{}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := e.turn(ctx, &syncRecordingSink{})
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		cancel()
		t.Fatalf("Turn returned before any child started: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("no child turn started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Turn did not return after cancellation")
	}
	e.spy.mu.Lock()
	defer e.spy.mu.Unlock()
	if len(e.spy.errs) == 0 {
		t.Fatal("runner never returned")
	}
	for _, err := range e.spy.errs {
		var ae *agentToolError
		if !errors.As(err, &ae) || ae.Code != "cancelled" {
			t.Fatalf("runner err = %v, want cancelled", err)
		}
	}
}

// 12. Tool handler close.
func TestMultiAgent_ToolHandlerClose(t *testing.T) {
	t.Run("nested success", func(t *testing.T) {
		e := nestedEnv(t)
		if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
			t.Fatal(err)
		}
		hs := e.builder.allHandlers()
		if len(hs) != 3 {
			t.Fatalf("handlers built = %d, want 3", len(hs))
		}
		for _, h := range hs {
			if h.closes != 1 {
				t.Fatalf("handler %s closed %d times", h.versionID, h.closes)
			}
		}
	})
	t.Run("child fails", func(t *testing.T) {
		e := newMAEnv(t)
		e.standardRoot()
		e.llm.route(sysRoot, callsStep("", agentCall("researcher", "d", "P")), textStep("root done"))
		e.llm.route(sysW, textStep("x")).failAt = map[int]error{0: errors.New("boom")}
		if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
			t.Fatal(err)
		}
		hs := e.builder.allHandlers()
		if len(hs) != 2 {
			t.Fatalf("handlers built = %d, want 2", len(hs))
		}
		for _, h := range hs {
			if h.closes != 1 {
				t.Fatalf("handler %s closed %d times", h.versionID, h.closes)
			}
		}
	})
}

// aguiRun runs a root turn through a real AG-UI sink and returns the
// decoded SSE events.
func aguiRun(t *testing.T, e *maEnv) []map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	sink, err := agui.NewFactory().NewSink(rec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.turn(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	_ = sink.Close()
	var out []map[string]any
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("bad SSE data %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

func evType(ev map[string]any) string { s, _ := ev["type"].(string); return s }

func rawField(ev map[string]any, k string) any {
	raw, _ := ev["rawEvent"].(map[string]any)
	return raw[k]
}

// 13. Event order at depth 1 (AG-UI).
func TestMultiAgent_EventOrderDepth1(t *testing.T) {
	e := newMAEnv(t)
	e.standardRoot()
	e.llm.route(sysRoot, callsStep("", agentCall("researcher", "look up", "P")), textStep("root done"))
	e.llm.route(sysW, callsStep("", toolCallSpec{Name: "search", Input: `{"q":1}`}), textStep("W done"))

	evs := aguiRun(t, e)
	var types_ []string
	for _, ev := range evs {
		types_ = append(types_, evType(ev))
	}
	want := []string{
		"RUN_STARTED",
		"TEXT_MESSAGE_START", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TEXT_MESSAGE_END",
		"STEP_STARTED",
		"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT",
		"STEP_FINISHED",
		"TOOL_CALL_RESULT",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END",
		"RUN_FINISHED",
	}
	if strings.Join(types_, ",") != strings.Join(want, ",") {
		t.Fatalf("event types:\n got %v\nwant %v", types_, want)
	}

	rootID := e.toolUses("R")[0].ID
	child := e.childCalls()[0].ChildID
	childUse := e.toolUses(child)[0].ID
	if evs[2]["toolCallId"] != rootID || evs[12]["toolCallId"] != rootID {
		t.Fatalf("root agent ids: %v / %v, want %s", evs[2]["toolCallId"], evs[12]["toolCallId"], rootID)
	}
	if evs[6]["stepName"] != "researcher:"+rootID || rawField(evs[6], "parentToolCallId") != rootID || rawField(evs[6], "childSessionId") != child || rawField(evs[6], "description") != "look up" {
		t.Fatalf("STEP_STARTED = %v", evs[6])
	}
	for _, i := range []int{7, 8, 9, 10} {
		if evs[i]["toolCallId"] != child+":"+childUse || rawField(evs[i], "childSessionId") != child {
			t.Fatalf("child event %d = %v", i, evs[i])
		}
	}
	if evs[11]["stepName"] != "researcher:"+rootID || rawField(evs[11], "isError") != false {
		t.Fatalf("STEP_FINISHED = %v", evs[11])
	}
}

// 14. Event order at depth 2 (AG-UI).
func TestMultiAgent_EventOrderDepth2(t *testing.T) {
	e := nestedEnv(t)
	e.llm.route(sysX, callsStep("", toolCallSpec{Name: "search", Input: `{}`}), textStep("X done"))
	evs := aguiRun(t, e)

	calls := e.childCalls()
	w, x := calls[0], calls[1]
	rootAgentID := e.toolUses("R")[0].ID
	wAgentID := e.toolUses(w.ChildID)[0].ID
	xUse := e.toolUses(x.ChildID)[0].ID

	var wStart, xStart, wFin, xFin = -1, -1, -1, -1
	runStarted, runFinished := 0, 0
	for i, ev := range evs {
		switch evType(ev) {
		case "RUN_STARTED":
			runStarted++
		case "RUN_FINISHED":
			runFinished++
		case "STEP_STARTED":
			switch rawField(ev, "childSessionId") {
			case w.ChildID:
				wStart = i
				if rawField(ev, "parentToolCallId") != rootAgentID {
					t.Fatalf("W parentToolCallId = %v", rawField(ev, "parentToolCallId"))
				}
			case x.ChildID:
				xStart = i
				if rawField(ev, "parentToolCallId") != w.ChildID+":"+wAgentID {
					t.Fatalf("X parentToolCallId = %v", rawField(ev, "parentToolCallId"))
				}
			}
		case "STEP_FINISHED":
			switch rawField(ev, "childSessionId") {
			case w.ChildID:
				wFin = i
			case x.ChildID:
				xFin = i
			}
		case "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT":
			id, _ := ev["toolCallId"].(string)
			if strings.Count(id, ":") > 1 {
				t.Fatalf("toolCallId prefixed twice: %s", id)
			}
			if rawField(ev, "childSessionId") == x.ChildID && id != x.ChildID+":"+xUse {
				t.Fatalf("X tool event id = %s", id)
			}
		}
	}
	if runStarted != 1 || runFinished != 1 {
		t.Fatalf("RUN_STARTED=%d RUN_FINISHED=%d", runStarted, runFinished)
	}
	if !(wStart >= 0 && wStart < xStart && xStart < xFin && xFin < wFin) {
		t.Fatalf("step order W start %d, X start %d, X finish %d, W finish %d", wStart, xStart, xFin, wFin)
	}
}

// 15. Two parallel calls to the same binding.
func TestMultiAgent_DistinctStepNames(t *testing.T) {
	e := newMAEnv(t)
	e.standardRoot()
	e.llm.route(sysRoot, callsStep("", agentCall("researcher", "a", "P1"), agentCall("researcher", "b", "P2")), textStep("root done"))
	e.llm.route(sysW, echoStep(""))
	evs := aguiRun(t, e)
	names := map[string]bool{}
	for _, ev := range evs {
		if evType(ev) == "STEP_STARTED" {
			names[ev["stepName"].(string)] = true
		}
	}
	if len(names) != 2 {
		t.Fatalf("distinct step names = %v", names)
	}
}

type markerKey struct{}

// 16. Header inheritance.
func TestMultiAgent_HeaderInheritance(t *testing.T) {
	var mu sync.Mutex
	var gotUser, gotOverride string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotUser, gotOverride = r.Header.Get("X-User"), r.Header.Get("X-Test")
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	e := newMAEnv(t)
	e.standardRoot()
	e.repo.setArchitecture("vW", `{"tools":[{"id":"orders.create","name":"orders_create","method":"POST","path":"/api/orders"}]}`)
	backend := tools.NewHTTPEndpointBackend("api", "backend-1", tools.HTTPBackendCfg{BaseURL: srv.URL},
		[]tools.HTTPEndpointDef{{ID: "orders.create", Name: "orders_create", Method: "POST", Path: "/api/orders"}},
		map[string]string{"orders.create": "backend-1"})
	e.builder.override = map[string]tools.Handler{"vW": tools.NewComposite([]tools.Backend{backend})}
	e.llm.route(sysRoot, callsStep("", agentCall("researcher", "d", "P")), textStep("root done"))
	e.llm.route(sysW, callsStep("", toolCallSpec{Name: "orders_create", Input: `{}`}), textStep("W done"))

	ctx := tools.WithForwardedHeaders(context.Background(), http.Header{"X-User": {"u1"}})
	ctx = tools.WithBackendHeaderRouting(ctx, tools.BackendHeaderRouting{ByName: map[string]tools.BackendRoutingBlock{"api": {All: true}}})
	ctx = tools.WithSessionHeaderOverrides(ctx, tools.SessionHeaderOverrides{"backend-1": {"X-Test": "t1"}})
	ctx = context.WithValue(ctx, markerKey{}, "root")

	if _, err := e.turn(ctx, &syncRecordingSink{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotUser != "u1" || gotOverride != "t1" {
		t.Fatalf("child MCP call headers: X-User=%q X-Test=%q", gotUser, gotOverride)
	}
	e.builder.mu.Lock()
	defer e.builder.mu.Unlock()
	sawChild := false
	for _, b := range e.builder.builds {
		if b.ctx.Value(markerKey{}) != "root" {
			t.Fatalf("Build ctx for %s lost the request context", b.versionID)
		}
		sawChild = sawChild || b.versionID == "vW"
	}
	if !sawChild {
		t.Fatal("child handler never built")
	}
}

// 17. Artifacts: child tool-result artifacts stay on the child; echoed refs
// are opaque.
func TestMultiAgent_Artifacts(t *testing.T) {
	t.Run("child tool result artifact", func(t *testing.T) {
		e := newMAEnv(t)
		e.o.WithArtifactUploader(&stubUploader{})
		e.standardRoot()
		e.builder.result = func(v string, c tools.Call) tools.Result {
			if v == "vW" && c.Name == "search" {
				return tools.Result{ToolUseID: c.ID, Output: imageToolOutput()}
			}
			return tools.Result{ToolUseID: c.ID, Output: []byte(`{}`)}
		}
		e.llm.route(sysRoot, callsStep("", agentCall("researcher", "d", "P")), textStep("root done"))
		e.llm.route(sysW, callsStep("", toolCallSpec{Name: "search", Input: `{}`}), textStep("seen"))
		rec := &syncRecordingSink{}
		if _, err := e.turn(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
		child := e.childCalls()[0].ChildID
		if len(e.chats.toolRows) != 1 {
			t.Fatalf("tool rows = %d", len(e.chats.toolRows))
		}
		childTool := e.toolMsgs(child)
		if len(childTool) != 1 || e.chats.toolRows[0].MessageID != childTool[0].ID {
			t.Fatal("tool_result row not recorded on the child's tool message")
		}
		sawChildResult := false
		for _, ev := range rec.snapshot() {
			switch x := ev.(type) {
			case transport.ArtifactRefEvent:
				t.Fatal("ARTIFACT_REF forwarded on the root stream")
			case transport.ToolResultEvent:
				sawChildResult = sawChildResult || x.ChildSessionID == child
			}
		}
		if !sawChildResult {
			t.Fatal("child's tagged tool result missing from the root stream")
		}
		if hasArtifactRef(e.toolMsgs("R")[0]) {
			t.Fatal("root agent tool message carries an artifact_ref")
		}
	})
	t.Run("echoed ref is opaque", func(t *testing.T) {
		e := newMAEnv(t)
		e.o.WithArtifactUploader(&stubUploader{})
		e.standardRoot()
		ref := `{"store_id":"S","uri":"u"}`
		e.llm.route(sysRoot, callsStep("", agentCall("researcher", "d", "use "+ref)), textStep("root done"))
		e.llm.route(sysW, callsStep("", toolCallSpec{Name: "search", Input: `{"ref":` + ref + `}`}), textStep("ok"))
		if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
			t.Fatal(err)
		}
		child := e.childCalls()[0].ChildID
		var got []tools.Call
		for _, h := range e.builder.allHandlers() {
			if h.versionID == "vW" {
				got = append(got, h.calls...)
			}
		}
		if len(got) != 1 || string(got[0].Input) != `{"ref":`+ref+`}` {
			t.Fatalf("tool input = %+v", got)
		}
		if len(e.chats.toolRows) != 0 {
			t.Fatal("echoed ref created a tool-result row")
		}
		for _, m := range e.msgs(child) {
			if hasArtifactRef(m) {
				t.Fatal("child messages carry an artifact_ref")
			}
		}
	})
}

// panicRunner panics for one prompt and delegates every other call.
type panicRunner struct {
	inner   subAgentRunner
	panicOn string
}

func (p *panicRunner) Run(ctx context.Context, req subAgentRequest) (subAgentResult, error) {
	if req.Prompt == p.panicOn {
		panic("kaboom")
	}
	return p.inner.Run(ctx, req)
}

// 15. A panicking child is contained to its own agent call.
func TestMultiAgent_ChildPanicRecovered(t *testing.T) {
	logs := &syncBuffer{}
	e := newMAEnvWithLogger(t, slog.New(slog.NewTextHandler(logs, nil)))
	e.o.MaxParallel = 1 // a leaked semaphore slot would deadlock the sibling
	e.standardRoot()
	e.o.runner = &panicRunner{inner: e.spy, panicOn: "BOOM"}
	e.llm.route(sysRoot, callsStep("", agentCall("researcher", "d", "BOOM"), agentCall("researcher", "d", "P2")), textStep("root done"))
	e.llm.route(sysW, echoStep("echo:"))

	sink := &syncRecordingSink{}
	done := make(chan error, 1)
	go func() {
		_, err := e.turn(context.Background(), sink)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not finish")
	}

	uses := e.toolUses("R")
	rs := resultsOf(e.toolMsgs("R")[0])
	if len(rs) != 2 {
		t.Fatalf("results = %d", len(rs))
	}
	if !rs[0].IsError || resultString(t, rs[0]) != "subagent_failed: sub-agent panicked" {
		t.Fatalf("panicked result = %+v (%s)", rs[0], rs[0].Result)
	}
	if rs[1].IsError || resultString(t, rs[1]) != "echo:P2" {
		t.Fatalf("sibling result = %+v (%s)", rs[1], rs[1].Result)
	}
	var sawPanicResult bool
	for _, ev := range sink.snapshot() {
		if r, ok := ev.(transport.ToolResultEvent); ok && r.ToolCallID == uses[0].ID && r.IsError {
			sawPanicResult = true
		}
	}
	if !sawPanicResult {
		t.Fatal("no TOOL_CALL_RESULT for the panicked call")
	}
	out := logs.String()
	for _, want := range []string{"sub-agent panicked", "parent_session_id=R", "tool_use_id=" + uses[0].ID, "stack="} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q:\n%s", want, out)
		}
	}
	if e.lastText("R") != "root done" {
		t.Fatal("root did not continue")
	}
}

// A panic inside the child turn (after STEP_STARTED) still closes the step.
func TestMultiAgent_ChildTurnPanicClosesStep(t *testing.T) {
	e := newMAEnv(t)
	e.standardRoot()
	e.llm.route(sysRoot, callsStep("", agentCall("researcher", "d", "P")), textStep("root done"))
	e.llm.route(sysW, func(llm.Request) []llm.Event { panic("llm client bug") })

	sink := &syncRecordingSink{}
	if _, err := e.turn(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	rs := resultsOf(e.toolMsgs("R")[0])
	if len(rs) != 1 || resultString(t, rs[0]) != "subagent_failed: sub-agent panicked" {
		t.Fatalf("result = %+v", rs)
	}
	var started, finished int
	for _, ev := range sink.snapshot() {
		switch f := ev.(type) {
		case transport.StepStartedEvent:
			started++
		case transport.StepFinishedEvent:
			finished++
			if !f.IsError {
				t.Fatal("STEP_FINISHED not flagged as error")
			}
		}
	}
	if started != 1 || finished != 1 {
		t.Fatalf("STEP_STARTED=%d STEP_FINISHED=%d", started, finished)
	}
}

// Unparsable input on a non-agent tool is an error result, not a failed turn.
func TestMultiAgent_UnparsableToolInput(t *testing.T) {
	e := newMAEnv(t)
	e.standardRoot()
	e.llm.route(sysRoot, callsStep("", toolCallSpec{Name: "search", Input: `{bad`}, toolCallSpec{Name: "search", Input: `{"q":1}`}), textStep("root done"))

	stop, err := e.turn(context.Background(), &syncRecordingSink{})
	if err != nil || stop != "end_turn" {
		t.Fatalf("Turn = %q, %v", stop, err)
	}
	if n := len(e.childCalls()); n != 0 {
		t.Fatalf("children = %d", n)
	}
	var calls []tools.Call
	for _, h := range e.builder.allHandlers() {
		calls = append(calls, h.calls...)
	}
	if len(calls) != 1 || string(calls[0].Input) != `{"q":1}` {
		t.Fatalf("handler calls = %+v, want only the valid one", calls)
	}
	uses := e.toolUses("R")
	if len(uses) != 2 || string(uses[0].Input) != `{}` {
		t.Fatalf("persisted tool_use = %+v, want input {}", uses)
	}
	reqs := e.llm.reqs(sysRoot)
	if len(reqs) != 2 {
		t.Fatalf("root LLM calls = %d", len(reqs))
	}
	for _, m := range reqs[1].Messages {
		for _, b := range m.Content {
			if tu, ok := b.(llm.ToolUseBlock); ok && !json.Valid(tu.Input) {
				t.Fatalf("next request carries invalid tool_use input %q", tu.Input)
			}
		}
	}
	rs := requestToolResults(reqs[1])
	if len(rs) != 2 || !rs[0].IsError || string(rs[0].Result) != `{"error":"tool input is not valid JSON"}` || rs[1].IsError {
		t.Fatalf("results = %+v", rs)
	}
	if e.lastText("R") != "root done" {
		t.Fatal("root did not continue")
	}
}

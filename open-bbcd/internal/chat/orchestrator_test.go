package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport/jsonl"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestOrchestrator_TurnTextOnly(t *testing.T) {
	version := &types.AgentVersion{
		ID:     "agent-1",
		Bundle: []byte(`{"main_prompt":"sys","skills":[],"capabilities":[]}`),
	}
	fakeAgent := &fakeAgentRepo{version: version}
	fakeChat := &fakeChatRepo{}
	flm := &fakeLLM{
		name: "fake",
		script: [][]llm.Event{
			{
				llm.TextDeltaEvent{Delta: "hello "},
				llm.TextDeltaEvent{Delta: "world"},
				llm.MessageStopEvent{StopReason: "end_turn"},
			},
		},
	}
	ftools := &fakeTools{}

	o := NewOrchestrator(fakeAgent, fakeChat, flm, ftools, slog.Default())

	var buf bytes.Buffer
	sink, _ := jsonl.NewFactory().NewWriterSink(&buf)

	err := o.Turn(context.Background(), "agent-1", "session-1",
		[]llm.Block{llm.TextBlock{Text: "hi"}}, sink)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}

	// 2 messages persisted: user then assistant.
	if len(fakeChat.messages) != 2 {
		t.Fatalf("expected 2 persisted messages, got %d", len(fakeChat.messages))
	}
	if fakeChat.messages[0].Role != types.ChatRoleUser {
		t.Fatalf("first message role: got %v", fakeChat.messages[0].Role)
	}
	if fakeChat.messages[1].Role != types.ChatRoleAssistant {
		t.Fatalf("second message role: got %v", fakeChat.messages[1].Role)
	}

	// Stream contains text_delta + run_finished.
	out := buf.String()
	if !strings.Contains(out, "text_delta") {
		t.Fatalf("expected text_delta in output: %s", out)
	}
	if !strings.Contains(out, "run_finished") {
		t.Fatalf("expected run_finished in output: %s", out)
	}
}

func TestOrchestrator_NoBundle_ReturnsErrAgentNotRunnable(t *testing.T) {
	version := &types.AgentVersion{ID: "agent-1", Bundle: nil}
	fakeAgent := &fakeAgentRepo{version: version}
	o := NewOrchestrator(fakeAgent, &fakeChatRepo{}, &fakeLLM{}, &fakeTools{}, slog.Default())

	var buf bytes.Buffer
	sink, _ := jsonl.NewFactory().NewWriterSink(&buf)

	err := o.Turn(context.Background(), "agent-1", "s1",
		[]llm.Block{llm.TextBlock{Text: "hi"}}, sink)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if err != types.ErrAgentNotRunnable {
		t.Fatalf("expected ErrAgentNotRunnable, got %v", err)
	}
}

func TestOrchestrator_AssistantTextPersistsConcatenated(t *testing.T) {
	// Verifies that streaming deltas accumulate into a single TextBlock
	// before persistence (we shouldn't end up with one-block-per-delta).
	version := &types.AgentVersion{
		ID:     "a",
		Bundle: []byte(`{"main_prompt":"sys","skills":[],"capabilities":[]}`),
	}
	fakeChat := &fakeChatRepo{}
	flm := &fakeLLM{
		name: "fake",
		script: [][]llm.Event{
			{
				llm.TextDeltaEvent{Delta: "one "},
				llm.TextDeltaEvent{Delta: "two "},
				llm.TextDeltaEvent{Delta: "three"},
				llm.MessageStopEvent{StopReason: "end_turn"},
			},
		},
	}
	o := NewOrchestrator(&fakeAgentRepo{version: version}, fakeChat, flm, &fakeTools{}, slog.Default())

	var buf bytes.Buffer
	sink, _ := jsonl.NewFactory().NewWriterSink(&buf)
	if err := o.Turn(context.Background(), "a", "s",
		[]llm.Block{llm.TextBlock{Text: "hi"}}, sink); err != nil {
		t.Fatalf("Turn: %v", err)
	}

	// The assistant message's persisted content should be a single text
	// block reading "one two three", not three separate blocks.
	var assistantBlocks []map[string]any
	_ = json_unmarshal_test_helper(t, fakeChat.messages[1].Content, &assistantBlocks)
	if len(assistantBlocks) != 1 {
		t.Fatalf("expected 1 assistant block, got %d", len(assistantBlocks))
	}
	if got := assistantBlocks[0]["text"]; got != "one two three" {
		t.Fatalf("text concat: got %v", got)
	}
}

// Small helper to keep the test bodies readable.
func json_unmarshal_test_helper(t *testing.T, raw []byte, dst any) any {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return dst
}

func TestOrchestrator_OneToolRound(t *testing.T) {
	version := &types.AgentVersion{
		ID:     "agent-1",
		Bundle: []byte(`{"main_prompt":"sys","skills":[],"capabilities":[]}`),
	}
	fakeChat := &fakeChatRepo{}
	flm := &fakeLLM{
		name: "fake",
		script: [][]llm.Event{
			// Round 1: tool_use stop.
			{
				llm.ToolUseStartEvent{ID: "tu_1", Name: "Skill"},
				llm.ToolUseInputEvent{ID: "tu_1", JSONFragment: `{"name":"x"}`},
				llm.ToolUseEndEvent{ID: "tu_1"},
				llm.MessageStopEvent{StopReason: "tool_use"},
			},
			// Round 2: text + end_turn.
			{
				llm.TextDeltaEvent{Delta: "done"},
				llm.MessageStopEvent{StopReason: "end_turn"},
			},
		},
	}
	ft := &fakeTools{
		results: []tools.Result{{ToolUseID: "tu_1", Output: []byte(`{"prompt":"X"}`)}},
	}
	o := NewOrchestrator(&fakeAgentRepo{version: version}, fakeChat, flm, ft, slog.Default())

	var buf bytes.Buffer
	sink, _ := jsonl.NewFactory().NewWriterSink(&buf)
	if err := o.Turn(context.Background(), "agent-1", "s1",
		[]llm.Block{llm.TextBlock{Text: "hi"}}, sink); err != nil {
		t.Fatalf("Turn: %v", err)
	}

	// Expect 4 persisted messages: user, assistant (tool_use), tool (tool_result), assistant (text)
	if len(fakeChat.messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(fakeChat.messages))
	}
	if fakeChat.messages[0].Role != types.ChatRoleUser {
		t.Fatalf("msg[0] role: got %v", fakeChat.messages[0].Role)
	}
	if fakeChat.messages[1].Role != types.ChatRoleAssistant {
		t.Fatalf("msg[1] role: got %v", fakeChat.messages[1].Role)
	}
	if fakeChat.messages[2].Role != types.ChatRoleTool {
		t.Fatalf("msg[2] role: got %v", fakeChat.messages[2].Role)
	}
	if fakeChat.messages[3].Role != types.ChatRoleAssistant {
		t.Fatalf("msg[3] role: got %v", fakeChat.messages[3].Role)
	}

	// Tool was called once with the right name.
	if len(ft.callLog) != 1 || ft.callLog[0].Name != "Skill" {
		t.Fatalf("expected 1 Skill call, got %+v", ft.callLog)
	}
}

func TestOrchestrator_BoundedToolLoop(t *testing.T) {
	version := &types.AgentVersion{
		ID:     "a",
		Bundle: []byte(`{"main_prompt":"x","skills":[],"capabilities":[]}`),
	}
	fakeChat := &fakeChatRepo{}
	toolUseRound := []llm.Event{
		llm.ToolUseStartEvent{ID: "t", Name: "Skill"},
		llm.ToolUseInputEvent{ID: "t", JSONFragment: `{"name":"x"}`},
		llm.ToolUseEndEvent{ID: "t"},
		llm.MessageStopEvent{StopReason: "tool_use"},
	}
	// LLM always emits tool_use — never stops naturally.
	script := make([][]llm.Event, 20)
	for i := range script {
		script[i] = toolUseRound
	}
	flm := &fakeLLM{name: "fake", script: script}
	ft := &fakeTools{}
	o := NewOrchestrator(&fakeAgentRepo{version: version}, fakeChat, flm, ft, slog.Default())
	o.MaxToolRounds = 3

	var buf bytes.Buffer
	sink, _ := jsonl.NewFactory().NewWriterSink(&buf)
	if err := o.Turn(context.Background(), "a", "s",
		[]llm.Block{llm.TextBlock{Text: "x"}}, sink); err != nil {
		t.Fatalf("Turn: %v", err)
	}

	// After 3 tool rounds the loop exits with stop_reason = max_tool_rounds.
	// Tool was called 3 times.
	if len(ft.callLog) != 3 {
		t.Fatalf("expected 3 tool calls (max bound), got %d", len(ft.callLog))
	}
}

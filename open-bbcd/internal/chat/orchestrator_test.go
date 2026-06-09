package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport/jsonl"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestOrchestrator_TurnTextOnly(t *testing.T) {
	agent := &types.Agent{
		ID:     "agent-1",
		Bundle: []byte(`{"main_prompt":"sys","skills":[],"capabilities":[]}`),
	}
	fakeAgent := &fakeAgentRepo{agent: agent}
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
	agent := &types.Agent{ID: "agent-1", Bundle: nil}
	fakeAgent := &fakeAgentRepo{agent: agent}
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
	agent := &types.Agent{
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
	o := NewOrchestrator(&fakeAgentRepo{agent: agent}, fakeChat, flm, &fakeTools{}, slog.Default())

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

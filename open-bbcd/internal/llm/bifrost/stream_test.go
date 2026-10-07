package bifrost

import (
	"reflect"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

func strp(s string) *string { return &s }

func choiceChunk(delta *schemas.ChatStreamResponseChoiceDelta, finish *string) *schemas.BifrostStreamChunk {
	return &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{{
			Index:                    0,
			FinishReason:             finish,
			ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: delta},
		}},
	}}
}

func textChunk(s string) *schemas.BifrostStreamChunk {
	return choiceChunk(&schemas.ChatStreamResponseChoiceDelta{Content: strp(s)}, nil)
}

// toolChunk: id/name are omitted when empty, like continuation deltas.
func toolChunk(index uint16, id, name, args string) *schemas.BifrostStreamChunk {
	tc := schemas.ChatAssistantMessageToolCall{Index: index, Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: args}}
	if id != "" {
		tc.ID = strp(id)
	}
	if name != "" {
		tc.Function.Name = strp(name)
	}
	return choiceChunk(&schemas.ChatStreamResponseChoiceDelta{ToolCalls: []schemas.ChatAssistantMessageToolCall{tc}}, nil)
}

func finishChunk(reason string) *schemas.BifrostStreamChunk {
	return choiceChunk(&schemas.ChatStreamResponseChoiceDelta{}, strp(reason))
}

func usageChunk(p, c int) *schemas.BifrostStreamChunk {
	return &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{},
		Usage:   &schemas.BifrostLLMUsage{PromptTokens: p, CompletionTokens: c},
	}}
}

// run feeds chunks then the channel close and returns every event and the
// first error.
func run(t *testing.T, chunks ...*schemas.BifrostStreamChunk) ([]llm.Event, error) {
	t.Helper()
	tr := newStreamTranslator("openai")
	var evs []llm.Event
	for _, c := range chunks {
		got, err := tr.translate(c)
		evs = append(evs, got...)
		if err != nil {
			return evs, err
		}
	}
	return evs, tr.done()
}

func TestStream_TextOnly(t *testing.T) {
	evs, err := run(t, textChunk("Hel"), textChunk("lo"), finishChunk("stop"), usageChunk(11, 7))
	if err != nil {
		t.Fatal(err)
	}
	want := []llm.Event{
		llm.TextDeltaEvent{Delta: "Hel"}, llm.TextDeltaEvent{Delta: "lo"},
		llm.MessageStopEvent{StopReason: "end_turn"},
		llm.UsageEvent{InputTokens: 11, OutputTokens: 7},
	}
	if !reflect.DeepEqual(evs, want) {
		t.Fatalf("events = %#v", evs)
	}
}

func TestStream_SingleToolCall(t *testing.T) {
	evs, err := run(t,
		toolChunk(0, "call_1", "lookup", ""),
		toolChunk(0, "", "", `{"q":`),
		toolChunk(0, "", "", `"x"}`),
		finishChunk("tool_calls"),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []llm.Event{
		llm.ToolUseStartEvent{ID: "call_1", Name: "lookup"},
		llm.ToolUseInputEvent{ID: "call_1", JSONFragment: `{"q":`},
		llm.ToolUseInputEvent{ID: "call_1", JSONFragment: `"x"}`},
		llm.ToolUseEndEvent{ID: "call_1"},
		llm.MessageStopEvent{StopReason: "tool_use"},
	}
	if !reflect.DeepEqual(evs, want) {
		t.Fatalf("events = %#v", evs)
	}
}

func TestStream_ParallelToolCallsInterleaved(t *testing.T) {
	evs, err := run(t,
		toolChunk(0, "a", "one", ""),
		toolChunk(1, "b", "two", `{"y"`),
		toolChunk(0, "", "", `{"x":1}`),
		toolChunk(1, "", "", `:2}`),
		finishChunk("tool_calls"),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []llm.Event{
		llm.ToolUseStartEvent{ID: "a", Name: "one"},
		llm.ToolUseStartEvent{ID: "b", Name: "two"},
		llm.ToolUseInputEvent{ID: "b", JSONFragment: `{"y"`},
		llm.ToolUseInputEvent{ID: "a", JSONFragment: `{"x":1}`},
		llm.ToolUseInputEvent{ID: "b", JSONFragment: `:2}`},
		llm.ToolUseEndEvent{ID: "a"},
		llm.ToolUseEndEvent{ID: "b"},
		llm.MessageStopEvent{StopReason: "tool_use"},
	}
	if !reflect.DeepEqual(evs, want) {
		t.Fatalf("events = %#v", evs)
	}
}

func TestStream_ArgsBeforeNameAreBuffered(t *testing.T) {
	evs, err := run(t, toolChunk(0, "a", "", `{"x"`), toolChunk(0, "", "one", `:1}`), finishChunk("tool_calls"))
	if err != nil {
		t.Fatal(err)
	}
	want := []llm.Event{
		llm.ToolUseStartEvent{ID: "a", Name: "one"},
		llm.ToolUseInputEvent{ID: "a", JSONFragment: `{"x"`},
		llm.ToolUseInputEvent{ID: "a", JSONFragment: `:1}`},
		llm.ToolUseEndEvent{ID: "a"},
		llm.MessageStopEvent{StopReason: "tool_use"},
	}
	if !reflect.DeepEqual(evs, want) {
		t.Fatalf("events = %#v", evs)
	}
}

func TestStream_StopWithOpenToolCallIsToolUse(t *testing.T) {
	evs, _ := run(t, toolChunk(0, "a", "one", "{}"), finishChunk("stop"))
	if got := evs[len(evs)-1]; got != (llm.MessageStopEvent{StopReason: "tool_use"}) {
		t.Fatalf("last = %#v", got)
	}
}

func TestStream_LengthWithoutToolCallIsMaxTokens(t *testing.T) {
	evs, _ := run(t, textChunk("par"), finishChunk("length"))
	if got := evs[len(evs)-1]; got != (llm.MessageStopEvent{StopReason: "max_tokens"}) {
		t.Fatalf("last = %#v", got)
	}
}

func TestStream_LengthWithOpenToolCallIsMaxTokens(t *testing.T) {
	evs, err := run(t, toolChunk(0, "a", "one", `{"q":"ab`), finishChunk("length"))
	if err != nil {
		t.Fatal(err)
	}
	want := []llm.Event{
		llm.ToolUseStartEvent{ID: "a", Name: "one"},
		llm.ToolUseInputEvent{ID: "a", JSONFragment: `{"q":"ab`},
		llm.ToolUseEndEvent{ID: "a"},
		llm.MessageStopEvent{StopReason: "max_tokens"},
	}
	if !reflect.DeepEqual(evs, want) {
		t.Fatalf("events = %#v", evs)
	}
}

func TestStream_UnknownReasonPassesThrough(t *testing.T) {
	evs, _ := run(t, textChunk("x"), finishChunk("content_filter"))
	if got := evs[len(evs)-1]; got != (llm.MessageStopEvent{StopReason: "content_filter"}) {
		t.Fatalf("last = %#v", got)
	}
}

func TestStream_NoFinishReasonIsError(t *testing.T) {
	evs, err := run(t, textChunk("par"), toolChunk(0, "a", "one", "{}"))
	if err == nil || err.Error() != "bifrost: openai: stream ended without finish_reason" {
		t.Fatalf("err = %v", err)
	}
	for _, e := range evs {
		switch e.(type) {
		case llm.ToolUseEndEvent, llm.MessageStopEvent:
			t.Fatalf("unexpected %#v", e)
		}
	}
}

func TestStream_ToolCallWithoutIDOrNameIsError(t *testing.T) {
	_, err := run(t, toolChunk(0, "", "", `{"x":1}`), finishChunk("tool_calls"))
	if err == nil || !strings.Contains(err.Error(), "tool call at index 0 finished without an id and name") {
		t.Fatalf("err = %v", err)
	}
}

func TestStream_ErrorChunk(t *testing.T) {
	status := 529
	errChunk := &schemas.BifrostStreamChunk{BifrostError: &schemas.BifrostError{StatusCode: &status, Error: &schemas.ErrorField{Message: "overloaded"}}}
	evs, err := run(t, textChunk("a"), errChunk, textChunk("never"))
	if err == nil || err.Error() != "bifrost: openai: overloaded (status 529)" {
		t.Fatalf("err = %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("events after error: %#v", evs)
	}
}

func TestProviderError_NoStatus(t *testing.T) {
	err := providerError("groq", &schemas.BifrostError{Error: &schemas.ErrorField{Message: "boom"}})
	if err.Error() != "bifrost: groq: boom" {
		t.Fatalf("err = %v", err)
	}
}

func TestStream_DuplicateIDsAreMadeUnique(t *testing.T) {
	evs, err := run(t,
		toolChunk(0, "lookup", "lookup", `{"a":1}`),
		toolChunk(1, "lookup", "lookup", `{"b":2}`),
		finishChunk("tool_calls"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 7 {
		t.Fatalf("events = %#v", evs)
	}
	s0, s1 := evs[0].(llm.ToolUseStartEvent), evs[2].(llm.ToolUseStartEvent)
	if s0.ID == s1.ID || !strings.HasPrefix(s0.ID, "lookup_") || !strings.HasPrefix(s1.ID, "lookup_") {
		t.Fatalf("ids = %q %q", s0.ID, s1.ID)
	}
	if in := evs[1].(llm.ToolUseInputEvent); in.ID != s0.ID || in.JSONFragment != `{"a":1}` {
		t.Fatalf("input0 = %#v", in)
	}
	if in := evs[3].(llm.ToolUseInputEvent); in.ID != s1.ID || in.JSONFragment != `{"b":2}` {
		t.Fatalf("input1 = %#v", in)
	}
	if e0, e1 := evs[4].(llm.ToolUseEndEvent), evs[5].(llm.ToolUseEndEvent); e0.ID != s0.ID || e1.ID != s1.ID {
		t.Fatalf("ends = %#v %#v", e0, e1)
	}
}

func TestStream_IDEqualToNameIsRewritten(t *testing.T) {
	evs, err := run(t, toolChunk(0, "get_x", "get_x", "{}"), finishChunk("tool_calls"))
	if err != nil {
		t.Fatal(err)
	}
	if s := evs[0].(llm.ToolUseStartEvent); !strings.HasPrefix(s.ID, "get_x_") || s.ID == "get_x_" {
		t.Fatalf("id = %q", s.ID)
	}
}

func TestStream_SignatureAndProviderIDsUnchanged(t *testing.T) {
	evs, err := run(t,
		toolChunk(0, "lookup_ts_abc", "lookup", "{}"),
		toolChunk(1, "call_1", "lookup", "{}"),
		finishChunk("tool_calls"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if s := evs[0].(llm.ToolUseStartEvent); s.ID != "lookup_ts_abc" {
		t.Fatalf("id0 = %q", s.ID)
	}
	if s := evs[2].(llm.ToolUseStartEvent); s.ID != "call_1" {
		t.Fatalf("id1 = %q", s.ID)
	}
}

package bifrost

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// streamTranslator turns Bifrost chat-completion stream chunks into the
// internal llm.Event taxonomy. Tool-call deltas are tracked per index: start
// is emitted once both id and name are known (argument fragments seen before
// that are buffered); every open call is ended, in index order, when the
// finish_reason arrives.
type streamTranslator struct {
	provider string
	calls    map[uint16]*pendingCall
	order    []uint16
	opened   bool // at least one ToolUseStartEvent emitted
	finished bool // a finish_reason was seen
}

type pendingCall struct {
	id, name string
	started  bool
	buffered []string
}

func newStreamTranslator(provider string) *streamTranslator {
	return &streamTranslator{provider: provider, calls: map[uint16]*pendingCall{}}
}

// translate returns the events for one chunk, or the error a chunk carries.
func (t *streamTranslator) translate(c *schemas.BifrostStreamChunk) ([]llm.Event, error) {
	if c == nil {
		return nil, nil
	}
	if c.BifrostError != nil {
		return nil, providerError(t.provider, c.BifrostError)
	}
	r := c.BifrostChatResponse
	if r == nil {
		return nil, nil
	}
	var evs []llm.Event
	for _, ch := range r.Choices {
		if ch.Index != 0 { // n=1: only the first choice exists
			continue
		}
		if s := ch.ChatStreamResponseChoice; s != nil && s.Delta != nil {
			if s.Delta.Content != nil && *s.Delta.Content != "" {
				evs = append(evs, llm.TextDeltaEvent{Delta: *s.Delta.Content})
			}
			for _, tc := range s.Delta.ToolCalls {
				evs = append(evs, t.toolDelta(tc)...)
			}
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			fin, err := t.finish(*ch.FinishReason)
			if err != nil {
				return nil, err
			}
			evs = append(evs, fin...)
		}
	}
	if u := r.Usage; u != nil && (u.PromptTokens > 0 || u.CompletionTokens > 0) {
		evs = append(evs, llm.UsageEvent{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens})
	}
	return evs, nil
}

func (t *streamTranslator) toolDelta(tc schemas.ChatAssistantMessageToolCall) []llm.Event {
	pc, ok := t.calls[tc.Index]
	if !ok {
		pc = &pendingCall{}
		t.calls[tc.Index] = pc
		t.order = append(t.order, tc.Index)
	}
	if pc.id == "" && tc.ID != nil {
		pc.id = *tc.ID
	}
	if pc.name == "" && tc.Function.Name != nil {
		pc.name = *tc.Function.Name
	}
	var evs []llm.Event
	if !pc.started && pc.id != "" && pc.name != "" {
		pc.started = true
		t.opened = true
		evs = append(evs, llm.ToolUseStartEvent{ID: pc.id, Name: pc.name})
		for _, f := range pc.buffered {
			evs = append(evs, llm.ToolUseInputEvent{ID: pc.id, JSONFragment: f})
		}
		pc.buffered = nil
	}
	if a := tc.Function.Arguments; a != "" {
		if pc.started {
			evs = append(evs, llm.ToolUseInputEvent{ID: pc.id, JSONFragment: a})
		} else {
			pc.buffered = append(pc.buffered, a)
		}
	}
	return evs
}

func (t *streamTranslator) finish(reason string) ([]llm.Event, error) {
	if t.finished {
		return nil, nil
	}
	t.finished = true
	var evs []llm.Event
	for _, idx := range t.order {
		pc := t.calls[idx]
		if !pc.started {
			return nil, fmt.Errorf("bifrost: %s: tool call at index %d finished without an id and name", t.provider, idx)
		}
		evs = append(evs, llm.ToolUseEndEvent{ID: pc.id})
	}
	return append(evs, llm.MessageStopEvent{StopReason: t.stopReason(reason)}), nil
}

// stopReason maps a chat-completions finish_reason onto the vocabulary the
// orchestrator branches on. First match wins: length beats the tool-call
// override so a call truncated by the token cap reaches the orchestrator as
// max_tokens and is dropped there (orchestrator.go, "round did not stop for
// tool_use"), never dispatched with invalid input.
func (t *streamTranslator) stopReason(reason string) string {
	switch {
	case reason == "length":
		return "max_tokens"
	case reason == "tool_calls", t.opened:
		return "tool_use"
	case reason == "stop":
		return "end_turn"
	}
	return reason
}

// done is called when the chunk channel closes. A stream that never carried a
// finish_reason was cut off: it fails the turn rather than passing for a clean
// finish or a complete tool call.
func (t *streamTranslator) done() error {
	if !t.finished {
		return fmt.Errorf("bifrost: %s: stream ended without finish_reason", t.provider)
	}
	return nil
}

// providerError renders a Bifrost error as "bifrost: <provider>: <message>
// (status <code>)", omitting the status when Bifrost has none.
func providerError(provider string, e *schemas.BifrostError) error {
	msg := "unknown error"
	if e.Error != nil {
		switch {
		case e.Error.Message != "":
			msg = e.Error.Message
		case e.Error.Error != nil:
			msg = e.Error.Error.Error()
		}
	}
	if e.StatusCode != nil {
		return fmt.Errorf("bifrost: %s: %s (status %d)", provider, msg, *e.StatusCode)
	}
	return fmt.Errorf("bifrost: %s: %s", provider, msg)
}

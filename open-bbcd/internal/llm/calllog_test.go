package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

type scriptedLLM struct {
	name   string
	events []Event
	err    error
	pulled int
}

func (s *scriptedLLM) Name() string { return s.name }

func (s *scriptedLLM) Generate(context.Context, Request) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		for _, ev := range s.events {
			s.pulled++
			if !yield(ev, nil) {
				return
			}
		}
		if s.err != nil {
			yield(nil, s.err)
		}
	}
}

type scriptedRenderer struct{ scriptedLLM }

func (r *scriptedRenderer) RenderArtifactAsBlock(context.Context, ArtifactRefBlock, ArtifactFetcher) (Block, error) {
	return TextBlock{Text: "rendered"}, nil
}
func (r *scriptedRenderer) SupportsNative(ArtifactRefBlock) bool { return true }
func (r *scriptedRenderer) NativeRenderBudget() RenderBudget     { return RenderBudget{MaxBlocks: 7} }

// debugLog returns a JSON debug logger and a func decoding its single line.
func debugLog(t *testing.T) (*slog.Logger, func() map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return l, func() map[string]any {
		t.Helper()
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		if len(lines) != 1 || lines[0] == "" {
			t.Fatalf("want exactly one log line, got %q", buf.String())
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
}

func drain(t *testing.T, l LLM, req Request) ([]Event, error) {
	t.Helper()
	var evs []Event
	for ev, err := range l.Generate(context.Background(), req) {
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

func TestWithCallLog_LogsAdapterAndPassesEventsThrough(t *testing.T) {
	events := []Event{
		TextDeltaEvent{Delta: "hi"},
		MessageStopEvent{StopReason: "end_turn"},
		UsageEvent{InputTokens: 11, OutputTokens: 3},
	}
	logger, line := debugLog(t)
	wrapped := WithCallLog(&scriptedLLM{name: "bifrost:anthropic", events: events}, logger)

	got, err := drain(t, wrapped, Request{Model: "claude-sonnet-4-6", Messages: make([]Message, 2), Tools: make([]ToolDef, 3)})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, events) {
		t.Fatalf("events = %#v", got)
	}
	if wrapped.Name() != "bifrost:anthropic" {
		t.Fatalf("Name() = %q", wrapped.Name())
	}
	m := line()
	want := map[string]any{
		"level": "DEBUG", "msg": "llm call", "adapter": "bifrost:anthropic", "model": "claude-sonnet-4-6",
		"messages": float64(2), "tools": float64(3), "stop_reason": "end_turn",
		"input_tokens": float64(11), "output_tokens": float64(3),
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v (line %v)", k, m[k], v, m)
		}
	}
	if _, ok := m["duration_ms"]; !ok {
		t.Errorf("duration_ms missing: %v", m)
	}
	if _, ok := m["error"]; ok {
		t.Errorf("unexpected error attr: %v", m)
	}
}

func TestWithCallLog_LogsError(t *testing.T) {
	logger, line := debugLog(t)
	wrapped := WithCallLog(&scriptedLLM{name: "anthropic", err: errors.New("boom")}, logger)
	if _, err := drain(t, wrapped, Request{Model: "m"}); err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v", err)
	}
	if m := line(); m["error"] != "boom" || m["adapter"] != "anthropic" {
		t.Fatalf("line = %v", m)
	}
}

func TestWithCallLog_EarlyBreakStopsInnerAndStillLogs(t *testing.T) {
	inner := &scriptedLLM{name: "anthropic", events: []Event{TextDeltaEvent{Delta: "a"}, TextDeltaEvent{Delta: "b"}, TextDeltaEvent{Delta: "c"}}}
	logger, line := debugLog(t)
	for range WithCallLog(inner, logger).Generate(context.Background(), Request{Model: "m"}) {
		break
	}
	if inner.pulled != 1 {
		t.Fatalf("inner pulled %d events after consumer break, want 1", inner.pulled)
	}
	if m := line(); m["adapter"] != "anthropic" {
		t.Fatalf("line = %v", m)
	}
}

func TestWithCallLog_SilentAboveDebug(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if _, err := drain(t, WithCallLog(&scriptedLLM{name: "anthropic", events: []Event{MessageStopEvent{StopReason: "end_turn"}}}, logger), Request{}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("logged at info level: %s", buf.String())
	}
}

func TestWithCallLog_KeepsMultimodalRenderer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	r, ok := WithCallLog(&scriptedRenderer{scriptedLLM{name: "bifrost:openai"}}, logger).(MultimodalRenderer)
	if !ok {
		t.Fatal("wrapped renderer lost MultimodalRenderer")
	}
	if r.NativeRenderBudget().MaxBlocks != 7 || !r.SupportsNative(ArtifactRefBlock{}) {
		t.Fatal("renderer methods not forwarded")
	}
	if b, _ := r.RenderArtifactAsBlock(context.Background(), ArtifactRefBlock{}, nil); b != (TextBlock{Text: "rendered"}) {
		t.Fatalf("RenderArtifactAsBlock = %#v", b)
	}
	if _, ok := WithCallLog(&scriptedLLM{name: "x"}, logger).(MultimodalRenderer); ok {
		t.Fatal("non-renderer gained MultimodalRenderer")
	}
}

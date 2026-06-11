package jsonl

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
)

func TestJSONL_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	sink, err := NewFactory().NewWriterSink(&buf)
	if err != nil {
		t.Fatalf("NewWriterSink: %v", err)
	}

	ctx := context.Background()
	_ = sink.Send(ctx, transport.SessionStartEvent{SessionID: "s1", AgentID: "a1"})
	_ = sink.Send(ctx, transport.TextDeltaEvent{MessageID: "m1", Delta: "hi"})
	_ = sink.Send(ctx, transport.TurnEndEvent{StopReason: "end_turn", UsageIn: 10, UsageOut: 5})
	_ = sink.Close()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), buf.String())
	}

	// First line is session_start.
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("line 0 unmarshal: %v", err)
	}
	if first["type"] != "session_start" {
		t.Fatalf("line 0 type: got %v", first["type"])
	}

	// Second line is text_delta with the right delta value.
	var second struct {
		Type string `json:"type"`
		Data struct {
			MessageID string `json:"MessageID"`
			Delta     string `json:"Delta"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("line 1 unmarshal: %v", err)
	}
	if second.Type != "text_delta" {
		t.Fatalf("line 1 type: got %q", second.Type)
	}
	if second.Data.Delta != "hi" {
		t.Fatalf("line 1 delta: got %q", second.Data.Delta)
	}

	// Third line is run_finished.
	var third map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &third); err != nil {
		t.Fatalf("line 2 unmarshal: %v", err)
	}
	if third["type"] != "run_finished" {
		t.Fatalf("line 2 type: got %v", third["type"])
	}
}

func TestJSONL_FactoryContentType(t *testing.T) {
	if ct := NewFactory().ContentType(); ct != "application/x-ndjson" {
		t.Fatalf("ContentType: got %q", ct)
	}
}

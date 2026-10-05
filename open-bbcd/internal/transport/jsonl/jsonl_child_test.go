package jsonl

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
)

func jsonlFrames(t *testing.T, evs ...transport.Event) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	sink, _ := NewFactory().NewWriterSink(&buf)
	for _, ev := range evs {
		if err := sink.Send(context.Background(), ev); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestJSONL_StepEvents(t *testing.T) {
	f := jsonlFrames(t,
		transport.StepStartedEvent{StepName: "researcher", ToolCallID: "tu_1", ChildSessionID: "c1", Description: "find x"},
		transport.StepFinishedEvent{StepName: "researcher", ToolCallID: "tu_1", ChildSessionID: "c1", IsError: true},
	)
	if f[0]["type"] != "step_started" || f[1]["type"] != "step_finished" {
		t.Fatalf("types: %v %v", f[0]["type"], f[1]["type"])
	}
	d0 := f[0]["data"].(map[string]any)
	if d0["StepName"] != "researcher" || d0["ToolCallID"] != "tu_1" || d0["ChildSessionID"] != "c1" || d0["Description"] != "find x" {
		t.Fatalf("step_started data: %v", d0)
	}
	if d1 := f[1]["data"].(map[string]any); d1["IsError"] != true {
		t.Fatalf("step_finished data: %v", d1)
	}
}

func TestJSONL_ChildToolEventsCarryChildSessionID(t *testing.T) {
	f := jsonlFrames(t,
		transport.ToolCallStartEvent{ToolCallID: "tu_9", Name: "search", ChildSessionID: "c1"},
		transport.ToolCallArgsEvent{ToolCallID: "tu_9", ArgsJSON: "{}", ChildSessionID: "c1"},
		transport.ToolCallEndEvent{ToolCallID: "tu_9", ChildSessionID: "c1"},
		transport.ToolResultEvent{ToolCallID: "tu_9", Result: []byte(`{}`), ChildSessionID: "c1"},
	)
	want := []string{"tool_call_start", "tool_call_args", "tool_call_end", "tool_call_result"}
	for i, m := range f {
		if m["type"] != want[i] {
			t.Fatalf("frame %d type: %v", i, m["type"])
		}
		d := m["data"].(map[string]any)
		if d["child_session_id"] != "c1" || d["ToolCallID"] != "tu_9" {
			t.Fatalf("frame %d data: %v", i, d)
		}
	}
}

func TestJSONL_RootToolEventsHaveNoChildSessionID(t *testing.T) {
	var buf bytes.Buffer
	sink, _ := NewFactory().NewWriterSink(&buf)
	_ = sink.Send(context.Background(), transport.ToolCallStartEvent{ToolCallID: "tu_9", Name: "search"})
	if got, want := buf.String(), `{"data":{"ToolCallID":"tu_9","Name":"search"},"type":"tool_call_start"}`+"\n"; got != want {
		t.Fatalf("root frame changed:\n got %q\nwant %q", got, want)
	}
}

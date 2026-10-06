package agui

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
)

// sseObjects sends evs through a fresh AG-UI writer sink and returns the
// JSON object of every SSE `data:` line, in order.
func sseObjects(t *testing.T, evs ...transport.Event) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	s := newWriterSink(&buf)
	for _, ev := range evs {
		if err := s.Send(context.Background(), ev); err != nil {
			t.Fatalf("Send(%T): %v", ev, err)
		}
	}
	var out []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(data), &obj); err != nil {
			t.Fatalf("unmarshal %q: %v", data, err)
		}
		out = append(out, obj)
	}
	if len(out) != len(evs) {
		t.Fatalf("got %d data lines for %d events: %q", len(out), len(evs), buf.String())
	}
	return out
}

func assertRaw(t *testing.T, obj map[string]any, want map[string]any) {
	t.Helper()
	got, ok := obj["rawEvent"].(map[string]any)
	if !ok {
		t.Fatalf("rawEvent missing or not an object in %v", obj)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rawEvent: got %v, want %v", got, want)
	}
}

func TestAGUI_StepStarted(t *testing.T) {
	obj := sseObjects(t, transport.StepStartedEvent{StepName: "researcher", ToolCallID: "tu_1", ChildSessionID: "c1", Description: "find x"})[0]
	if obj["type"] != "STEP_STARTED" {
		t.Fatalf("type: %v", obj["type"])
	}
	if obj["stepName"] != "researcher:tu_1" {
		t.Fatalf("stepName: %v", obj["stepName"])
	}
	assertRaw(t, obj, map[string]any{"childSessionId": "c1", "parentToolCallId": "tu_1", "description": "find x"})
}

func TestAGUI_StepFinished(t *testing.T) {
	obj := sseObjects(t, transport.StepFinishedEvent{StepName: "researcher", ToolCallID: "tu_1", ChildSessionID: "c1", IsError: true})[0]
	if obj["type"] != "STEP_FINISHED" {
		t.Fatalf("type: %v", obj["type"])
	}
	if obj["stepName"] != "researcher:tu_1" {
		t.Fatalf("stepName: %v", obj["stepName"])
	}
	assertRaw(t, obj, map[string]any{"childSessionId": "c1", "isError": true})
}

func TestAGUI_StepToolCallIDNeverRePrefixed(t *testing.T) {
	objs := sseObjects(t,
		transport.StepStartedEvent{StepName: "researcher", ToolCallID: "c0:tu_3", ChildSessionID: "c1", Description: "d"},
		transport.StepFinishedEvent{StepName: "researcher", ToolCallID: "c0:tu_3", ChildSessionID: "c1"},
	)
	for _, obj := range objs {
		if obj["stepName"] != "researcher:c0:tu_3" {
			t.Fatalf("stepName: %v", obj["stepName"])
		}
	}
	assertRaw(t, objs[0], map[string]any{"childSessionId": "c1", "parentToolCallId": "c0:tu_3", "description": "d"})
	assertRaw(t, objs[1], map[string]any{"childSessionId": "c1", "isError": false})
}

func TestAGUI_ChildToolEventsPrefixed(t *testing.T) {
	objs := sseObjects(t,
		transport.ToolCallStartEvent{ToolCallID: "tu_9", Name: "search", ChildSessionID: "c1"},
		transport.ToolCallArgsEvent{ToolCallID: "tu_9", ArgsJSON: `{"q":"x"}`, ChildSessionID: "c1"},
		transport.ToolCallEndEvent{ToolCallID: "tu_9", ChildSessionID: "c1"},
		transport.ToolResultEvent{ToolCallID: "tu_9", Result: []byte(`{"ok":true}`), ChildSessionID: "c1"},
	)
	wantTypes := []string{"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT"}
	for i, obj := range objs {
		if obj["type"] != wantTypes[i] {
			t.Fatalf("event %d type: %v", i, obj["type"])
		}
		if obj["toolCallId"] != "c1:tu_9" {
			t.Fatalf("event %d toolCallId: %v", i, obj["toolCallId"])
		}
		assertRaw(t, obj, map[string]any{"childSessionId": "c1"})
	}
	if objs[0]["toolCallName"] != "search" {
		t.Fatalf("toolCallName: %v", objs[0]["toolCallName"])
	}
	if objs[3]["messageId"] != "c1:tu_9" {
		t.Fatalf("result messageId: %v", objs[3]["messageId"])
	}
}

// Root (non-child) tool events must serialise exactly as before the
// multi-agent change. Expected bytes captured from the pre-change mapper;
// only the per-event timestamp (and the SSE id derived from it) is volatile.
func TestAGUI_RootToolEventsUnchanged(t *testing.T) {
	var buf bytes.Buffer
	s := newWriterSink(&buf)
	ctx := context.Background()
	for _, ev := range []transport.Event{
		transport.ToolCallStartEvent{ToolCallID: "tu_9", Name: "search"},
		transport.ToolCallArgsEvent{ToolCallID: "tu_9", ArgsJSON: `{"q":"x"}`},
		transport.ToolCallEndEvent{ToolCallID: "tu_9"},
		transport.ToolResultEvent{ToolCallID: "tu_9", Result: []byte(`{"ok":true}`), IsError: false},
	} {
		if err := s.Send(ctx, ev); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	got := regexp.MustCompile(`"timestamp":\d+`).ReplaceAllString(buf.String(), `"timestamp":0`)
	got = regexp.MustCompile(`(?m)^id: ([A-Z_]+)_\d+$`).ReplaceAllString(got, `id: ${1}_0`)

	const want = "id: TOOL_CALL_START_0\ndata: {\"type\":\"TOOL_CALL_START\",\"timestamp\":0,\"toolCallId\":\"tu_9\",\"toolCallName\":\"search\"}\n\n" +
		"id: TOOL_CALL_ARGS_0\ndata: {\"type\":\"TOOL_CALL_ARGS\",\"timestamp\":0,\"toolCallId\":\"tu_9\",\"delta\":\"{\\\"q\\\":\\\"x\\\"}\"}\n\n" +
		"id: TOOL_CALL_END_0\ndata: {\"type\":\"TOOL_CALL_END\",\"timestamp\":0,\"toolCallId\":\"tu_9\"}\n\n" +
		"id: TOOL_CALL_RESULT_0\ndata: {\"type\":\"TOOL_CALL_RESULT\",\"timestamp\":0,\"messageId\":\"tu_9\",\"toolCallId\":\"tu_9\",\"content\":\"{\\\"is_error\\\":false,\\\"result\\\":{\\\"ok\\\":true}}\",\"role\":\"tool\"}\n\n"
	if got != want {
		t.Fatalf("root tool events changed:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(buf.String(), "rawEvent") {
		t.Fatalf("root events must not carry rawEvent: %q", buf.String())
	}
}

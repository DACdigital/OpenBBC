package agui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
)

func TestAGUI_TextDeltaMapping(t *testing.T) {
	var buf bytes.Buffer
	sink := newWriterSink(&buf)

	ctx := context.Background()
	_ = sink.Send(ctx, transport.SessionStartEvent{SessionID: "s1", AgentID: "a1"})
	_ = sink.Send(ctx, transport.TextStartEvent{MessageID: "m1"})
	_ = sink.Send(ctx, transport.TextDeltaEvent{MessageID: "m1", Delta: "hi"})
	_ = sink.Send(ctx, transport.TextEndEvent{MessageID: "m1"})
	_ = sink.Close()

	out := buf.String()
	if !strings.Contains(out, "TEXT_MESSAGE_START") {
		t.Fatalf("expected TEXT_MESSAGE_START in output, got: %q", out)
	}
	if !strings.Contains(out, "TEXT_MESSAGE_CONTENT") {
		t.Fatalf("expected TEXT_MESSAGE_CONTENT in output")
	}
	if !strings.Contains(out, "TEXT_MESSAGE_END") {
		t.Fatalf("expected TEXT_MESSAGE_END in output")
	}
}

func TestAGUI_ToolCallMapping(t *testing.T) {
	var buf bytes.Buffer
	sink := newWriterSink(&buf)

	ctx := context.Background()
	_ = sink.Send(ctx, transport.ToolCallStartEvent{ToolCallID: "tc_1", Name: "Skill"})
	_ = sink.Send(ctx, transport.ToolCallArgsEvent{ToolCallID: "tc_1", ArgsJSON: `{"name":"x"}`})
	_ = sink.Send(ctx, transport.ToolCallEndEvent{ToolCallID: "tc_1"})
	_ = sink.Send(ctx, transport.ToolResultEvent{ToolCallID: "tc_1", Result: []byte(`{"ok":true}`), IsError: false})
	_ = sink.Close()

	out := buf.String()
	for _, want := range []string{"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %s in output, got: %q", want, out)
		}
	}
}

func TestAGUI_TurnEnd(t *testing.T) {
	var buf bytes.Buffer
	sink := newWriterSink(&buf)
	_ = sink.Send(context.Background(), transport.SessionStartEvent{SessionID: "s1", AgentID: "a1"})
	_ = sink.Send(context.Background(), transport.TurnEndEvent{StopReason: "end_turn", UsageIn: 10, UsageOut: 5})
	_ = sink.Close()
	if !strings.Contains(buf.String(), "RUN_FINISHED") {
		t.Fatalf("expected RUN_FINISHED in output, got: %q", buf.String())
	}
}

func TestAGUI_FactoryContentType(t *testing.T) {
	if ct := NewFactory().ContentType(); ct != "text/event-stream" {
		t.Fatalf("ContentType: got %q", ct)
	}
}

func TestAGUI_ArtifactRefIsCustomEvent(t *testing.T) {
	var buf bytes.Buffer
	sink := newWriterSink(&buf)
	_ = sink.Send(context.Background(), transport.ArtifactRefEvent{
		ToolCallID: "tc_1", StoreID: "MAIN", URI: "sha256/abc", MIME: "image/png", SizeBytes: 1234, Sha256: "abc",
	})
	out := buf.String()
	for _, want := range []string{`"type":"CUSTOM"`, `"name":"ARTIFACT_REF"`, `"toolCallId":"tc_1"`, `"storeId":"MAIN"`, `"uri":"sha256/abc"`, `"mime":"image/png"`, `"sizeBytes":1234`, `"sha256":"abc"`, `"filename":null`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %q", want, out)
		}
	}
}

func TestAGUI_ArtifactRefWithFilename(t *testing.T) {
	var buf bytes.Buffer
	sink := newWriterSink(&buf)
	_ = sink.Send(context.Background(), transport.ArtifactRefEvent{ToolCallID: "tc_1", StoreID: "MAIN", URI: "sha256/abc", Filename: "chart.png"})
	if out := buf.String(); !strings.Contains(out, `"filename":"chart.png"`) {
		t.Fatalf("missing filename in %q", out)
	}
}

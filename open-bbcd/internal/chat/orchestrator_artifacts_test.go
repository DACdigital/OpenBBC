package chat

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// onePixelPNG is a valid 1x1 PNG, base64-encoded.
const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"

func imageToolOutput() []byte {
	return []byte(`{"content":[{"type":"image","data":"` + onePixelPNG + `","mimeType":"image/png"}]}`)
}

// toolUseRound scripts one LLM round that calls the "Skill" tool once per id.
func toolUseRound(ids ...string) []llm.Event {
	var evs []llm.Event
	for _, id := range ids {
		evs = append(evs,
			llm.ToolUseStartEvent{ID: id, Name: "Skill"},
			llm.ToolUseInputEvent{ID: id, JSONFragment: `{}`},
			llm.ToolUseEndEvent{ID: id},
		)
	}
	return append(evs, llm.MessageStopEvent{StopReason: "tool_use"})
}

func endRound() []llm.Event {
	return []llm.Event{llm.TextDeltaEvent{Delta: "done"}, llm.MessageStopEvent{StopReason: "end_turn"}}
}

// newArtifactOrchestrator builds an orchestrator over fakes with the
// tool-result uploader wired. results are returned by the fake tool in call order.
func newArtifactOrchestrator(t *testing.T, l llm.LLM, results []tools.Result) (*Orchestrator, *fakeChatRepo, *stubUploader) {
	t.Helper()
	version := &types.AgentVersion{ID: "v1", Prompts: []byte(`{"main_prompt":"sys"}`)}
	agents := &fakeAgentRepo{version: version, agent: &types.Agent{ID: "a1", Architecture: []byte(`{}`)}}
	chats := &fakeChatRepo{}
	up := &stubUploader{}
	o := NewOrchestrator(agents, chats, l, &fakeBuilder{handler: &fakeTools{results: results}}, slog.Default())
	o.WithArtifactUploader(up)
	return o, chats, up
}

func messagesWithRole(chats *fakeChatRepo, role types.ChatRole) []types.ChatMessage {
	var out []types.ChatMessage
	for _, m := range chats.messages {
		if m.Role == role {
			out = append(out, m)
		}
	}
	return out
}

func TestOrchestrator_ArtifactRefsSurviveHistoryReload(t *testing.T) {
	flm := &fakeLLM{script: [][]llm.Event{endRound(), endRound()}}
	o, _, _ := newArtifactOrchestrator(t, flm, nil)
	ctx := context.Background()

	ref := llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/abc", MIME: "application/pdf", SizeBytes: 42, Sha256: "abc", Filename: "q3.pdf"}
	if err := o.Turn(ctx, "v1", "s1", []llm.Block{llm.TextBlock{Text: "summarise"}, ref}, &recordingSink{}); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if err := o.Turn(ctx, "v1", "s1", []llm.Block{llm.TextBlock{Text: "and again"}}, &recordingSink{}); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	// Turn 2's request starts with turn 1's user message. No resolver is
	// wired, so the ref is rendered as its text surrogate — but it must be there.
	first := flm.requests[1].Messages[0]
	if len(first.Content) != 2 {
		t.Fatalf("reloaded user message has %d blocks, want 2 (ref dropped from history?)", len(first.Content))
	}
	tb, ok := first.Content[1].(llm.TextBlock)
	if !ok || !strings.Contains(tb.Text, "[Attachment: q3.pdf") {
		t.Fatalf("second block = %#v, want the q3.pdf text surrogate", first.Content[1])
	}
}

func TestOrchestrator_ToolMessage_ResultsBeforeRefs(t *testing.T) {
	flm := &fakeLLM{script: [][]llm.Event{toolUseRound("tu1", "tu2"), endRound()}}
	o, chats, _ := newArtifactOrchestrator(t, flm, []tools.Result{
		{ToolUseID: "tu1", Output: imageToolOutput()},
		{ToolUseID: "tu2", Output: []byte(`{}`)},
	})
	if err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "go"}}, &recordingSink{}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	tm := messagesWithRole(chats, types.ChatRoleTool)
	if len(tm) != 1 {
		t.Fatalf("tool messages = %d, want 1", len(tm))
	}
	got := blockTypes(t, tm[0].Content)
	want := []string{"tool_result", "tool_result", "artifact_ref"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tool message block order = %v, want %v", got, want)
	}
}

func TestOrchestrator_ErrorFlaggedToolResultIsNormalised(t *testing.T) {
	flm := &fakeLLM{script: [][]llm.Event{toolUseRound("tu1"), endRound()}}
	o, chats, up := newArtifactOrchestrator(t, flm, []tools.Result{
		{ToolUseID: "tu1", Output: imageToolOutput(), IsError: true},
	})
	if err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "go"}}, &recordingSink{}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if up.uploads != 1 {
		t.Fatalf("uploads = %d, want 1 (isError results must be normalised)", up.uploads)
	}
	tm := messagesWithRole(chats, types.ChatRoleTool)
	if strings.Contains(string(tm[0].Content), onePixelPNG) {
		t.Fatalf("base64 persisted in tool message: %s", tm[0].Content)
	}
	if !strings.Contains(string(tm[0].Content), `"is_error":true`) {
		t.Fatalf("is_error lost: %s", tm[0].Content)
	}
}

func eventIndex(events []transport.Event, match func(transport.Event) bool) int {
	for i, e := range events {
		if match(e) {
			return i
		}
	}
	return -1
}

func TestOrchestrator_ArtifactRefAfterToolMessagePersisted(t *testing.T) {
	flm := &fakeLLM{script: [][]llm.Event{toolUseRound("tu1", "tu2"), endRound()}}
	o, _, _ := newArtifactOrchestrator(t, flm, []tools.Result{
		{ToolUseID: "tu1", Output: imageToolOutput()},
		{ToolUseID: "tu2", Output: []byte(`{}`)},
	})
	sink := &recordingSink{}
	if err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "go"}}, sink); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	refIdx := eventIndex(sink.events, func(e transport.Event) bool { _, ok := e.(transport.ArtifactRefEvent); return ok })
	lastResult := -1
	for i, e := range sink.events {
		if tr, ok := e.(transport.ToolResultEvent); ok {
			lastResult = i
			if strings.Contains(string(tr.Result), onePixelPNG) {
				t.Fatalf("ToolResultEvent carries base64: %s", tr.Result)
			}
		}
	}
	if refIdx < 0 {
		t.Fatal("no ArtifactRefEvent sent")
	}
	if refIdx < lastResult {
		t.Fatalf("ArtifactRefEvent (idx %d) sent before the round's last tool result (idx %d)", refIdx, lastResult)
	}
	ev := sink.events[refIdx].(transport.ArtifactRefEvent)
	if ev.ToolCallID != "tu1" || ev.StoreID != "MAIN" || ev.MIME != "image/png" {
		t.Fatalf("event = %#v", ev)
	}
}

func TestOrchestrator_NoArtifactRefWhenToolMessagePersistFails(t *testing.T) {
	flm := &fakeLLM{script: [][]llm.Event{toolUseRound("tu1"), endRound()}}
	o, chats, _ := newArtifactOrchestrator(t, flm, []tools.Result{{ToolUseID: "tu1", Output: imageToolOutput()}})
	chats.failRole = types.ChatRoleTool
	sink := &recordingSink{}
	if err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "go"}}, sink); err == nil {
		t.Fatal("expected Turn error when the tool message fails to persist")
	}
	if eventIndex(sink.events, func(e transport.Event) bool {
		tr, ok := e.(transport.ToolResultEvent)
		return ok && tr.ToolCallID == "tu1"
	}) < 0 {
		t.Fatal("ToolResultEvent for tu1 should still have been sent")
	}
	if eventIndex(sink.events, func(e transport.Event) bool { _, ok := e.(transport.ArtifactRefEvent); return ok }) >= 0 {
		t.Fatal("ArtifactRefEvent sent although the tool message was not persisted")
	}
}

func TestOrchestrator_ArtifactRefOrder_ToolCallThenItem(t *testing.T) {
	img := `{"type":"image","data":"` + onePixelPNG + `","mimeType":"image/png"}`
	flm := &fakeLLM{script: [][]llm.Event{toolUseRound("tu1", "tu2"), endRound()}}
	o, _, _ := newArtifactOrchestrator(t, flm, []tools.Result{
		{ToolUseID: "tu1", Output: []byte(`{"content":[` + img + `,` + img + `]}`)},
		{ToolUseID: "tu2", Output: []byte(`{"content":[` + img + `]}`)},
	})
	sink := &recordingSink{}
	if err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "go"}}, sink); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	var refIDs []string
	firstRef, lastResult := -1, -1
	for i, e := range sink.events {
		switch ev := e.(type) {
		case transport.ArtifactRefEvent:
			if firstRef < 0 {
				firstRef = i
			}
			refIDs = append(refIDs, ev.ToolCallID)
		case transport.ToolResultEvent:
			lastResult = i
		}
	}
	if want := []string{"tu1", "tu1", "tu2"}; !reflect.DeepEqual(refIDs, want) {
		t.Fatalf("ref order = %v, want %v", refIDs, want)
	}
	if firstRef < lastResult {
		t.Fatalf("first ArtifactRefEvent (idx %d) before last ToolResultEvent (idx %d)", firstRef, lastResult)
	}
}

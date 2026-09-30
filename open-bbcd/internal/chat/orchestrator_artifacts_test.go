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
	o, chats, _ := newArtifactOrchestrator(t, flm, nil)
	ctx := context.Background()

	ref := llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/abc", MIME: "application/pdf", SizeBytes: 42, Sha256: "abc", Filename: "q3.pdf"}
	chats.pending = map[string][]llm.ArtifactRefBlock{"s1": {ref}}
	if err := o.Turn(ctx, "v1", "s1", []llm.Block{llm.TextBlock{Text: "summarise"}}, &recordingSink{}); err != nil {
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

func countInline(msgs []llm.Message) (inline, refs int) {
	for _, m := range msgs {
		for _, b := range m.Content {
			switch b.(type) {
			case llm.InlineMediaBlock:
				inline++
			case llm.ArtifactRefBlock:
				refs++
			}
		}
	}
	return
}

func TestOrchestrator_BudgetHoldsAcrossToolRounds(t *testing.T) {
	flm := &mmFakeLLM{
		fakeLLM: &fakeLLM{script: [][]llm.Event{toolUseRound("t1"), toolUseRound("t2"), toolUseRound("t3"), endRound()}},
		budget:  llm.RenderBudget{MaxBlocks: 2},
	}
	o, chats, _ := newArtifactOrchestrator(t, flm, []tools.Result{
		{ToolUseID: "t1", Output: imageToolOutput()},
		{ToolUseID: "t2", Output: imageToolOutput()},
		{ToolUseID: "t3", Output: imageToolOutput()},
	})
	o.WithArtifacts(func(string) llm.ArtifactFetcher { return &countingFetcher{} })
	if err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "go"}}, &recordingSink{}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	final := flm.requests[len(flm.requests)-1]
	inline, refs := countInline(final.Messages)
	if inline != 2 {
		t.Fatalf("final call carries %d native blocks, want 2", inline)
	}
	if refs != 0 {
		t.Fatalf("rendered request still carries %d raw refs", refs)
	}

	// The two native blocks must be the newest: one in each of the last
	// two tool messages, none in the first.
	var toolMsgs []llm.Message
	for _, m := range final.Messages {
		if m.Role == llm.RoleTool {
			toolMsgs = append(toolMsgs, m)
		}
	}
	if len(toolMsgs) != 3 {
		t.Fatalf("final request has %d tool messages, want 3", len(toolMsgs))
	}
	for i, want := range []int{0, 1, 1} {
		if got, _ := countInline(toolMsgs[i : i+1]); got != want {
			t.Fatalf("tool message %d carries %d native blocks, want %d", i, got, want)
		}
	}

	// Nothing rendered may reach persistence: every persisted block is one
	// of the ref-form types.
	allowed := map[string]bool{"text": true, "tool_use": true, "tool_result": true, "artifact_ref": true}
	for _, m := range chats.messages {
		for _, bt := range blockTypes(t, m.Content) {
			if !allowed[bt] {
				t.Fatalf("persisted %s message has unexpected block type %q", m.Role, bt)
			}
		}
	}
}

func TestOrchestrator_BlobFetchedOncePerTurn(t *testing.T) {
	flm := &mmFakeLLM{fakeLLM: &fakeLLM{script: [][]llm.Event{toolUseRound("t1"), toolUseRound("t2"), endRound()}}}
	o, _, _ := newArtifactOrchestrator(t, flm, []tools.Result{
		{ToolUseID: "t1", Output: imageToolOutput()},
		{ToolUseID: "t2", Output: []byte(`{}`)},
	})
	f := &countingFetcher{}
	o.WithArtifacts(func(string) llm.ArtifactFetcher { return f })
	if err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "go"}}, &recordingSink{}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	// The image ref is sent on calls 2 and 3.
	if f.gets != 1 {
		t.Fatalf("blob fetched %d times in one turn, want 1", f.gets)
	}
}

// With no artifact uploader wired (registry disabled), tool results are not
// normalised: the ImageContent is streamed and persisted unchanged, no
// tool_result rows are recorded and no ARTIFACT_REF is sent.
func TestOrchestrator_NoUploader_ToolResultPassesThroughUnchanged(t *testing.T) {
	flm := &fakeLLM{script: [][]llm.Event{toolUseRound("tu1"), endRound()}}
	version := &types.AgentVersion{ID: "v1", Prompts: []byte(`{"main_prompt":"sys"}`)}
	agents := &fakeAgentRepo{version: version, agent: &types.Agent{ID: "a1", Architecture: []byte(`{}`)}}
	chats := &fakeChatRepo{}
	out := imageToolOutput()
	o := NewOrchestrator(agents, chats, flm, &fakeBuilder{handler: &fakeTools{results: []tools.Result{{ToolUseID: "tu1", Output: out}}}}, slog.Default())
	sink := &recordingSink{}
	if err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "go"}}, sink); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	var streamed []transport.ToolResultEvent
	for _, ev := range sink.events {
		switch e := ev.(type) {
		case transport.ToolResultEvent:
			streamed = append(streamed, e)
		case transport.ArtifactRefEvent:
			t.Fatalf("ARTIFACT_REF sent with no uploader: %+v", e)
		}
	}
	if len(streamed) != 1 || streamed[0].ToolCallID != "tu1" || string(streamed[0].Result) != string(out) {
		t.Fatalf("streamed tool results = %+v, want the raw output once", streamed)
	}
	tm := messagesWithRole(chats, types.ChatRoleTool)
	if len(tm) != 1 {
		t.Fatalf("tool messages = %d, want 1", len(tm))
	}
	if !strings.Contains(string(tm[0].Content), onePixelPNG) {
		t.Fatalf("raw base64 not persisted: %s", tm[0].Content)
	}
	if got := blockTypes(t, tm[0].Content); !reflect.DeepEqual(got, []string{"tool_result"}) {
		t.Fatalf("tool message block types = %v, want [tool_result]", got)
	}
	if len(chats.toolRows) != 0 {
		t.Fatalf("toolRows = %+v, want none", chats.toolRows)
	}
}

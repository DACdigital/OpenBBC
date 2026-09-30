package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

var (
	refA = llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/aa", MIME: "application/pdf", SizeBytes: 10, Sha256: "aa", Filename: "a.pdf"}
	refB = llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/bb", MIME: "application/pdf", SizeBytes: 20, Sha256: "bb"}
)

func TestOrchestrator_UserTurnClaimsPendingAfterText(t *testing.T) {
	flm := &fakeLLM{script: [][]llm.Event{endRound()}}
	o, chats, _ := newArtifactOrchestrator(t, flm, nil)
	chats.pending = map[string][]llm.ArtifactRefBlock{"s1": {refA, refB}}
	sink := &recordingSink{}

	if err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "summarise"}}, sink); err != nil {
		t.Fatal(err)
	}
	user := messagesWithRole(chats, types.ChatRoleUser)[0]
	if got := blockTypes(t, user.Content); len(got) != 3 || got[0] != "text" || got[1] != "artifact_ref" || got[2] != "artifact_ref" {
		t.Fatalf("persisted user types = %v", got)
	}
	// The LLM request's user message carries text + both refs (rendered as
	// surrogates here — no resolver is wired).
	last := flm.requests[0].Messages[len(flm.requests[0].Messages)-1]
	if last.Role != llm.RoleUser || len(last.Content) != 3 {
		t.Fatalf("LLM user message = %+v", last)
	}
	for i, ref := range []llm.ArtifactRefBlock{refA, refB} {
		if got, want := last.Content[i+1], llm.TextSurrogate(ref); got != want {
			t.Fatalf("LLM user block %d = %#v, want surrogate %#v", i+1, got, want)
		}
	}
	for _, ev := range sink.events {
		if _, ok := ev.(transport.ArtifactRefEvent); ok {
			t.Fatal("ARTIFACT_REF emitted for a claimed user upload")
		}
	}
}

func TestOrchestrator_InputArtifactRefIsNotPersisted(t *testing.T) {
	flm := &fakeLLM{script: [][]llm.Event{endRound()}}
	o, chats, _ := newArtifactOrchestrator(t, flm, nil)
	if err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "hi"}, refA}, &recordingSink{}); err != nil {
		t.Fatal(err)
	}
	user := messagesWithRole(chats, types.ChatRoleUser)[0]
	if got := blockTypes(t, user.Content); len(got) != 1 || got[0] != "text" {
		t.Fatalf("persisted user types = %v (input ref must be dropped)", got)
	}
	last := flm.requests[0].Messages[len(flm.requests[0].Messages)-1]
	if last.Role != llm.RoleUser || len(last.Content) != 1 {
		t.Fatalf("LLM user message = %+v, want the text block only", last)
	}
}

func TestOrchestrator_ArtifactOnlyTurnAccepted(t *testing.T) {
	flm := &fakeLLM{script: [][]llm.Event{endRound()}}
	o, chats, _ := newArtifactOrchestrator(t, flm, nil)
	chats.pending = map[string][]llm.ArtifactRefBlock{"s1": {refA}}
	if err := o.Turn(context.Background(), "v1", "s1", nil, &recordingSink{}); err != nil {
		t.Fatal(err)
	}
	user := messagesWithRole(chats, types.ChatRoleUser)[0]
	if got := blockTypes(t, user.Content); len(got) != 1 || got[0] != "artifact_ref" {
		t.Fatalf("persisted user types = %v", got)
	}
}

func TestOrchestrator_EmptyTurnRace_RunErrorAndNothingPersisted(t *testing.T) {
	flm := &fakeLLM{script: [][]llm.Event{endRound()}}
	o, chats, _ := newArtifactOrchestrator(t, flm, nil)
	chats.pending = map[string][]llm.ArtifactRefBlock{"s1": {refA}}
	// The handler's validity check saw a pending artifact; a DELETE lands
	// before the claim.
	chats.beforeClaim = func() {
		chats.mu.Lock()
		delete(chats.pending, "s1")
		chats.mu.Unlock()
	}
	sink := &recordingSink{}
	err := o.Turn(context.Background(), "v1", "s1", nil, sink)
	if !errors.Is(err, types.ErrEmptyTurn) {
		t.Fatalf("err = %v, want ErrEmptyTurn", err)
	}
	var sawEmpty bool
	for _, ev := range sink.events {
		if e, ok := ev.(transport.ErrorEvent); ok && e.Code == "empty_turn" {
			sawEmpty = true
		}
	}
	if !sawEmpty {
		t.Fatalf("no RUN_ERROR empty_turn in %#v", sink.events)
	}
	for _, ev := range sink.events {
		if _, ok := ev.(transport.SessionStartEvent); ok {
			t.Fatal("SessionStartEvent sent for an empty turn")
		}
	}
	if n := len(messagesWithRole(chats, types.ChatRoleUser)); n != 0 {
		t.Fatalf("persisted %d user messages", n)
	}
	if flm.calls != 0 {
		t.Fatal("LLM called for an empty turn")
	}
}

func TestOrchestrator_ToolResultRefsRecordedWithToolMessage(t *testing.T) {
	flm := &fakeLLM{script: [][]llm.Event{toolUseRound("t1"), endRound()}}
	o, chats, _ := newArtifactOrchestrator(t, flm, []tools.Result{{ToolUseID: "t1", Output: imageToolOutput()}})
	if err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "go"}}, &recordingSink{}); err != nil {
		t.Fatal(err)
	}
	if len(chats.toolRows) != 1 || chats.toolRows[0].Ref.URI == "" {
		t.Fatalf("toolRows = %+v, want one ref", chats.toolRows)
	}
	tool := messagesWithRole(chats, types.ChatRoleTool)[0]
	if chats.toolRows[0].MessageID != tool.ID {
		t.Fatalf("tool row message id = %q, want persisted tool message %q", chats.toolRows[0].MessageID, tool.ID)
	}
	if got := blockTypes(t, tool.Content); len(got) != 2 || got[0] != "tool_result" || got[1] != "artifact_ref" {
		t.Fatalf("tool message types = %v", got)
	}
}

func TestOrchestrator_LLMFailureKeepsClaimedRefsForNextTurn(t *testing.T) {
	// Script index == call index, so call 0 (the failing one) gets a nil slot.
	flm := &fakeLLM{script: [][]llm.Event{nil, endRound()}, failCall: map[int]error{0: errors.New("upstream 500")}}
	o, chats, _ := newArtifactOrchestrator(t, flm, nil)
	chats.pending = map[string][]llm.ArtifactRefBlock{"s1": {refA}}
	if err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "one"}}, &recordingSink{}); err == nil {
		t.Fatal("turn 1 should fail")
	}
	if len(chats.pending["s1"]) != 0 {
		t.Fatal("ref not consumed by the failed turn")
	}
	if err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "two"}}, &recordingSink{}); err != nil {
		t.Fatal(err)
	}
	// Turn 2's request: history user message (text + ref) + new user message.
	req := flm.requests[len(flm.requests)-1]
	if len(req.Messages) < 2 || len(req.Messages[0].Content) != 2 {
		t.Fatalf("history user message should carry text + ref: %+v", req.Messages)
	}
}

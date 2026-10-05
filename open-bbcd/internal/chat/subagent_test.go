package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
)

func newTestChildSink(parent transport.Sink) *childSink {
	return newChildSink(newLockedSink(parent), "child-1", "researcher", "tu_parent", "find facts")
}

func TestChildSink_StartFinish(t *testing.T) {
	rec := &recordingSink{}
	cs := newTestChildSink(rec)
	ctx := context.Background()
	if err := cs.start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cs.finish(ctx, true); err != nil {
		t.Fatal(err)
	}
	want := []transport.Event{
		transport.StepStartedEvent{StepName: "researcher", ToolCallID: "tu_parent", ChildSessionID: "child-1", Description: "find facts"},
		transport.StepFinishedEvent{StepName: "researcher", ToolCallID: "tu_parent", ChildSessionID: "child-1", IsError: true},
	}
	if !reflect.DeepEqual(rec.events, want) {
		t.Fatalf("events = %#v\nwant %#v", rec.events, want)
	}
}

func TestChildSink_TagsOwnToolEvents(t *testing.T) {
	rec := &recordingSink{}
	cs := newTestChildSink(rec)
	ctx := context.Background()
	in := []transport.Event{
		transport.ToolCallStartEvent{ToolCallID: "tu_1", Name: "search"},
		transport.ToolCallArgsEvent{ToolCallID: "tu_1", ArgsJSON: `{"q":1}`},
		transport.ToolCallEndEvent{ToolCallID: "tu_1"},
		transport.ToolResultEvent{ToolCallID: "tu_1", Result: json.RawMessage(`{"ok":true}`), IsError: true},
	}
	for _, ev := range in {
		if err := cs.Send(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	want := []transport.Event{
		transport.ToolCallStartEvent{ToolCallID: "tu_1", Name: "search", ChildSessionID: "child-1"},
		transport.ToolCallArgsEvent{ToolCallID: "tu_1", ArgsJSON: `{"q":1}`, ChildSessionID: "child-1"},
		transport.ToolCallEndEvent{ToolCallID: "tu_1", ChildSessionID: "child-1"},
		transport.ToolResultEvent{ToolCallID: "tu_1", Result: json.RawMessage(`{"ok":true}`), IsError: true, ChildSessionID: "child-1"},
	}
	if !reflect.DeepEqual(rec.events, want) {
		t.Fatalf("events = %#v\nwant %#v", rec.events, want)
	}
}

func TestChildSink_DropsChildOwnNonToolEvents(t *testing.T) {
	rec := &recordingSink{}
	cs := newTestChildSink(rec)
	ctx := context.Background()
	for _, ev := range []transport.Event{
		transport.SessionStartEvent{SessionID: "child-1", AgentID: "a"},
		transport.TextStartEvent{MessageID: "m"},
		transport.TextDeltaEvent{MessageID: "m", Delta: "x"},
		transport.TextEndEvent{MessageID: "m"},
		transport.ArtifactRefEvent{ToolCallID: "tu_1", StoreID: "s"},
		transport.TurnEndEvent{StopReason: "end_turn"},
		transport.ErrorEvent{Code: "c", Message: "m"},
	} {
		if err := cs.Send(ctx, ev); err != nil {
			t.Fatalf("Send(%T) = %v, want nil", ev, err)
		}
	}
	if len(rec.events) != 0 {
		t.Fatalf("parent received %d events, want 0: %#v", len(rec.events), rec.events)
	}
}

func TestChildSink_ForwardsDeeperEventsUnchanged(t *testing.T) {
	rec := &recordingSink{}
	cs := newTestChildSink(rec)
	ctx := context.Background()
	in := []transport.Event{
		transport.StepStartedEvent{StepName: "writer", ToolCallID: "child-1:tu_2", ChildSessionID: "grand-1", Description: "d"},
		transport.ToolCallStartEvent{ToolCallID: "tu_9", Name: "x", ChildSessionID: "grand-1"},
		transport.ToolCallArgsEvent{ToolCallID: "tu_9", ArgsJSON: "{}", ChildSessionID: "grand-1"},
		transport.ToolCallEndEvent{ToolCallID: "tu_9", ChildSessionID: "grand-1"},
		transport.ToolResultEvent{ToolCallID: "tu_9", Result: json.RawMessage(`"r"`), ChildSessionID: "grand-1"},
		transport.StepFinishedEvent{StepName: "writer", ToolCallID: "child-1:tu_2", ChildSessionID: "grand-1"},
	}
	for _, ev := range in {
		if err := cs.Send(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(rec.events, in) {
		t.Fatalf("events = %#v\nwant %#v", rec.events, in)
	}
}

func TestChildSink_CloseIsNoop(t *testing.T) {
	rec := &recordingSink{}
	ls := newLockedSink(rec)
	cs := newChildSink(ls, "child-1", "researcher", "tu_parent", "d")
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ls.Close(); err != nil {
		t.Fatal(err)
	}
	if rec.closes != 0 {
		t.Fatalf("parent closes = %d, want 0", rec.closes)
	}
}

func TestChildSink_ParallelSiblingsShareParent(t *testing.T) {
	rec := &recordingSink{} // no lock of its own
	ls := newLockedSink(rec)
	ctx := context.Background()
	const workers, perWorker = 8, 100
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		cs := newChildSink(ls, fmt.Sprintf("child-%d", w), "researcher", fmt.Sprintf("tu_%d", w), "d")
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				_ = cs.Send(ctx, transport.ToolCallStartEvent{ToolCallID: fmt.Sprintf("t%d", i), Name: "x"})
			}
		}()
	}
	// The parent's own step events route through the same lockedSink.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < perWorker; i++ {
			_ = ls.Send(ctx, transport.ToolCallStartEvent{ToolCallID: fmt.Sprintf("p%d", i), Name: "agent"})
		}
	}()
	wg.Wait()
	if got, want := len(rec.events), (workers+1)*perWorker; got != want {
		t.Fatalf("events = %d, want %d", got, want)
	}
}

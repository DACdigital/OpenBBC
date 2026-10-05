package chat

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// closingTools is a fakeTools whose handler implements io.Closer, standing
// in for tools.Composite holding MCP sessions.
type closingTools struct {
	*fakeTools
	closes int
}

func (c *closingTools) Close() error { c.closes++; return nil }

func runnableAgentRepo() *fakeAgentRepo {
	return &fakeAgentRepo{
		version: &types.AgentVersion{ID: "v1", Prompts: []byte(`{"main_prompt":"sys"}`)},
		agent:   &types.Agent{ID: "a1", Architecture: []byte(`{}`)},
	}
}

func textTurnScript() [][]llm.Event {
	return [][]llm.Event{{
		llm.TextDeltaEvent{Delta: "hi"},
		llm.MessageStopEvent{StopReason: "end_turn"},
	}}
}

func skillToolUseRound(id string) []llm.Event {
	return []llm.Event{
		llm.ToolUseStartEvent{ID: id, Name: "Skill"},
		llm.ToolUseInputEvent{ID: id, JSONFragment: `{"name":"x"}`},
		llm.ToolUseEndEvent{ID: id},
		llm.MessageStopEvent{StopReason: "tool_use"},
	}
}

func TestTurn_ReturnsEndTurnStopReason(t *testing.T) {
	o := NewOrchestrator(runnableAgentRepo(), &fakeChatRepo{}, &fakeLLM{script: textTurnScript()}, &fakeBuilder{handler: &fakeTools{}}, slog.Default())
	stop, err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "hi"}}, &recordingSink{}, TurnOpts{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop reason = %q, want end_turn", stop)
	}
}

func TestTurn_ReturnsMaxToolRoundsStopReason(t *testing.T) {
	flm := &fakeLLM{script: [][]llm.Event{skillToolUseRound("t1"), skillToolUseRound("t2")}}
	o := NewOrchestrator(runnableAgentRepo(), &fakeChatRepo{}, flm, &fakeBuilder{handler: &fakeTools{}}, slog.Default())
	o.MaxToolRounds = 1
	stop, err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "hi"}}, &recordingSink{}, TurnOpts{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if stop != "max_tool_rounds" {
		t.Fatalf("stop reason = %q, want max_tool_rounds", stop)
	}
}

func TestTurn_ReturnsEmptyStopReasonOnError(t *testing.T) {
	flm := &fakeLLM{failCall: map[int]error{0: errors.New("llm down")}}
	o := NewOrchestrator(runnableAgentRepo(), &fakeChatRepo{}, flm, &fakeBuilder{handler: &fakeTools{}}, slog.Default())
	stop, err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "hi"}}, &recordingSink{}, TurnOpts{})
	if err == nil {
		t.Fatal("want error")
	}
	if stop != "" {
		t.Fatalf("stop reason = %q, want empty", stop)
	}
}

func TestTurn_NeverClosesSink(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		sink := &recordingSink{}
		o := NewOrchestrator(runnableAgentRepo(), &fakeChatRepo{}, &fakeLLM{script: textTurnScript()}, &fakeBuilder{handler: &fakeTools{}}, slog.Default())
		if _, err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "hi"}}, sink, TurnOpts{}); err != nil {
			t.Fatalf("Turn: %v", err)
		}
		if sink.closes != 0 {
			t.Fatalf("sink closes = %d, want 0", sink.closes)
		}
	})
	t.Run("failure", func(t *testing.T) {
		sink := &recordingSink{}
		flm := &fakeLLM{failCall: map[int]error{0: errors.New("llm down")}}
		o := NewOrchestrator(runnableAgentRepo(), &fakeChatRepo{}, flm, &fakeBuilder{handler: &fakeTools{}}, slog.Default())
		if _, err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "hi"}}, sink, TurnOpts{}); err == nil {
			t.Fatal("want error")
		}
		if sink.closes != 0 {
			t.Fatalf("sink closes = %d, want 0", sink.closes)
		}
	})
}

func TestTurn_ClosesToolHandlerOnce(t *testing.T) {
	cases := []struct {
		name       string
		agents     *fakeAgentRepo
		llm        *fakeLLM
		wantErr    bool
		wantCloses int
	}{
		{"success", runnableAgentRepo(), &fakeLLM{script: textTurnScript()}, false, 1},
		{"success with tool round", runnableAgentRepo(), &fakeLLM{script: append([][]llm.Event{skillToolUseRound("t1")}, textTurnScript()...)}, false, 1},
		{"llm error", runnableAgentRepo(), &fakeLLM{failCall: map[int]error{0: errors.New("llm down")}}, true, 1},
		{
			// The runnable check precedes Build, so no handler exists to close.
			"agent not runnable",
			&fakeAgentRepo{version: &types.AgentVersion{ID: "v1"}, agent: &types.Agent{ID: "a1", Architecture: []byte(`{}`)}},
			&fakeLLM{}, true, 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &closingTools{fakeTools: &fakeTools{}}
			o := NewOrchestrator(tc.agents, &fakeChatRepo{}, tc.llm, &fakeBuilder{handler: h}, slog.Default())
			_, err := o.Turn(context.Background(), "v1", "s1", []llm.Block{llm.TextBlock{Text: "hi"}}, &recordingSink{}, TurnOpts{})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.name == "agent not runnable" && !errors.Is(err, types.ErrAgentNotRunnable) {
				t.Fatalf("err = %v, want ErrAgentNotRunnable", err)
			}
			if h.closes != tc.wantCloses {
				t.Fatalf("handler closes = %d, want %d", h.closes, tc.wantCloses)
			}
		})
	}
}

package chat

import (
	"context"
	"encoding/json"
	"iter"
	"sync"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type fakeAgentRepo struct {
	agent *types.Agent
	err   error
}

func (f *fakeAgentRepo) GetByID(ctx context.Context, id string) (*types.Agent, error) {
	return f.agent, f.err
}

type fakeChatRepo struct {
	mu       sync.Mutex
	ensured  map[string]string
	messages []types.ChatMessage
	nextSeq  int
}

func (f *fakeChatRepo) EnsureSession(ctx context.Context, sessionID, agentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ensured == nil {
		f.ensured = map[string]string{}
	}
	if cur, ok := f.ensured[sessionID]; ok && cur != agentID {
		return types.ErrSessionAgentMismatch
	}
	f.ensured[sessionID] = agentID
	return nil
}

func (f *fakeChatRepo) LoadMessages(ctx context.Context, sessionID string) ([]*types.ChatMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*types.ChatMessage
	for i := range f.messages {
		m := f.messages[i]
		if m.SessionID == sessionID {
			out = append(out, &m)
		}
	}
	return out, nil
}

func (f *fakeChatRepo) AppendMessages(ctx context.Context, agentVersionID string, msgs []types.ChatMessage) error {
	_ = agentVersionID
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, msgs...)
	return nil
}

func (f *fakeChatRepo) NextSeq(ctx context.Context, sessionID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextSeq++
	return f.nextSeq, nil
}

// fakeLLM emits a scripted event sequence. Each call to Generate consumes
// the next slice from `script` and yields its events.
type fakeLLM struct {
	name   string
	script [][]llm.Event
	calls  int
}

func (f *fakeLLM) Name() string { return f.name }

func (f *fakeLLM) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		if f.calls >= len(f.script) {
			return
		}
		events := f.script[f.calls]
		f.calls++
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// fakeTools returns one canned tool def and logs all calls.
type fakeTools struct {
	callLog []tools.Call
	results []tools.Result
}

func (f *fakeTools) Tools(bundle json.RawMessage) ([]llm.ToolDef, error) {
	return []llm.ToolDef{
		{Name: "Skill", Description: "x", InputSchema: []byte(`{"type":"object"}`)},
	}, nil
}

func (f *fakeTools) Call(ctx context.Context, bundle json.RawMessage, c tools.Call) (tools.Result, error) {
	f.callLog = append(f.callLog, c)
	if len(f.results) == 0 {
		return tools.Result{ToolUseID: c.ID, Output: []byte(`{}`)}, nil
	}
	res := f.results[0]
	f.results = f.results[1:]
	return res, nil
}

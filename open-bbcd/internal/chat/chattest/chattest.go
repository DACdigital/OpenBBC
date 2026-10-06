// Package chattest holds test-only helpers shared by the chat and handler
// test suites: a scripted, concurrency-safe llm.LLM keyed by system prompt
// and step builders for it. It is not used by production code.
package chattest

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
)

// Step produces the events of one LLM round for a request.
type Step func(req llm.Request) []llm.Event

// Route scripts one system prompt (one agent version). The step used is
// picked by how many assistant messages the request already holds (i.e. per
// session); past the end the last step repeats.
type Route struct {
	Steps  []Step
	Delay  time.Duration // honours ctx
	FailAt map[int]error // assistant-count index → error
}

// RoutedLLM is a concurrency-safe scripted llm.LLM keyed by req.System.
type RoutedLLM struct {
	mu          sync.Mutex
	routes      map[string]*Route
	requests    map[string][]llm.Request
	inFlight    map[string]int
	maxInFlight map[string]int
	// OnCall runs at the start of every Generate (outside the lock). Set it
	// before the first Generate.
	OnCall func(system string)
}

func NewRoutedLLM() *RoutedLLM {
	return &RoutedLLM{
		routes:      map[string]*Route{},
		requests:    map[string][]llm.Request{},
		inFlight:    map[string]int{},
		maxInFlight: map[string]int{},
	}
}

// Route registers the steps for a system prompt and returns the route so
// callers can set Delay or FailAt (before the first Generate).
func (f *RoutedLLM) Route(system string, steps ...Step) *Route {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := &Route{Steps: steps}
	f.routes[system] = r
	return r
}

// Requests returns every request received for a system prompt, in order.
func (f *RoutedLLM) Requests(system string) []llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.Request(nil), f.requests[system]...)
}

// MaxInFlight is the peak number of concurrent Generate calls for a system
// prompt.
func (f *RoutedLLM) MaxInFlight(system string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxInFlight[system]
}

func (f *RoutedLLM) Name() string { return "routed" }

func (f *RoutedLLM) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		if f.OnCall != nil {
			f.OnCall(req.System)
		}
		f.mu.Lock()
		f.requests[req.System] = append(f.requests[req.System], req)
		r := f.routes[req.System]
		f.inFlight[req.System]++
		if f.inFlight[req.System] > f.maxInFlight[req.System] {
			f.maxInFlight[req.System] = f.inFlight[req.System]
		}
		f.mu.Unlock()
		defer func() {
			f.mu.Lock()
			f.inFlight[req.System]--
			f.mu.Unlock()
		}()
		if r == nil {
			yield(nil, fmt.Errorf("chattest: no route for system %q", req.System))
			return
		}
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		if r.Delay > 0 {
			t := time.NewTimer(r.Delay)
			select {
			case <-ctx.Done():
				t.Stop()
				yield(nil, ctx.Err())
				return
			case <-t.C:
			}
		}
		n := 0
		for _, m := range req.Messages {
			if m.Role == llm.RoleAssistant {
				n++
			}
		}
		if err, ok := r.FailAt[n]; ok {
			yield(nil, err)
			return
		}
		if len(r.Steps) == 0 {
			return
		}
		i := n
		if i >= len(r.Steps) {
			i = len(r.Steps) - 1
		}
		for _, ev := range r.Steps[i](req) {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// TextStep ends the turn with text.
func TextStep(text string) Step {
	return func(llm.Request) []llm.Event {
		return []llm.Event{llm.TextDeltaEvent{Delta: text}, llm.MessageStopEvent{StopReason: "end_turn"}}
	}
}

// EchoStep ends the turn with prefix + the request's first user text.
func EchoStep(prefix string) Step {
	return func(req llm.Request) []llm.Event {
		return []llm.Event{llm.TextDeltaEvent{Delta: prefix + FirstUserText(req)}, llm.MessageStopEvent{StopReason: "end_turn"}}
	}
}

// FirstUserText is the first text block of the request's first user message.
func FirstUserText(req llm.Request) string {
	for _, m := range req.Messages {
		if m.Role != llm.RoleUser {
			continue
		}
		for _, b := range m.Content {
			if tb, ok := b.(llm.TextBlock); ok {
				return tb.Text
			}
		}
	}
	return ""
}

// ToolCall is one scripted tool_use: a tool name and its raw input JSON
// (which may be deliberately invalid).
type ToolCall struct{ Name, Input string }

// AgentCall is a well-formed call of the built-in agent tool.
func AgentCall(subagent, description, prompt string) ToolCall {
	in, _ := json.Marshal(map[string]string{"subagent": subagent, "description": description, "prompt": prompt})
	return ToolCall{Name: tools.AgentToolName, Input: string(in)}
}

// CallsStep emits optional text then one tool_use per call, each with a
// fresh "tu_<uuid>" id, and stops with tool_use.
func CallsStep(text string, calls ...ToolCall) Step {
	return func(llm.Request) []llm.Event {
		var evs []llm.Event
		if text != "" {
			evs = append(evs, llm.TextDeltaEvent{Delta: text})
		}
		for _, c := range calls {
			id := "tu_" + uuid.NewString()
			evs = append(evs,
				llm.ToolUseStartEvent{ID: id, Name: c.Name},
				llm.ToolUseInputEvent{ID: id, JSONFragment: c.Input},
				llm.ToolUseEndEvent{ID: id},
			)
		}
		return append(evs, llm.MessageStopEvent{StopReason: "tool_use"})
	}
}

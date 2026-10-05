package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// lockedSink serialises Send on a parent sink so that it can be shared by
// every sibling childSink of one tool step and by the parent's own events
// for that step. Close is a no-op: the handler that built the real sink owns
// its close.
type lockedSink struct {
	mu sync.Mutex
	s  transport.Sink
}

func newLockedSink(s transport.Sink) *lockedSink { return &lockedSink{s: s} }

func (l *lockedSink) Send(ctx context.Context, ev transport.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s.Send(ctx, ev)
}

func (l *lockedSink) Close() error { return nil }

// childSink adapts one child turn's events onto its parent's stream (spec §
// AG-UI stream, childSink rules 1-5). Rule 5 (serialised Send) is provided
// by the shared lockedSink parent.
type childSink struct {
	parent         *lockedSink // shared by sibling children and the parent step
	childSessionID string
	stepName       string // binding name
	wireToolCallID string // parent's agent call id as the client sees it
	description    string
}

func newChildSink(parent *lockedSink, childSessionID, stepName, wireToolCallID, description string) *childSink {
	return &childSink{
		parent:         parent,
		childSessionID: childSessionID,
		stepName:       stepName,
		wireToolCallID: wireToolCallID,
		description:    description,
	}
}

// start announces the child run on the parent stream (rule 1).
func (s *childSink) start(ctx context.Context) error {
	return s.parent.Send(ctx, transport.StepStartedEvent{
		StepName:       s.stepName,
		ToolCallID:     s.wireToolCallID,
		ChildSessionID: s.childSessionID,
		Description:    s.description,
	})
}

// finish closes the step opened by start (rule 1).
func (s *childSink) finish(ctx context.Context, isError bool) error {
	return s.parent.Send(ctx, transport.StepFinishedEvent{
		StepName:       s.stepName,
		ToolCallID:     s.wireToolCallID,
		ChildSessionID: s.childSessionID,
		IsError:        isError,
	})
}

// Send tags the child's own tool events with its session id (rule 2),
// forwards events from deeper levels unchanged (rule 3) and drops the
// child's own framing, text, artifact and error events (rule 2).
func (s *childSink) Send(ctx context.Context, ev transport.Event) error {
	switch e := ev.(type) {
	case transport.ToolCallStartEvent:
		if e.ChildSessionID == "" {
			e.ChildSessionID = s.childSessionID
		}
		return s.parent.Send(ctx, e)
	case transport.ToolCallArgsEvent:
		if e.ChildSessionID == "" {
			e.ChildSessionID = s.childSessionID
		}
		return s.parent.Send(ctx, e)
	case transport.ToolCallEndEvent:
		if e.ChildSessionID == "" {
			e.ChildSessionID = s.childSessionID
		}
		return s.parent.Send(ctx, e)
	case transport.ToolResultEvent:
		if e.ChildSessionID == "" {
			e.ChildSessionID = s.childSessionID
		}
		return s.parent.Send(ctx, e)
	case transport.StepStartedEvent, transport.StepFinishedEvent:
		// Only a grandchild's childSink emits these, so they always carry
		// the deeper child's id already.
		return s.parent.Send(ctx, ev)
	default:
		// SessionStart, Text*, ArtifactRef, TurnEnd, Error: dropped. A child
		// error surfaces as the agent tool's error result instead.
		return nil
	}
}

// Close is a no-op (rule 4): the child turn never closes its parent's stream.
func (s *childSink) Close() error { return nil }

// subAgentRunner runs one agent call as a child turn. Internal to package
// chat; an interface only so tests can fake it.
type subAgentRunner interface {
	Run(ctx context.Context, req subAgentRequest) (subAgentResult, error)
}

type subAgentRequest struct {
	ParentSessionID  string
	ParentToolCallID string // raw tool_use id, as persisted
	RootSessionID    string
	ParentDepth      int
	Binding          types.SubAgentBinding
	Description      string
	Prompt           string
	ParentSink       transport.Sink // the step's shared lockedSink
}

type subAgentResult struct {
	ChildSessionID string
	Text           string
	StopReason     string // provider stop reason or "max_tool_rounds"
}

// turnRunner is the production subAgentRunner: it re-enters
// Orchestrator.Turn in-process for the pinned target version.
type turnRunner struct{ o *Orchestrator }

// Run implements spec § subAgentRunner.Run: depth check, CreateChildSession,
// child Turn under a childSink, result from the child's last assistant
// message. Every failure is an *agentToolError.
func (r *turnRunner) Run(ctx context.Context, req subAgentRequest) (subAgentResult, error) {
	if req.ParentDepth >= r.o.MaxDepth {
		return subAgentResult{}, &agentToolError{Code: "max_depth_exceeded", Details: fmt.Sprintf("sub-agent depth limit %d reached", r.o.MaxDepth)}
	}
	childID, err := r.o.chats.CreateChildSession(ctx, req.RootSessionID, req.ParentSessionID, req.ParentToolCallID, req.Binding.TargetVersionID)
	if err != nil {
		return subAgentResult{}, r.spawnError(ctx, req, err)
	}

	parent, ok := req.ParentSink.(*lockedSink)
	if !ok {
		parent = newLockedSink(req.ParentSink)
	}
	// The wire id of the parent's agent call: a root's own tool calls are
	// unprefixed on the stream, a child's carry its session id.
	wireID := req.ParentToolCallID
	if req.ParentDepth > 0 {
		wireID = req.ParentSessionID + ":" + req.ParentToolCallID
	}
	sink := newChildSink(parent, childID, req.Binding.Name, wireID, req.Description)
	_ = sink.start(ctx)

	stop, turnErr := r.o.Turn(ctx, req.Binding.TargetVersionID, childID, []llm.Block{llm.TextBlock{Text: req.Prompt}}, sink, TurnOpts{
		Depth:            req.ParentDepth + 1,
		ParentSessionID:  req.ParentSessionID,
		ParentToolCallID: req.ParentToolCallID,
		RootSessionID:    req.RootSessionID,
	})
	res := subAgentResult{ChildSessionID: childID, StopReason: stop}
	if turnErr == nil {
		res.Text, turnErr = r.o.lastAssistantText(ctx, childID)
	}
	err = classifyChildError(ctx, turnErr, res)
	_ = sink.finish(ctx, err != nil)
	return res, err
}

// spawnError maps a CreateChildSession failure (no child exists).
func (r *turnRunner) spawnError(ctx context.Context, req subAgentRequest, err error) error {
	if ctx.Err() != nil {
		return &agentToolError{Code: "cancelled", Details: ctx.Err().Error()}
	}
	if errors.Is(err, types.ErrSessionLocked) {
		// Cause (a): the BO root was locked before the child was created.
		return &agentToolError{Code: "session_locked", Details: err.Error()}
	}
	attrs := []any{
		slog.String("parent_session_id", req.ParentSessionID),
		slog.String("parent_tool_call_id", req.ParentToolCallID),
		slog.String("root_session_id", req.RootSessionID),
		slog.String("target_version_id", req.Binding.TargetVersionID),
		slog.Any("err", err),
	}
	if errors.Is(err, types.ErrNotFound) {
		// The parent or root was deleted while the turn was running.
		r.o.logger.Warn("sub-agent spawn failed", attrs...)
	} else {
		r.o.logger.Error("sub-agent spawn failed", attrs...)
	}
	return &agentToolError{Code: "subagent_failed", Details: err.Error()}
}

// classifyChildError maps the child turn's outcome to an agent tool error,
// or nil on success. Only "max_tool_rounds" among stop reasons is an error;
// an empty stop reason (stream ended without a stop event) is success.
func classifyChildError(ctx context.Context, turnErr error, res subAgentResult) error {
	if ctx.Err() != nil {
		return &agentToolError{Code: "cancelled", Details: ctx.Err().Error()}
	}
	if turnErr != nil {
		if errors.Is(turnErr, types.ErrSessionLocked) {
			// Cause (b): close-draft locked the tree after the child was
			// created; the empty locked child row remains.
			return &agentToolError{Code: "session_locked", Details: turnErr.Error()}
		}
		return &agentToolError{Code: "subagent_failed", Details: turnErr.Error()}
	}
	if res.StopReason == "max_tool_rounds" {
		return &agentToolError{Code: "subagent_max_tool_rounds", Details: res.Text}
	}
	return nil
}

// lastAssistantText concatenates (no separator) the text blocks of the
// session's last persisted assistant message.
func (o *Orchestrator) lastAssistantText(ctx context.Context, sessionID string) (string, error) {
	msgs, err := o.chats.LoadMessages(ctx, sessionID)
	if err != nil {
		return "", err
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != types.ChatRoleAssistant {
			continue
		}
		var raw []json.RawMessage
		if err := json.Unmarshal(msgs[i].Content, &raw); err != nil {
			return "", err
		}
		var sb strings.Builder
		for _, b := range parseBlocks(raw) {
			if tb, ok := b.(llm.TextBlock); ok {
				sb.WriteString(tb.Text)
			}
		}
		return sb.String(), nil
	}
	return "", nil
}

// runAgentCalls runs the step's agent calls (pendingToolUses[agentIdx...])
// concurrently, at most MaxParallel at a time, writing each result into its
// tool_use slot and sending its ToolResultEvent when it finishes. It returns
// once every call has finished, so no goroutine outlives the step.
func (o *Orchestrator) runAgentCalls(
	ctx context.Context,
	pending []llm.ToolUseBlock,
	agentIdx []int,
	results []llm.ToolResultBlock,
	bindings map[string]types.SubAgentBinding,
	sessionID string,
	opts TurnOpts,
	shared *lockedSink,
) {
	parallel := o.MaxParallel
	if parallel < 1 {
		parallel = 1
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, i := range agentIdx {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tu := pending[i]
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				results[i] = o.runAgentCall(ctx, tu, bindings, sessionID, opts, shared)
			case <-ctx.Done():
				results[i] = agentErrorResult(tu.ID, &agentToolError{Code: "cancelled", Details: ctx.Err().Error()})
			}
			_ = shared.Send(ctx, transport.ToolResultEvent{ToolCallID: tu.ID, Result: results[i].Result, IsError: results[i].IsError})
		}(i)
	}
	wg.Wait()
}

// runAgentCall validates one agent call and runs it through o.runner. The
// success result is the child's final text as a JSON string; it never
// carries an artifact_ref.
func (o *Orchestrator) runAgentCall(
	ctx context.Context,
	tu llm.ToolUseBlock,
	bindings map[string]types.SubAgentBinding,
	sessionID string,
	opts TurnOpts,
	shared *lockedSink,
) llm.ToolResultBlock {
	in, err := parseAgentInput(tu.Input)
	if err != nil {
		return agentErrorResult(tu.ID, err)
	}
	b, ok := bindings[in.Subagent]
	if !ok {
		return agentErrorResult(tu.ID, &agentToolError{Code: "unknown_subagent", Details: fmt.Sprintf("no sub-agent named %q", in.Subagent)})
	}
	res, err := o.runner.Run(ctx, subAgentRequest{
		ParentSessionID:  sessionID,
		ParentToolCallID: tu.ID,
		RootSessionID:    opts.RootSessionID,
		ParentDepth:      opts.Depth,
		Binding:          b,
		Description:      in.Description,
		Prompt:           in.Prompt,
		ParentSink:       shared,
	})
	if err != nil {
		var ae *agentToolError
		if !errors.As(err, &ae) {
			ae = &agentToolError{Code: "subagent_failed", Details: err.Error()}
		}
		return agentErrorResult(tu.ID, ae)
	}
	text, _ := json.Marshal(res.Text)
	return llm.ToolResultBlock{ToolUseID: tu.ID, Result: text}
}

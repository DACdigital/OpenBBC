package chat

import (
	"context"
	"sync"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
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

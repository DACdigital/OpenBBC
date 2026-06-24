// Package chat provides the per-turn orchestrator for the run-agent
// feature. Stateless: each Turn call loads bundle + history, drives one
// LLM round (text streaming + tool-use loop in B21), persists user +
// assistant messages, streams events through the transport.
package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

// ToolHandlerBuilder constructs the Composite handler for one chat session.
// The bundle is passed in raw so the builder can derive endpoint definitions
// without a separate bundle-parse step in the orchestrator.
type ToolHandlerBuilder interface {
	Build(ctx context.Context, versionID string, bundle json.RawMessage) (tools.Handler, error)
}

// AgentReader is the narrow agent-side interface the orchestrator needs.
// The orchestrator is version-centric: it loads a version row (which carries
// the bundle) by its id. In BO chat the id is a chat_sessions.agent_version_id;
// in deployed runtime it's resolved via DeployedRepository before Turn.
type AgentReader interface {
	GetByID(ctx context.Context, versionID string) (*types.AgentVersion, error)
}

// ChatStore is the narrow chat-repo interface the orchestrator needs.
//
// scopeID is the per-impl ownership scope passed through from Turn's agentID
// parameter: BO chat's ChatRepository treats it as the version row id
// (chat_sessions.agent_version_id), while DeployedChatStore treats it as the
// per-agent id (deployed_sessions.agent_id). The orchestrator itself doesn't
// interpret the value — it just forwards it.
type ChatStore interface {
	EnsureSession(ctx context.Context, sessionID, scopeID string) error
	LoadMessages(ctx context.Context, sessionID string) ([]*types.ChatMessage, error)
	AppendMessages(ctx context.Context, agentVersionID string, msgs []types.ChatMessage) error
	NextSeq(ctx context.Context, sessionID string) (int, error)
}

type Orchestrator struct {
	agents  AgentReader
	chats   ChatStore
	llm     llm.LLM
	builder ToolHandlerBuilder
	logger  *slog.Logger

	// Tunables; set by NewAPI from config. Sensible defaults baked in.
	Model         string
	MaxTokens     int
	MaxToolRounds int
}

func NewOrchestrator(agents AgentReader, chats ChatStore, l llm.LLM, b ToolHandlerBuilder, logger *slog.Logger) *Orchestrator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Orchestrator{
		agents:        agents,
		chats:         chats,
		llm:           l,
		builder:       b,
		logger:        logger,
		Model:         "claude-sonnet-4-6",
		MaxTokens:     4096,
		MaxToolRounds: 10,
	}
}

// Turn runs one chat turn end-to-end. Caller owns the Sink + HTTP
// connection. Stream-level errors are emitted as ErrorEvent and don't
// abort the function; only unrecoverable errors (bundle missing, session
// mismatch, persistence failures) return non-nil error.
//
// The inner loop runs the LLM, executes any tool calls it emits, and
// re-runs the LLM with the results appended — up to MaxToolRounds times.
// On cap, stopReason is set to "max_tool_rounds" and the turn ends cleanly.
func (o *Orchestrator) Turn(
	ctx context.Context,
	agentID, sessionID string,
	userInput []llm.Block,
	sink transport.Sink,
) error {
	// failTurn logs the error with context and emits a RUN_ERROR event via
	// the sink so the chat UI sees the message in-band. Returns err so
	// callers can `return failTurn(...)`.
	failTurn := func(code, stage string, err error) error {
		o.logger.Error("chat turn failed",
			slog.String("agent_id", agentID),
			slog.String("session_id", sessionID),
			slog.String("stage", stage),
			slog.String("code", code),
			slog.Any("err", err),
		)
		_ = sink.Send(ctx, transport.ErrorEvent{Code: code, Message: err.Error()})
		return err
	}

	// 1. Load version + verify bundle exists. `agentID` is the orchestrator's
	// scope identifier — in BO chat it's the version row id, in deployed
	// runtime it's been resolved to a version id before Turn is invoked.
	version, err := o.agents.GetByID(ctx, agentID)
	if err != nil {
		return failTurn("agent_load", "load_agent", err)
	}
	if len(version.Bundle) == 0 {
		return failTurn("agent_not_runnable", "verify_bundle", types.ErrAgentNotRunnable)
	}

	// 2. Ensure session row exists (lazy-create). The ChatStore impl decides
	// how to interpret the second arg (version-id for BO chat,
	// per-agent-id for deployed runtime).
	if err := o.chats.EnsureSession(ctx, sessionID, agentID); err != nil {
		return failTurn("session_error", "ensure_session", err)
	}

	// 3. Load history.
	history, err := o.chats.LoadMessages(ctx, sessionID)
	if err != nil {
		return failTurn("history_load", "load_messages", err)
	}

	// 4. Build LLM request.
	var bundleHead struct {
		MainPrompt string `json:"main_prompt"`
	}
	if err := json.Unmarshal(version.Bundle, &bundleHead); err != nil {
		return failTurn("bundle_parse", "parse_bundle", err)
	}

	toolHandler, err := o.builder.Build(ctx, version.ID, version.Bundle)
	if err != nil {
		return failTurn("tool_handler_init", "build_tool_handler", err)
	}

	toolDefs, err := toolHandler.Tools(version.Bundle)
	if err != nil {
		return failTurn("tools_init", "build_tool_defs", err)
	}

	msgs := historyToLLM(history)
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: userInput})

	// 5. Persist the user message NOW (before the LLM call). A failed
	// turn still captures what the user asked.
	userMsgID := uuid.NewString()
	userContent, err := blocksToJSON(userInput)
	if err != nil {
		return failTurn("encode_user_msg", "serialize_user_blocks", err)
	}
	userSeq, err := o.chats.NextSeq(ctx, sessionID)
	if err != nil {
		return failTurn("seq_assign", "next_seq_user", err)
	}
	if err := o.chats.AppendMessages(ctx, version.ID, []types.ChatMessage{{
		ID:        userMsgID,
		SessionID: sessionID,
		Role:      types.ChatRoleUser,
		Content:   userContent,
		Seq:       userSeq,
	}}); err != nil {
		return failTurn("persist_user_msg", "append_user_msg", err)
	}

	// 6. Send session-start event.
	_ = sink.Send(ctx, transport.SessionStartEvent{SessionID: sessionID, AgentID: agentID})

	// 7. Tool-use loop. Each iteration drives one LLM round, persists the
	// assistant message, optionally executes tools + persists the tool message,
	// then loops. Exits when stop_reason != "tool_use" or MaxToolRounds is hit.
	req := llm.Request{
		Model:     o.Model,
		System:    bundleHead.MainPrompt,
		Messages:  msgs,
		Tools:     toolDefs,
		MaxTokens: o.MaxTokens,
	}

	var stopReason string
	var usageIn, usageOut int
	toolRounds := 0

	for {
		var (
			assistantBlocks     []llm.Block
			pendingToolUses     []llm.ToolUseBlock
			inputBuffers        = map[string]*bytes.Buffer{}
			stopReasonThisRound string
		)

		assistantMsgID := uuid.NewString()
		_ = sink.Send(ctx, transport.TextStartEvent{MessageID: assistantMsgID})

		for ev, err := range o.llm.Generate(ctx, req) {
			if err != nil {
				return failTurn("llm_error", "llm_generate", err)
			}
			switch e := ev.(type) {
			case llm.TextDeltaEvent:
				_ = sink.Send(ctx, transport.TextDeltaEvent{MessageID: assistantMsgID, Delta: e.Delta})
				// Accumulate into the last TextBlock (or open a new one).
				n := len(assistantBlocks)
				if n > 0 {
					if tb, ok := assistantBlocks[n-1].(llm.TextBlock); ok {
						assistantBlocks[n-1] = llm.TextBlock{Text: tb.Text + e.Delta}
						continue
					}
				}
				assistantBlocks = append(assistantBlocks, llm.TextBlock{Text: e.Delta})

			case llm.ToolUseStartEvent:
				_ = sink.Send(ctx, transport.ToolCallStartEvent{ToolCallID: e.ID, Name: e.Name})
				assistantBlocks = append(assistantBlocks, llm.ToolUseBlock{ID: e.ID, Name: e.Name})
				inputBuffers[e.ID] = &bytes.Buffer{}

			case llm.ToolUseInputEvent:
				_ = sink.Send(ctx, transport.ToolCallArgsEvent{ToolCallID: e.ID, ArgsJSON: e.JSONFragment})
				if buf, ok := inputBuffers[e.ID]; ok {
					buf.WriteString(e.JSONFragment)
				}

			case llm.ToolUseEndEvent:
				_ = sink.Send(ctx, transport.ToolCallEndEvent{ToolCallID: e.ID})
				// Finalize the ToolUseBlock with accumulated input bytes.
				if buf, ok := inputBuffers[e.ID]; ok {
					inputBytes := buf.Bytes()
					for i, b := range assistantBlocks {
						if tu, ok := b.(llm.ToolUseBlock); ok && tu.ID == e.ID {
							tu.Input = inputBytes
							assistantBlocks[i] = tu
							pendingToolUses = append(pendingToolUses, tu)
							break
						}
					}
				}

			case llm.MessageStopEvent:
				stopReasonThisRound = e.StopReason

			case llm.UsageEvent:
				if e.InputTokens > 0 {
					usageIn = e.InputTokens
				}
				if e.OutputTokens > 0 {
					usageOut = e.OutputTokens
				}
			}
		}

		_ = sink.Send(ctx, transport.TextEndEvent{MessageID: assistantMsgID})

		// Persist assistant message for this round.
		assistantContent, err := blocksToJSON(assistantBlocks)
		if err != nil {
			return failTurn("encode_assistant_msg", "serialize_assistant_blocks", err)
		}
		assistantSeq, err := o.chats.NextSeq(ctx, sessionID)
		if err != nil {
			return failTurn("seq_assign", "next_seq_assistant", err)
		}
		if err := o.chats.AppendMessages(ctx, version.ID, []types.ChatMessage{{
			ID:        assistantMsgID,
			SessionID: sessionID,
			Role:      types.ChatRoleAssistant,
			Content:   assistantContent,
			Seq:       assistantSeq,
		}}); err != nil {
			return failTurn("persist_assistant_msg", "append_assistant_msg", err)
		}

		stopReason = stopReasonThisRound

		// Loop exit conditions.
		if stopReason != "tool_use" {
			break
		}
		if toolRounds >= o.MaxToolRounds {
			stopReason = "max_tool_rounds"
			break
		}

		// Execute the pending tools and build a tool-role message.
		toolBlocks := make([]llm.Block, 0, len(pendingToolUses))
		for _, tu := range pendingToolUses {
			res, err := toolHandler.Call(ctx, version.Bundle, tools.Call{
				ID:    tu.ID,
				Name:  tu.Name,
				Input: tu.Input,
			})
			if err != nil {
				// Wrap as IsError=true result so the model can recover or surface.
				errMsg, _ := json.Marshal(map[string]string{"error": err.Error()})
				res = tools.Result{ToolUseID: tu.ID, Output: errMsg, IsError: true}
			}
			_ = sink.Send(ctx, transport.ToolResultEvent{
				ToolCallID: tu.ID,
				Result:     res.Output,
				IsError:    res.IsError,
			})
			toolBlocks = append(toolBlocks, llm.ToolResultBlock{
				ToolUseID: tu.ID,
				Result:    res.Output,
				IsError:   res.IsError,
			})
		}

		// Persist the tool-role message.
		toolMsgID := uuid.NewString()
		toolContent, err := blocksToJSON(toolBlocks)
		if err != nil {
			return failTurn("encode_tool_msg", "serialize_tool_blocks", err)
		}
		toolSeq, err := o.chats.NextSeq(ctx, sessionID)
		if err != nil {
			return failTurn("seq_assign", "next_seq_tool", err)
		}
		if err := o.chats.AppendMessages(ctx, version.ID, []types.ChatMessage{{
			ID:        toolMsgID,
			SessionID: sessionID,
			Role:      types.ChatRoleTool,
			Content:   toolContent,
			Seq:       toolSeq,
		}}); err != nil {
			return failTurn("persist_tool_msg", "append_tool_msg", err)
		}

		// Extend the LLM request with both messages and loop.
		req.Messages = append(req.Messages,
			llm.Message{Role: llm.RoleAssistant, Content: assistantBlocks},
			llm.Message{Role: llm.RoleTool, Content: toolBlocks},
		)
		toolRounds++
	}

	// 8. Turn-end + close sink.
	_ = sink.Send(ctx, transport.TurnEndEvent{
		StopReason: stopReason,
		UsageIn:    usageIn,
		UsageOut:   usageOut,
	})
	_ = sink.Close()

	o.logger.Info("turn completed",
		slog.String("agent_id", agentID),
		slog.String("session_id", sessionID),
		slog.String("stop_reason", stopReason),
		slog.Int("tokens_in", usageIn),
		slog.Int("tokens_out", usageOut),
	)
	return nil
}

// historyToLLM converts persisted ChatMessage rows to llm.Message values.
// Content is a JSONB array of content blocks (matches Anthropic's shape);
// parse each block by its "type" field.
func historyToLLM(rows []*types.ChatMessage) []llm.Message {
	out := make([]llm.Message, 0, len(rows))
	for _, m := range rows {
		var rawBlocks []json.RawMessage
		if err := json.Unmarshal(m.Content, &rawBlocks); err != nil {
			// Skip malformed rows rather than fail the whole turn.
			continue
		}
		out = append(out, llm.Message{
			Role:    llm.Role(m.Role),
			Content: parseBlocks(rawBlocks),
		})
	}
	return out
}

func parseBlocks(raw []json.RawMessage) []llm.Block {
	out := make([]llm.Block, 0, len(raw))
	for _, r := range raw {
		var head struct{ Type string `json:"type"` }
		_ = json.Unmarshal(r, &head)
		switch head.Type {
		case "text":
			var b struct{ Text string `json:"text"` }
			_ = json.Unmarshal(r, &b)
			out = append(out, llm.TextBlock{Text: b.Text})
		case "tool_use":
			var b struct {
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(r, &b)
			out = append(out, llm.ToolUseBlock{ID: b.ID, Name: b.Name, Input: b.Input})
		case "tool_result":
			var b struct {
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
				IsError   bool            `json:"is_error"`
			}
			_ = json.Unmarshal(r, &b)
			out = append(out, llm.ToolResultBlock{ToolUseID: b.ToolUseID, Result: b.Content, IsError: b.IsError})
		}
	}
	return out
}

func blocksToJSON(blocks []llm.Block) (json.RawMessage, error) {
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		switch x := b.(type) {
		case llm.TextBlock:
			out = append(out, map[string]any{"type": "text", "text": x.Text})
		case llm.ToolUseBlock:
			out = append(out, map[string]any{
				"type":  "tool_use",
				"id":    x.ID,
				"name":  x.Name,
				"input": json.RawMessage(x.Input),
			})
		case llm.ToolResultBlock:
			out = append(out, map[string]any{
				"type":        "tool_result",
				"tool_use_id": x.ToolUseID,
				"content":     json.RawMessage(x.Result),
				"is_error":    x.IsError,
			})
		}
	}
	return json.Marshal(out)
}

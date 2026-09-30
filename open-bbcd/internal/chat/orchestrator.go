// Package chat provides the per-turn orchestrator for the run-agent
// feature. Stateless: each Turn call loads bundle + history, drives one
// LLM round (text streaming + tool-use loop in B21), persists user +
// assistant messages, streams events through the transport.
package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

// ToolHandlerBuilder constructs the Composite handler for one chat session.
// agentID resolves the (now agent-level) endpoint→backend wiring; versionID
// resolves the MCP attachments; architecture is the frozen agent-level
// architecture blob (endpoints + metadata).
type ToolHandlerBuilder interface {
	Build(ctx context.Context, agentID, versionID string, architecture json.RawMessage) (tools.Handler, error)
}

// AgentReader is the narrow agent-side interface the orchestrator needs.
// Post-017, the orchestrator must load both the version (for prompts) and
// its owning agent (for the architecture blob). One trip via GetWithAgent.
type AgentReader interface {
	GetWithAgent(ctx context.Context, versionID string) (*types.AgentVersion, *types.Agent, error)
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

// ArtifactFetcherResolver resolves an llm.ArtifactFetcher for a given
// store_id. Returning nil means "no such store" — the orchestrator
// substitutes the text surrogate in place of the artifact_ref block.
//
// Optional dependency; when the resolver is nil (chat-artifacts feature
// disabled), every ArtifactRefBlock in flight is dropped to the text
// surrogate. This keeps the code path compiled even in artifact-free
// deployments.
type ArtifactFetcherResolver func(storeID string) llm.ArtifactFetcher

// ErrInlineMediaNotPersistable is returned by blocksToJSON when a message
// carries renderer-produced bytes. Rendered media exists only inside one
// LLM request; persisting it would put artifact bytes in Postgres.
var ErrInlineMediaNotPersistable = errors.New("chat: inline media blocks must never be persisted")

type Orchestrator struct {
	agents            AgentReader
	chats             ChatStore
	llm               llm.LLM
	builder           ToolHandlerBuilder
	logger            *slog.Logger
	artifactResolver  ArtifactFetcherResolver
	artifactUploader  ArtifactUploader

	// Tunables; set by NewAPI from config. Sensible defaults baked in.
	Model         string
	MaxTokens     int
	MaxToolRounds int
}

// WithArtifacts wires a resolver for looking up ArtifactFetchers by
// store_id. Returns the same orchestrator so callers can chain the call
// during construction. Passing nil clears any previously-set resolver.
func (o *Orchestrator) WithArtifacts(resolver ArtifactFetcherResolver) *Orchestrator {
	o.artifactResolver = resolver
	return o
}

// ArtifactUploader is the narrow slice of artifacts machinery the
// orchestrator needs to normalise MCP tool results carrying inline
// bytes (ImageContent / EmbeddedResource with `blob` or `text`) into
// ArtifactRefBlocks. Bytes flow to the default store; ref metadata
// (store_id, uri, sha256, mime, size) comes back for embedding on the
// tool-role message.
//
// Callers pass a nil uploader to keep tool-result normalisation off
// entirely (matches the artifact-feature-disabled default).
type ArtifactUploader interface {
	Upload(ctx context.Context, mime string, bytes []byte) (llm.ArtifactRefBlock, error)
}

// WithArtifactUploader wires the uploader used to normalise MCP tool
// result payloads. Returns the same orchestrator for chaining.
func (o *Orchestrator) WithArtifactUploader(uploader ArtifactUploader) *Orchestrator {
	o.artifactUploader = uploader
	return o
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

	// 1. Load version + owning agent. `agentID` is the orchestrator's scope
	// identifier — in BO chat it's the version row id (used to fetch both
	// rows), in deployed runtime it's been resolved to a version id before
	// Turn is invoked. The version carries prompts, the agent carries the
	// frozen architecture (endpoints + metadata).
	version, agent, err := o.agents.GetWithAgent(ctx, agentID)
	if err != nil {
		return failTurn("agent_load", "load_agent", err)
	}
	if len(agent.Architecture) == 0 || len(version.Prompts) == 0 {
		return failTurn("agent_not_runnable", "verify_finalized", types.ErrAgentNotRunnable)
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

	// 4. Build LLM request. Main prompt lives on the version's prompts blob;
	// architecture (endpoints + metadata) lives on the agent.
	var promptsHead struct {
		MainPrompt string `json:"main_prompt"`
	}
	if err := json.Unmarshal(version.Prompts, &promptsHead); err != nil {
		return failTurn("prompts_parse", "parse_prompts", err)
	}

	toolHandler, err := o.builder.Build(ctx, agent.ID, version.ID, agent.Architecture)
	if err != nil {
		return failTurn("tool_handler_init", "build_tool_handler", err)
	}

	toolDefs, err := toolHandler.Tools(agent.Architecture)
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
	//
	// Before every LLM call we render any llm.ArtifactRefBlock content
	// blocks into provider-native inline media (via MultimodalRenderer)
	// or the text surrogate fallback. Refs on newly-appended tool-role
	// messages (see Phase 5 normalisation) are re-rendered by the same
	// call at the top of the loop.
	req := llm.Request{
		Model:     o.Model,
		System:    promptsHead.MainPrompt,
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

		// Render any artifact_ref blocks in the current message list
		// into provider-native inline media (or text surrogates). Runs
		// each iteration to also cover tool-role messages appended in
		// the previous round.
		rendered, renderErr := renderArtifactsForLLM(ctx, req.Messages, o.llm, o.artifactResolver)
		if renderErr != nil {
			return failTurn("artifact_render", "render_artifacts", renderErr)
		}
		req.Messages = rendered

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

		// Execute the pending tools and build the tool-role message.
		// Block order matters: every tool_result comes first (tool-call
		// order), then every artifact_ref (tool-call order, then item
		// order). Anthropic rejects a user message answering tool_use
		// whose content does not start with the tool_result blocks.
		toolResults := make([]llm.Block, 0, len(pendingToolUses))
		var toolRefs []llm.Block
		for _, tu := range pendingToolUses {
			res, err := toolHandler.Call(ctx, agent.Architecture, tools.Call{
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
			if o.artifactUploader != nil {
				// Error-flagged results are normalised too: an MCP isError
				// result may carry a screenshot of the failed state.
				nr := normaliseToolResult(ctx, res.Output, o.artifactUploader, o.logger, tu.Name)
				res.Output = nr.Output
				if nr.ForceError {
					res.IsError = true
				}
				for _, r := range nr.Refs {
					toolRefs = append(toolRefs, r)
				}
			}
			toolResults = append(toolResults, llm.ToolResultBlock{
				ToolUseID: tu.ID,
				Result:    res.Output,
				IsError:   res.IsError,
			})
		}
		toolBlocks := append(toolResults, toolRefs...)

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
		case "artifact_ref":
			var b struct {
				StoreID   string `json:"store_id"`
				URI       string `json:"uri"`
				MIME      string `json:"mime"`
				SizeBytes int64  `json:"size_bytes"`
				Sha256    string `json:"sha256"`
				Filename  string `json:"filename"`
			}
			_ = json.Unmarshal(r, &b)
			out = append(out, llm.ArtifactRefBlock{
				StoreID:   b.StoreID,
				URI:       b.URI,
				MIME:      b.MIME,
				SizeBytes: b.SizeBytes,
				Sha256:    b.Sha256,
				Filename:  b.Filename,
			})
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
		case llm.ArtifactRefBlock:
			m := map[string]any{
				"type":       "artifact_ref",
				"store_id":   x.StoreID,
				"uri":        x.URI,
				"mime":       x.MIME,
				"size_bytes": x.SizeBytes,
				"sha256":     x.Sha256,
			}
			if x.Filename != "" {
				m["filename"] = x.Filename
			}
			out = append(out, m)
		case llm.InlineMediaBlock:
			return nil, ErrInlineMediaNotPersistable
		}
	}
	return json.Marshal(out)
}

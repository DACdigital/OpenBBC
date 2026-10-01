package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

// turnSurface wires AppendUserTurn / AppendToolMessage / LoadMessages for BO
// and deployed behind one ChatMessage-shaped API.
type turnSurface struct {
	saSurface
	appendUser func(ctx context.Context, m types.ChatMessage) ([]llm.ArtifactRefBlock, error)
	appendTool func(ctx context.Context, m types.ChatMessage, refs []llm.ArtifactRefBlock) error
	load       func(ctx context.Context, sessionID string) ([]json.RawMessage, error) // content per message, seq order
	msgTable   string
}

func turnSurfaces(t *testing.T) []turnSurface {
	t.Helper()
	ss := saSurfaces(t)
	bo, depl := ss[0], ss[1]
	chat := bo.store.(*ChatRepository)
	dr := depl.store.(*DeployedRepository)
	var deployedVersion string
	if err := depl.db.QueryRow(`SELECT id::text FROM agent_versions WHERE status='DEPLOYED' LIMIT 1`).Scan(&deployedVersion); err != nil {
		t.Fatal(err)
	}
	return []turnSurface{
		{
			saSurface: bo, msgTable: "chat_messages",
			appendUser: func(ctx context.Context, m types.ChatMessage) ([]llm.ArtifactRefBlock, error) {
				return chat.AppendUserTurn(ctx, "", m)
			},
			appendTool: func(ctx context.Context, m types.ChatMessage, refs []llm.ArtifactRefBlock) error {
				return chat.AppendToolMessage(ctx, "", m, refs)
			},
			load: func(ctx context.Context, sid string) ([]json.RawMessage, error) {
				ms, err := chat.LoadMessages(ctx, sid)
				out := make([]json.RawMessage, len(ms))
				for i, m := range ms {
					out[i] = m.Content
				}
				return out, err
			},
		},
		{
			saSurface: depl, msgTable: "deployed_messages",
			appendUser: func(ctx context.Context, m types.ChatMessage) ([]llm.ArtifactRefBlock, error) {
				return dr.AppendUserTurn(ctx, types.DeployedMessage{ID: m.ID, SessionID: m.SessionID, AgentVersionID: deployedVersion, Role: m.Role, Content: m.Content, Seq: m.Seq})
			},
			appendTool: func(ctx context.Context, m types.ChatMessage, refs []llm.ArtifactRefBlock) error {
				return dr.AppendToolMessage(ctx, types.DeployedMessage{ID: m.ID, SessionID: m.SessionID, AgentVersionID: deployedVersion, Role: m.Role, Content: m.Content, Seq: m.Seq}, refs)
			},
			load: func(ctx context.Context, sid string) ([]json.RawMessage, error) {
				ms, err := dr.LoadMessages(ctx, sid)
				out := make([]json.RawMessage, len(ms))
				for i, m := range ms {
					out[i] = m.Content
				}
				return out, err
			},
		},
	}
}

func mustCommit(t *testing.T, store interface {
	CommitUpload(ctx context.Context, a types.SessionArtifact, maxPending int) (*types.SessionArtifact, error)
}, ctx context.Context, row types.SessionArtifact, maxPending int) *types.SessionArtifact {
	t.Helper()
	a, err := store.CommitUpload(ctx, row, maxPending)
	if err != nil {
		t.Fatalf("CommitUpload: %v", err)
	}
	return a
}

func userMsg(sid string, seq int, text string) types.ChatMessage {
	content := `[]`
	if text != "" {
		content = `[{"type":"text","text":` + string(mustJSON(text)) + `}]`
	}
	return types.ChatMessage{ID: uuid.NewString(), SessionID: sid, Role: types.ChatRoleUser, Content: json.RawMessage(content), Seq: seq}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func contentTypes(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var bs []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &bs); err != nil {
		t.Fatalf("content %s: %v", raw, err)
	}
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Type
	}
	return out
}

func TestAppendUserTurn_ClaimsInOrder(t *testing.T) {
	for _, s := range turnSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)
			a := mustCommit(t, s.store, ctx, pendingRow(sid, "sha256/aa", "a.png"), 10)
			b := mustCommit(t, s.store, ctx, pendingRow(sid, "sha256/bb", ""), 10)

			msg := userMsg(sid, 1, "summarise")
			refs, err := s.appendUser(ctx, msg)
			if err != nil {
				t.Fatalf("AppendUserTurn: %v", err)
			}
			if len(refs) != 2 || refs[0].URI != a.URI || refs[1].URI != b.URI || refs[0].Filename != "a.png" {
				t.Fatalf("refs = %+v", refs)
			}
			if refs[0].MIME != a.MIME || refs[0].SizeBytes != a.SizeBytes || refs[0].Sha256 != a.Sha256 || refs[0].StoreID != "MAIN" {
				t.Fatalf("ref fields differ from row: %+v vs %+v", refs[0], a)
			}
			contents, _ := s.load(ctx, sid)
			if got := contentTypes(t, contents[0]); len(got) != 3 || got[0] != "text" || got[1] != "artifact_ref" || got[2] != "artifact_ref" {
				t.Fatalf("persisted types = %v", got)
			}
			var n int
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM `+s.table+` WHERE session_id=$1::uuid AND message_id=$2::uuid`, sid, msg.ID).Scan(&n)
			if n != 2 {
				t.Fatalf("rows claimed by message = %d, want 2", n)
			}
			if list, _ := s.store.ListPendingArtifacts(ctx, sid); len(list) != 0 {
				t.Fatalf("still pending: %+v", list)
			}
		})
	}
}

func TestAppendUserTurn_NoPending_PersistsAsBefore(t *testing.T) {
	for _, s := range turnSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)
			refs, err := s.appendUser(ctx, userMsg(sid, 1, "hi"))
			if err != nil || len(refs) != 0 {
				t.Fatalf("refs=%v err=%v", refs, err)
			}
			contents, _ := s.load(ctx, sid)
			if got := contentTypes(t, contents[0]); len(got) != 1 || got[0] != "text" {
				t.Fatalf("types = %v", got)
			}
		})
	}
}

func TestAppendUserTurn_ArtifactOnly_Accepted(t *testing.T) {
	for _, s := range turnSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)
			mustCommit(t, s.store, ctx, pendingRow(sid, "sha256/aa", ""), 10)
			if _, err := s.appendUser(ctx, userMsg(sid, 1, "")); err != nil {
				t.Fatalf("AppendUserTurn: %v", err)
			}
			contents, _ := s.load(ctx, sid)
			if got := contentTypes(t, contents[0]); len(got) != 1 || got[0] != "artifact_ref" {
				t.Fatalf("types = %v", got)
			}
		})
	}
}

func TestAppendUserTurn_Empty_PersistsNothing(t *testing.T) {
	for _, s := range turnSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)
			if _, err := s.appendUser(ctx, userMsg(sid, 1, "")); !errors.Is(err, types.ErrEmptyTurn) {
				t.Fatalf("err=%v, want ErrEmptyTurn", err)
			}
			if contents, _ := s.load(ctx, sid); len(contents) != 0 {
				t.Fatalf("persisted %d messages", len(contents))
			}
		})
	}
}

func TestAppendUserTurn_ConcurrentTurnsClaimOnce(t *testing.T) {
	for _, s := range turnSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)
			mustCommit(t, s.store, ctx, pendingRow(sid, "sha256/aa", ""), 10)
			var wg sync.WaitGroup
			total := make([]int, 2)
			errs := make([]error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					refs, err := s.appendUser(ctx, userMsg(sid, i+1, "go"))
					total[i], errs[i] = len(refs), err
				}(i)
			}
			wg.Wait()
			if errs[0] != nil || errs[1] != nil {
				t.Fatalf("errs: %v", errs)
			}
			if total[0]+total[1] != 1 {
				t.Fatalf("ref claimed %d times, want exactly 1", total[0]+total[1])
			}
			contents, _ := s.load(ctx, sid)
			refBlocks := 0
			for _, c := range contents {
				for _, ty := range contentTypes(t, c) {
					if ty == "artifact_ref" {
						refBlocks++
					}
				}
			}
			if refBlocks != 1 {
				t.Fatalf("artifact_ref blocks across messages = %d, want 1", refBlocks)
			}
		})
	}
}

// DELETE racing a claim: either the delete wins (claim gets nothing) or the
// claim wins (delete gets ErrArtifactConsumed). Never both, never neither.
func TestDeleteVsClaim_ExactlyOneWins(t *testing.T) {
	for _, s := range turnSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			for i := 0; i < 20; i++ {
				sid := s.newSession(t)
				a := mustCommit(t, s.store, ctx, pendingRow(sid, "sha256/aa", ""), 10)
				var wg sync.WaitGroup
				var delErr, turnErr error
				var claimed int
				wg.Add(2)
				go func() { defer wg.Done(); delErr = s.store.DeletePendingArtifact(ctx, sid, a.ID) }()
				go func() {
					defer wg.Done()
					refs, err := s.appendUser(ctx, userMsg(sid, 1, "go"))
					claimed, turnErr = len(refs), err
				}()
				wg.Wait()
				if turnErr != nil {
					t.Fatalf("turn: %v", turnErr)
				}
				deleted := delErr == nil
				if deleted == (claimed == 1) {
					t.Fatalf("iteration %d: deleted=%v claimed=%d delErr=%v", i, deleted, claimed, delErr)
				}
				if !deleted && !errors.Is(delErr, types.ErrArtifactConsumed) {
					t.Fatalf("delete lost with %v, want ErrArtifactConsumed", delErr)
				}
			}
		})
	}
}

func TestAppendToolMessage_WritesRowsAtomically(t *testing.T) {
	for _, s := range turnSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)
			ref := llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/cc", MIME: "image/png", SizeBytes: 9, Sha256: "cc"}
			msg := types.ChatMessage{ID: uuid.NewString(), SessionID: sid, Role: types.ChatRoleTool, Seq: 1,
				Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"t1","content":{},"is_error":false},{"type":"artifact_ref","store_id":"MAIN","uri":"sha256/cc","mime":"image/png","size_bytes":9,"sha256":"cc"}]`)}
			if err := s.appendTool(ctx, msg, []llm.ArtifactRefBlock{ref, ref}); err != nil {
				t.Fatalf("AppendToolMessage: %v", err)
			}
			var n int
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM `+s.table+` WHERE session_id=$1::uuid AND origin='tool_result' AND message_id=$2::uuid`, sid, msg.ID).Scan(&n)
			if n != 2 {
				t.Fatalf("tool_result rows = %d, want 2 (same ref twice = two records)", n)
			}
			if _, err := s.store.LookupSessionArtifact(ctx, sid, "MAIN", "sha256/cc"); err != nil {
				t.Fatalf("tool-result ref not retrievable: %v", err)
			}
			// A failing row insert (NUL byte is invalid in Postgres text) rolls back the message.
			bad := llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/\x00", MIME: "image/png", SizeBytes: 1, Sha256: "x"}
			msg2 := msg
			msg2.ID, msg2.Seq = uuid.NewString(), 2
			if err := s.appendTool(ctx, msg2, []llm.ArtifactRefBlock{bad}); err == nil {
				t.Fatal("expected row insert failure")
			}
			var m int
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM `+s.msgTable+` WHERE id=$1::uuid`, msg2.ID).Scan(&m)
			if m != 0 {
				t.Fatal("tool message persisted although its row insert failed")
			}
		})
	}
}

// Claim order is (created_at, id), not heap/return order of the UPDATE.
func TestAppendUserTurn_ClaimOrderIsCreatedAtNotHeap(t *testing.T) {
	for _, s := range turnSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)
			a := mustCommit(t, s.store, ctx, pendingRow(sid, "sha256/aa", ""), 10)
			b := mustCommit(t, s.store, ctx, pendingRow(sid, "sha256/bb", ""), 10)
			// Make B older than A while heap order stays A, B.
			if _, err := s.db.Exec(`UPDATE `+s.table+` SET created_at = now() - interval '1 minute' WHERE id = $1::uuid`, b.ID); err != nil {
				t.Fatal(err)
			}
			refs, err := s.appendUser(ctx, userMsg(sid, 1, "go"))
			if err != nil {
				t.Fatalf("AppendUserTurn: %v", err)
			}
			if len(refs) != 2 || refs[0].URI != b.URI || refs[1].URI != a.URI {
				t.Fatalf("refs = %+v, want [B, A]", refs)
			}
			contents, _ := s.load(ctx, sid)
			var blocks []struct {
				Type string `json:"type"`
				URI  string `json:"uri"`
			}
			if err := json.Unmarshal(contents[0], &blocks); err != nil {
				t.Fatal(err)
			}
			if len(blocks) != 3 || blocks[1].URI != b.URI || blocks[2].URI != a.URI {
				t.Fatalf("persisted blocks = %+v, want text, B, A", blocks)
			}
		})
	}
}

// A failing message insert (duplicate seq) must roll the claim back.
func TestAppendUserTurn_MessageInsertFailureRollsBackClaim(t *testing.T) {
	for _, s := range turnSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)
			mustCommit(t, s.store, ctx, pendingRow(sid, "sha256/aa", ""), 10)
			if _, err := s.appendUser(ctx, userMsg(sid, 1, "first")); err != nil {
				t.Fatalf("first turn: %v", err)
			}
			b := mustCommit(t, s.store, ctx, pendingRow(sid, "sha256/bb", ""), 10)
			if _, err := s.appendUser(ctx, userMsg(sid, 1, "dup seq")); err == nil {
				t.Fatal("expected UNIQUE(session_id, seq) violation")
			}
			list, _ := s.store.ListPendingArtifacts(ctx, sid)
			if len(list) != 1 || list[0].ID != b.ID || list[0].MessageID != "" {
				t.Fatalf("pending after failed turn = %+v, want B still pending", list)
			}
		})
	}
}

func TestAppendUserTurn_InvalidContent_LeavesPending(t *testing.T) {
	for _, s := range turnSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)
			mustCommit(t, s.store, ctx, pendingRow(sid, "sha256/aa", ""), 10)
			m := userMsg(sid, 1, "x")
			m.Content = json.RawMessage(`not json`)
			if _, err := s.appendUser(ctx, m); err == nil {
				t.Fatal("expected error for invalid content")
			}
			if list, _ := s.store.ListPendingArtifacts(ctx, sid); len(list) != 1 {
				t.Fatalf("pending = %d, want 1", len(list))
			}
		})
	}
}

func TestAppendToolMessage_NilRefs(t *testing.T) {
	for _, s := range turnSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)
			msg := types.ChatMessage{ID: uuid.NewString(), SessionID: sid, Role: types.ChatRoleTool, Seq: 1,
				Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"t1","content":{},"is_error":false}]`)}
			if err := s.appendTool(ctx, msg, nil); err != nil {
				t.Fatalf("AppendToolMessage: %v", err)
			}
			if contents, _ := s.load(ctx, sid); len(contents) != 1 {
				t.Fatalf("messages = %d, want 1", len(contents))
			}
			var n int
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM `+s.table+` WHERE session_id=$1::uuid`, sid).Scan(&n)
			if n != 0 {
				t.Fatalf("rows = %d, want 0", n)
			}
		})
	}
}

func TestTurnWrites_DeletedSession_NotFound(t *testing.T) {
	for _, s := range turnSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)
			s.delSession(t, sid)
			if _, err := s.appendUser(ctx, userMsg(sid, 1, "hi")); !errors.Is(err, types.ErrNotFound) {
				t.Fatalf("AppendUserTurn err=%v, want ErrNotFound", err)
			}
			msg := types.ChatMessage{ID: uuid.NewString(), SessionID: sid, Role: types.ChatRoleTool, Seq: 1, Content: json.RawMessage(`[]`)}
			if err := s.appendTool(ctx, msg, nil); !errors.Is(err, types.ErrNotFound) {
				t.Fatalf("AppendToolMessage err=%v, want ErrNotFound", err)
			}
		})
	}
}

func TestAppendUserTurn_ClaimTieBreakIsID(t *testing.T) {
	for _, s := range turnSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)
			a := mustCommit(t, s.store, ctx, pendingRow(sid, "sha256/aa", ""), 10)
			b := mustCommit(t, s.store, ctx, pendingRow(sid, "sha256/bb", ""), 10)
			if _, err := s.db.Exec(`UPDATE `+s.table+` SET created_at = '2026-01-01T00:00:00Z' WHERE session_id = $1::uuid`, sid); err != nil {
				t.Fatal(err)
			}
			first, second := a, b
			if b.ID < a.ID {
				first, second = b, a
			}
			refs, err := s.appendUser(ctx, userMsg(sid, 1, "go"))
			if err != nil {
				t.Fatal(err)
			}
			if len(refs) != 2 || refs[0].URI != first.URI || refs[1].URI != second.URI {
				t.Fatalf("refs = %+v, want id order [%s, %s]", refs, first.ID, second.ID)
			}
		})
	}
}

// Spec: pending rows on a locked session are never claimed. The BO turn
// re-reads locked_at under a row lock as its first statement, so a turn
// that passed the handler's unlocked check just before a dataset close
// committed still refuses to claim.
func TestChatAppendUserTurn_LockedSession_NoClaim(t *testing.T) {
	s := turnSurfaces(t)[0]
	ctx := context.Background()
	sid := s.newSession(t)
	mustCommit(t, s.store, ctx, pendingRow(sid, "sha256/aa", ""), 10)
	if _, err := s.db.Exec(`UPDATE chat_sessions SET locked_at = now() WHERE id=$1::uuid`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.appendUser(ctx, userMsg(sid, 1, "go")); !errors.Is(err, types.ErrSessionLocked) {
		t.Fatalf("AppendUserTurn err=%v, want ErrSessionLocked", err)
	}
	pending, err := s.store.ListPendingArtifacts(ctx, sid)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending after refused turn: %d %v (want 1 still pending)", len(pending), err)
	}
	if contents, _ := s.load(ctx, sid); len(contents) != 0 {
		t.Fatalf("refused turn persisted %d messages", len(contents))
	}
}

// A dataset close that commits while a BO turn is blocked on the session row
// is seen by that turn: its first statement (UPDATE … RETURNING locked_at)
// waits on the row and re-reads the committed version.
func TestChatAppendUserTurn_LockCommittedWhileWaiting_NoClaim(t *testing.T) {
	s := turnSurfaces(t)[0]
	ctx := context.Background()
	sid := s.newSession(t)
	mustCommit(t, s.store, ctx, pendingRow(sid, "sha256/aa", ""), 10)
	closer, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closer.Rollback() }()
	if _, err := closer.ExecContext(ctx, `UPDATE chat_sessions SET locked_at = now() WHERE id=$1::uuid`, sid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.appendUser(ctx, userMsg(sid, 1, "go"))
		done <- err
	}()
	// Wait until the turn is blocked on the session row, then commit the lock.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("turn never blocked on the session row")
		}
		var waiting bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE pid <> pg_backend_pid() AND wait_event_type = 'Lock'
			AND query LIKE '%RETURNING locked_at%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("turn finished before the lock committed: %v", err)
		default:
		}
	}
	if err := closer.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, types.ErrSessionLocked) {
		t.Fatalf("AppendUserTurn err=%v, want ErrSessionLocked", err)
	}
	if pending, err := s.store.ListPendingArtifacts(ctx, sid); err != nil || len(pending) != 1 {
		t.Fatalf("pending after refused turn: %d %v", len(pending), err)
	}
}

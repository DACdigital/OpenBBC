package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/chat"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

// Spec client-context Scope 10 (Persistence): canonical survives JSONB
// byte-identically once decoded; context may be normalized. A fake repo
// cannot catch JSONB normalization, so this runs against real Postgres.
func TestClientContext_PostgresRoundTrip(t *testing.T) {
	raw := json.RawMessage(`{"n":1e3,"f":1.0,"big":123456789012345678901234567890,"neg":-0,"html":"<>&","sur":"\ud800"}`)
	canonical, err := chat.CanonicalizeClientContext(raw)
	if err != nil {
		t.Fatal(err)
	}
	block, err := json.Marshal(map[string]any{
		"type": "client_context", "canonical": canonical, "context": json.RawMessage(canonical),
	})
	if err != nil {
		t.Fatal(err)
	}
	content := json.RawMessage(`[` + string(block) + `,{"type":"text","text":"hi"}]`)

	for _, s := range turnSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)
			msg := types.ChatMessage{ID: uuid.NewString(), SessionID: sid, Role: types.ChatRoleUser, Content: content, Seq: 1}
			if _, err := s.appendUser(ctx, msg); err != nil {
				t.Fatalf("AppendUserTurn: %v", err)
			}
			contents, err := s.load(ctx, sid)
			if err != nil || len(contents) != 1 {
				t.Fatalf("load: %d messages, %v", len(contents), err)
			}
			if got := contentTypes(t, contents[0]); len(got) != 2 || got[0] != "client_context" || got[1] != "text" {
				t.Fatalf("persisted types = %v", got)
			}
			var blocks []struct {
				Canonical string          `json:"canonical"`
				Context   json.RawMessage `json:"context"`
			}
			if err := json.Unmarshal(contents[0], &blocks); err != nil {
				t.Fatal(err)
			}
			if blocks[0].Canonical != canonical {
				t.Fatalf("canonical changed in %s:\n got %q\nwant %q", s.msgTable, blocks[0].Canonical, canonical)
			}
			// context is informational: JSONB may normalize it (1e3 → 1000).
			t.Logf("%s context after JSONB: %s", s.msgTable, blocks[0].Context)
		})
	}
}

// The transactional empty-turn re-check never counts a client_context block.
func TestDecodeUserContent_ClientContextIsNotText(t *testing.T) {
	_, hasText, err := decodeUserContent(json.RawMessage(`[{"type":"client_context","canonical":"{}","context":{}}]`))
	if err != nil || hasText {
		t.Fatalf("hasText = %v, err = %v; want false, nil", hasText, err)
	}
}

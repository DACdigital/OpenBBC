package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

// saSurface abstracts BO vs deployed so every test runs on both tables.
type saSurface struct {
	name  string
	db    *sql.DB
	store interface {
		PrecheckUpload(ctx context.Context, sessionID, storeID, uri string, maxPending int) (*types.SessionArtifact, error)
		CommitUpload(ctx context.Context, a types.SessionArtifact, maxPending int) (*types.SessionArtifact, error)
		ListPendingArtifacts(ctx context.Context, sessionID string) ([]*types.SessionArtifact, error)
		DeletePendingArtifact(ctx context.Context, sessionID, id string) error
		LookupSessionArtifact(ctx context.Context, sessionID, storeID, uri string) (*types.SessionArtifact, error)
		HasPendingArtifacts(ctx context.Context, sessionID string) (bool, error)
	}
	table      string
	newSession func(t *testing.T) string
	delSession func(t *testing.T, id string)
}

func saSurfaces(t *testing.T) []saSurface {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t)
	vid := seedAgentVersion(t, db)
	chat := NewChatRepository(db)

	// Deployed: seed a deployed agent on the same (already truncated) DB.
	agentRepo, versionRepo := NewAgentRepository(db), NewAgentVersionRepository(db)
	agent, version, err := agentRepo.CreateFromWizard(ctx, types.CreateAgentFromWizardOpts{Name: "sa-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE agent_versions SET status='READY' WHERE id=$1`, version.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := versionRepo.Deploy(ctx, version.ID); err != nil {
		t.Fatal(err)
	}
	depl := NewDeployedRepository(db)

	return []saSurface{
		{
			name: "bo", db: db, store: chat, table: "chat_session_artifacts",
			newSession: func(t *testing.T) string {
				id := uuid.NewString()
				if err := chat.EnsureSession(ctx, id, vid); err != nil {
					t.Fatal(err)
				}
				return id
			},
			delSession: func(t *testing.T, id string) {
				if _, err := db.ExecContext(ctx, `DELETE FROM chat_sessions WHERE id=$1::uuid`, id); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "deployed", db: db, store: depl, table: "deployed_session_artifacts",
			newSession: func(t *testing.T) string {
				s, err := depl.CreateSession(ctx, agent.ID, "user-A", "")
				if err != nil {
					t.Fatal(err)
				}
				return s.ID
			},
			delSession: func(t *testing.T, id string) {
				if err := depl.DeleteSession(ctx, id, "user-A"); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
}

func pendingRow(sessionID, uri, filename string) types.SessionArtifact {
	return types.SessionArtifact{
		SessionID: sessionID, Origin: types.ArtifactOriginUpload, StoreID: "MAIN",
		URI: uri, MIME: "image/png", SizeBytes: 3, Sha256: uri[len("sha256/"):], Filename: filename,
	}
}

func TestSessionArtifacts_CommitDedupCapList(t *testing.T) {
	for _, s := range saSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)

			a, err := s.store.CommitUpload(ctx, pendingRow(sid, "sha256/aa", "a.png"), 2)
			if err != nil {
				t.Fatalf("commit a: %v", err)
			}
			if a.ID == "" || a.MessageID != "" || a.Origin != types.ArtifactOriginUpload {
				t.Fatalf("bad row %+v", a)
			}
			// Dedup: same blob, different filename -> same row, original filename.
			dup, err := s.store.CommitUpload(ctx, pendingRow(sid, "sha256/aa", "other.png"), 2)
			if err != nil || dup.ID != a.ID || dup.Filename != "a.png" {
				t.Fatalf("dedup: row=%+v err=%v", dup, err)
			}
			if _, err := s.store.CommitUpload(ctx, pendingRow(sid, "sha256/bb", ""), 2); err != nil {
				t.Fatalf("commit b: %v", err)
			}
			// At cap: new content refused, dedup still returns the row.
			if _, err := s.store.CommitUpload(ctx, pendingRow(sid, "sha256/cc", ""), 2); !errors.Is(err, types.ErrPendingArtifactCap) {
				t.Fatalf("over cap: err=%v", err)
			}
			if got, err := s.store.CommitUpload(ctx, pendingRow(sid, "sha256/aa", ""), 2); err != nil || got.ID != a.ID {
				t.Fatalf("dedup at cap: %+v %v", got, err)
			}
			// Pre-check mirrors commit.
			if got, err := s.store.PrecheckUpload(ctx, sid, "MAIN", "sha256/aa", 2); err != nil || got == nil || got.ID != a.ID {
				t.Fatalf("precheck dedup: %+v %v", got, err)
			}
			if _, err := s.store.PrecheckUpload(ctx, sid, "MAIN", "sha256/cc", 2); !errors.Is(err, types.ErrPendingArtifactCap) {
				t.Fatalf("precheck cap: %v", err)
			}
			if got, err := s.store.PrecheckUpload(ctx, sid, "MAIN", "sha256/cc", 3); err != nil || got != nil {
				t.Fatalf("precheck free: %+v %v", got, err)
			}
			// List in (created_at, id) order.
			list, err := s.store.ListPendingArtifacts(ctx, sid)
			if err != nil || len(list) != 2 || list[0].URI != "sha256/aa" || list[1].URI != "sha256/bb" {
				t.Fatalf("list: %+v %v", list, err)
			}
			if has, err := s.store.HasPendingArtifacts(ctx, sid); err != nil || !has {
				t.Fatalf("has pending: %v %v", has, err)
			}
			other := s.newSession(t)
			if list, err := s.store.ListPendingArtifacts(ctx, other); err != nil || list == nil || len(list) != 0 {
				t.Fatalf("empty list must be non-nil []: %#v %v", list, err)
			}
			if has, _ := s.store.HasPendingArtifacts(ctx, other); has {
				t.Fatal("other session reports pending")
			}
		})
	}
}

func TestSessionArtifacts_CommitOnGoneSession_NotFound(t *testing.T) {
	for _, s := range saSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			sid := s.newSession(t)
			s.delSession(t, sid)
			if _, err := s.store.CommitUpload(context.Background(), pendingRow(sid, "sha256/aa", ""), 10); !errors.Is(err, types.ErrNotFound) {
				t.Fatalf("err=%v, want ErrNotFound", err)
			}
		})
	}
}

func TestChatSessionArtifacts_CommitOnLockedSession_Conflict(t *testing.T) {
	s := saSurfaces(t)[0]
	sid := s.newSession(t)
	if _, err := s.db.Exec(`UPDATE chat_sessions SET locked_at = now() WHERE id=$1::uuid`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.CommitUpload(context.Background(), pendingRow(sid, "sha256/aa", ""), 10); !errors.Is(err, types.ErrSessionLocked) {
		t.Fatalf("err=%v, want ErrSessionLocked", err)
	}
}

// The FK-violation mapping is reachable only if a session disappears between
// the re-read and the insert; drive it directly with a no-op re-read.
func TestSessionArtifacts_InsertFKViolation_NotFound(t *testing.T) {
	db := openTestDB(t)
	sa := sessionArtifacts{db: db, table: "chat_session_artifacts", lockKey: chatSessionArtifactsLockKey,
		recheckSession: func(context.Context, *sql.Tx, string) error { return nil }}
	if _, err := sa.CommitUpload(context.Background(), pendingRow(uuid.NewString(), "sha256/aa", ""), 10); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}
}

func TestSessionArtifacts_ConcurrentCommitsRespectCap(t *testing.T) {
	const max = 3
	for _, s := range saSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			sid := s.newSession(t)
			var wg sync.WaitGroup
			errs := make([]error, max+1)
			for i := 0; i <= max; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					_, errs[i] = s.store.CommitUpload(context.Background(), pendingRow(sid, fmt.Sprintf("sha256/%02d", i), ""), max)
				}(i)
			}
			wg.Wait()
			refused := 0
			for _, err := range errs {
				switch {
				case errors.Is(err, types.ErrPendingArtifactCap):
					refused++
				case err != nil:
					t.Fatalf("unexpected: %v", err)
				}
			}
			var n int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+s.table+` WHERE session_id=$1::uuid`, sid).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if refused != 1 || n != max {
				t.Fatalf("refused=%d rows=%d, want 1 and %d", refused, n, max)
			}
		})
	}
}

func TestSessionArtifacts_DeleteAndLookup(t *testing.T) {
	for _, s := range saSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid, other := s.newSession(t), s.newSession(t)
			a, err := s.store.CommitUpload(ctx, pendingRow(sid, "sha256/aa", "a.png"), 10)
			if err != nil {
				t.Fatal(err)
			}

			if got, err := s.store.LookupSessionArtifact(ctx, sid, "MAIN", "sha256/aa"); err != nil || got.Filename != "a.png" {
				t.Fatalf("lookup pending: %+v %v", got, err)
			}
			if _, err := s.store.LookupSessionArtifact(ctx, other, "MAIN", "sha256/aa"); !errors.Is(err, types.ErrNotFound) {
				t.Fatalf("lookup other session: %v", err)
			}
			if err := s.store.DeletePendingArtifact(ctx, other, a.ID); !errors.Is(err, types.ErrNotFound) {
				t.Fatalf("delete from other session: %v", err)
			}
			if err := s.store.DeletePendingArtifact(ctx, sid, "not-a-uuid"); !errors.Is(err, types.ErrNotFound) {
				t.Fatalf("delete bad id: %v", err)
			}
			if err := s.store.DeletePendingArtifact(ctx, sid, a.ID); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if err := s.store.DeletePendingArtifact(ctx, sid, a.ID); !errors.Is(err, types.ErrNotFound) {
				t.Fatalf("repeat delete: %v", err)
			}
			if _, err := s.store.LookupSessionArtifact(ctx, sid, "MAIN", "sha256/aa"); !errors.Is(err, types.ErrNotFound) {
				t.Fatalf("lookup after delete: %v", err)
			}
			// Re-upload after removal is a new row; a removed row frees the cap.
			b, err := s.store.CommitUpload(ctx, pendingRow(sid, "sha256/aa", ""), 1)
			if err != nil || b.ID == a.ID {
				t.Fatalf("re-upload: %+v %v", b, err)
			}
			// Consumed rows cannot be removed.
			if _, err := s.db.Exec(`UPDATE `+s.table+` SET message_id = gen_random_uuid() WHERE id=$1::uuid`, b.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.store.DeletePendingArtifact(ctx, sid, b.ID); !errors.Is(err, types.ErrArtifactConsumed) {
				t.Fatalf("delete consumed: %v", err)
			}
			// Re-upload of consumed content creates a new pending row.
			c, err := s.store.CommitUpload(ctx, pendingRow(sid, "sha256/aa", ""), 1)
			if err != nil || c.ID == b.ID {
				t.Fatalf("re-upload consumed: %+v %v", c, err)
			}
		})
	}
}

// Lookup must return the NEWEST row when a consumed and a pending row exist
// for the same blob.
func TestSessionArtifacts_LookupReturnsNewest(t *testing.T) {
	for _, s := range saSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			sid := s.newSession(t)
			old, err := s.store.CommitUpload(ctx, pendingRow(sid, "sha256/aa", "old.png"), 10)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE `+s.table+` SET message_id = gen_random_uuid(),
				created_at = now() - interval '1 minute' WHERE id=$1::uuid`, old.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.store.CommitUpload(ctx, pendingRow(sid, "sha256/aa", "new.png"), 10); err != nil {
				t.Fatal(err)
			}
			got, err := s.store.LookupSessionArtifact(ctx, sid, "MAIN", "sha256/aa")
			if err != nil || got.Filename != "new.png" {
				t.Fatalf("lookup newest: %+v %v", got, err)
			}
		})
	}
}

// Postgres accepts several textual spellings of one uuid; the advisory lock
// must be keyed on the canonical form or the cap / dedup can be bypassed.
func TestSessionArtifacts_IDSpellingsShareLock(t *testing.T) {
	spell := func(id string, i int) string {
		switch i % 4 {
		case 0:
			return id
		case 1:
			return strings.ToUpper(id)
		case 2:
			return "{" + id + "}"
		}
		return strings.ReplaceAll(id, "-", "")
	}
	run := func(t *testing.T, s saSurface, max int, uri func(i int) string) ([]error, int) {
		sid := s.newSession(t)
		const n = 8
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = s.store.CommitUpload(context.Background(), pendingRow(spell(sid, i), uri(i), ""), max)
			}(i)
		}
		wg.Wait()
		var rows int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+s.table+` WHERE session_id=$1::uuid`, sid).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return errs, rows
	}
	for _, s := range saSurfaces(t) {
		t.Run(s.name+"/cap", func(t *testing.T) {
			errs, rows := run(t, s, 1, func(i int) string { return fmt.Sprintf("sha256/%02d", i) })
			refused := 0
			for _, err := range errs {
				switch {
				case errors.Is(err, types.ErrPendingArtifactCap):
					refused++
				case err != nil:
					t.Fatalf("unexpected: %v", err)
				}
			}
			if rows != 1 || refused != len(errs)-1 {
				t.Fatalf("rows=%d refused=%d, want 1 and %d", rows, refused, len(errs)-1)
			}
		})
		t.Run(s.name+"/same-blob", func(t *testing.T) {
			errs, rows := run(t, s, 10, func(int) string { return "sha256/aa" })
			for _, err := range errs {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
			}
			if rows != 1 {
				t.Fatalf("rows=%d, want 1", rows)
			}
		})
	}
}

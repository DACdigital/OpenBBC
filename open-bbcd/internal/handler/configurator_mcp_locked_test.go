package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
)

// MCP writes on a version outside INITIALIZING/DRAFT are refused with 409 and
// render the #mcp-error fragment (spec 2026-11-24-declarative-seed §4).
func TestMCPTab_LockedVersion409(t *testing.T) {
	db := openConfiguratorTestDB(t)
	versionID := seedConfiguratorAgentVersion(t, db) // DRAFT
	be := seedConfiguratorMCPBackend(t, db, "slack-locked")
	if err := repository.NewVersionWiringRepository(db).AttachMCP(context.Background(), versionID, be, "keep"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agent_versions SET status = 'READY' WHERE id = $1::uuid`, versionID); err != nil {
		t.Fatal(err)
	}
	h := newConfigHandlerWithDB(t, db)

	t.Run("toggle", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/agent_versions/"+versionID+"/architecture/mcp/"+be+"/toggle", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("version_id", versionID)
		req.SetPathValue("backendID", be)
		w := httptest.NewRecorder()
		h.ToggleMCPBackend(w, req) // no "attached" → detach

		if w.Code != http.StatusConflict {
			t.Fatalf("status %d, want 409; body %s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, `id="mcp-error"`) || !strings.Contains(body, "INITIALIZING or DRAFT") {
			t.Fatalf("missing #mcp-error fragment: %s", body)
		}
		// The row snaps back out-of-band, still attached.
		if !strings.Contains(body, `id="mcp-row-`+be+`" hx-swap-oob="true"`) || !strings.Contains(body, "checked") {
			t.Fatalf("missing OOB snap-back row: %s", body)
		}
	})

	t.Run("notes", func(t *testing.T) {
		form := url.Values{"note[" + be + "]": {"changed"}}
		req := httptest.NewRequest(http.MethodPost, "/agent_versions/"+versionID+"/architecture/mcp/notes", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("version_id", versionID)
		w := httptest.NewRecorder()
		h.UpdateAllMCPNotes(w, req)

		if w.Code != http.StatusConflict {
			t.Fatalf("status %d, want 409; body %s", w.Code, w.Body.String())
		}
		if body := w.Body.String(); !strings.Contains(body, `id="mcp-error"`) {
			t.Fatalf("missing #mcp-error fragment: %s", body)
		}
		atts, err := repository.NewVersionWiringRepository(db).ListMCPAttachments(context.Background(), versionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(atts) != 1 || atts[0].Note != "keep" {
			t.Fatalf("attachment changed: %+v", atts)
		}
	})
}

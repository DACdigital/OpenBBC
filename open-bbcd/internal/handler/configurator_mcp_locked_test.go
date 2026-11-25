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

// The MCP tab renders its controls disabled (and no Save button) unless the
// version is INITIALIZING or DRAFT, matching the 409 the writes would get.
func TestMCPTab_ControlsDisabledOutsideEditableStatus(t *testing.T) {
	for _, tc := range []struct {
		status   string
		disabled bool
	}{
		{"DRAFT", false},
		{"INITIALIZING", false},
		{"READY", true},
		{"DEPLOYED", true},
	} {
		t.Run(tc.status, func(t *testing.T) {
			db := openConfiguratorTestDB(t)
			versionID := seedConfiguratorAgentVersion(t, db) // DRAFT
			attached := seedConfiguratorMCPBackend(t, db, "mcp-att-"+tc.status)
			seedConfiguratorMCPBackend(t, db, "mcp-free-"+tc.status)
			if err := repository.NewVersionWiringRepository(db).AttachMCP(context.Background(), versionID, attached, "n"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE agent_versions SET status = $2 WHERE id = $1::uuid`, versionID, tc.status); err != nil {
				t.Fatal(err)
			}
			h := newConfigHandlerWithDB(t, db)

			req := httptest.NewRequest(http.MethodGet, "/agent_versions/"+versionID+"/architecture/mcp", nil)
			req.SetPathValue("version_id", versionID)
			w := httptest.NewRecorder()
			h.MCPSubtab(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d; body %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			start := strings.Index(body, `id="mcp-form-block"`)
			if start < 0 {
				t.Fatalf("no #mcp-form-block in page")
			}
			body = body[start:]
			nDisabled := strings.Count(body, " disabled")
			hasSave := strings.Contains(body, `id="mcp-notes-form"`)
			if tc.disabled {
				// two checkboxes + one textarea
				if nDisabled < 3 {
					t.Errorf("disabled controls = %d, want >= 3:\n%s", nDisabled, body)
				}
				if hasSave {
					t.Errorf("Save form rendered on a locked version")
				}
				if strings.Contains(body, "hx-post=\"/agent_versions/"+versionID+"/architecture/mcp/") {
					t.Errorf("write controls still carry hx-post on a locked version")
				}
			} else {
				if nDisabled != 0 {
					t.Errorf("disabled controls = %d, want 0:\n%s", nDisabled, body)
				}
				if !hasSave {
					t.Errorf("Save form missing on an editable version")
				}
			}
		})
	}
}

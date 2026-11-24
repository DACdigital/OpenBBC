package handler

import (
	"net/http"
	"strings"
	"testing"
)

func TestExportModal_ListsClosureWithDefaults(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newResolveAPI(t, db)
	agentID, versionID := seedStaticAgent(t, db, false) // agent "sales" / version "sales_v1", both fixed

	// A bound sub-agent whose agent and version have no static ID yet.
	var subAgent, subVersion string
	if err := db.QueryRow(`INSERT INTO agents (name) VALUES ('Helper Bot') RETURNING id::text`).Scan(&subAgent); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO agent_versions (agent_id, status, flow_map_config) VALUES ($1::uuid, 'READY', '{}'::jsonb) RETURNING id::text`, subAgent).Scan(&subVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_version_subagent (caller_version_id, target_version_id, name, note) VALUES ($1::uuid, $2::uuid, 'helper', '')`, versionID, subVersion); err != nil {
		t.Fatal(err)
	}

	rr := do(api, "GET", "/agent_versions/"+versionID+"/export-modal", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()

	// Fixed IDs are read-only and carry the existing value.
	for _, want := range []string{
		`data-row-id="` + agentID + `" value="sales"`,
		`data-row-id="` + versionID + `" value="sales_v1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if n := strings.Count(body, "readonly data-row-id"); n != 2 {
		t.Errorf("readonly inputs = %d, want 2 (only the fixed rows)", n)
	}
	// Unassigned rows are editable and prefilled with defaults.
	for _, want := range []string{
		`data-row-id="` + subAgent + `" value="helper-bot"`,
		`data-row-id="` + subVersion + `" value="helper-bot_v1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "Backend URLs are exported as-is — a token embedded in a URL will be in the file.") {
		t.Error("missing URL warning")
	}
	if !strings.Contains(body, `action="/agent_versions/`+versionID+`/export"`) && !strings.Contains(body, `data-action="/agent_versions/`+versionID+`/export"`) {
		t.Error("missing export target")
	}
	for _, opt := range []string{`value="none"`, `value="if_none" selected`, `value="force"`} {
		if !strings.Contains(body, opt) {
			t.Errorf("missing deploy option %s", opt)
		}
	}
	// Preview must not persist anything.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM agents WHERE id = $1::uuid AND static_id IS NOT NULL`, subAgent).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("preview persisted a static ID")
	}
}

func TestExportModal_NotExportableIs409(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newResolveAPI(t, db)
	_, versionID := seedStaticAgent(t, db, false)
	if _, err := db.Exec(`UPDATE agent_versions SET status='DRAFT' WHERE id=$1::uuid`, versionID); err != nil {
		t.Fatal(err)
	}
	if rr := do(api, "GET", "/agent_versions/"+versionID+"/export-modal", "", ""); rr.Code != http.StatusConflict {
		t.Errorf("got %d, want 409", rr.Code)
	}
}

package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/seed"
)

func TestSeedExport_ByStaticID(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newResolveAPI(t, db)
	_, versionID := seedStaticAgent(t, db, false)
	var backendID string
	if err := db.QueryRow(`INSERT INTO tool_backends (name, kind, config)
		VALUES ('crm-mcp-'||substr(gen_random_uuid()::text,1,8), 'mcp_client',
		        '{"url":"https://mcp","default_headers":{"Authorization":"x","X-Tenant":"t"}}'::jsonb)
		RETURNING id::text`).Scan(&backendID); err != nil {
		t.Fatal(err)
	}
	var backendName string
	if err := db.QueryRow(`SELECT name FROM tool_backends WHERE id = $1::uuid`, backendID).Scan(&backendName); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_version_mcp_backend (agent_version_id, backend_id) VALUES ($1::uuid, $2::uuid)`, versionID, backendID); err != nil {
		t.Fatal(err)
	}

	rr := do(api, "POST", "/agent_versions/sales_v1/export", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); cd != "attachment; filename=sales_v1.seed.yaml" {
		t.Errorf("Content-Disposition = %q", cd)
	}
	prefix := seedEnvPrefix(backendName)
	if got, want := rr.Header().Get("X-Seed-Env-Vars"), prefix+"_AUTHORIZATION,"+prefix+"_X_TENANT"; got != want {
		t.Errorf("X-Seed-Env-Vars = %q, want %q", got, want)
	}

	path := filepath.Join(t.TempDir(), "x.yaml")
	if err := os.WriteFile(path, rr.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := seed.Load(path)
	if err != nil {
		t.Fatalf("Load: %v\n%s", err, rr.Body.String())
	}
	if len(s.Versions) != 1 || s.Versions[0].StaticID != "sales_v1" || s.Versions[0].Deploy != seed.DeployIfNone {
		t.Errorf("versions = %+v", s.Versions)
	}

	// The UUID path addresses the same version.
	if rr := do(api, "POST", "/agent_versions/"+versionID+"/export", "application/json", `{"deploy":"force"}`); rr.Code != http.StatusOK {
		t.Errorf("by uuid: got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSeedExport_Errors(t *testing.T) {
	db := openTestDBForHandlers(t)
	api := newResolveAPI(t, db)
	agentID, versionID := seedStaticAgent(t, db, false)

	cases := []struct {
		name, target, body string
		want               int
	}{
		{"unknown version", "/agent_versions/nope_v9/export", "", http.StatusNotFound},
		{"bad json", "/agent_versions/sales_v1/export", "{", http.StatusBadRequest},
		{"bad deploy", "/agent_versions/sales_v1/export", `{"deploy":"always"}`, http.StatusBadRequest},
		{"invalid static id", "/agent_versions/sales_v1/export", `{"static_ids":{"` + versionID + `":"Not Valid"}}`, http.StatusBadRequest},
		{"static id conflict", "/agent_versions/sales_v1/export", `{"static_ids":{"` + agentID + `":"other"}}`, http.StatusConflict},
	}
	for _, c := range cases {
		if rr := do(api, "POST", c.target, "application/json", c.body); rr.Code != c.want {
			t.Errorf("%s: got %d, want %d: %s", c.name, rr.Code, c.want, rr.Body.String())
		}
	}

	if _, err := db.Exec(`UPDATE agent_versions SET status = 'DRAFT' WHERE id = $1::uuid`, versionID); err != nil {
		t.Fatal(err)
	}
	if rr := do(api, "POST", "/agent_versions/sales_v1/export", "", ""); rr.Code != http.StatusConflict {
		t.Errorf("draft: got %d, want 409: %s", rr.Code, rr.Body.String())
	}
}

// seedEnvPrefix upper-snake-cases a backend name the way the export does.
func seedEnvPrefix(name string) string {
	out := []byte{}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
			out = append(out, c-'a'+'A')
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
		default:
			if len(out) > 0 && out[len(out)-1] != '_' {
				out = append(out, '_')
			}
		}
	}
	return string(out)
}

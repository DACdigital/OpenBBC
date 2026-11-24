package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/seed"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// Seed export (declarative-seed spec §5).

const exportSalesArch = `{"tools":[{"id":"get_customer","name":"get_customer"}],"limits":{"max":3,"ratio":0.1,"big":9007199254740993}}`

// exportFixture is a source DB with no static IDs:
//
//	research (READY root, "Research bot")
//	sales v1 (READY root, flow_map_config) → sales v2 (READY), the export
//	target, binding research's version as "researcher" and attaching two MCP
//	backends whose Authorization headers collide after upper-snake-casing.
type exportFixture struct {
	salesID, researchID          string
	salesV1, salesV2, researchV1 string
}

func execOne(t *testing.T, db *sql.DB, q string, args ...any) string {
	t.Helper()
	var id string
	if err := db.QueryRow(q, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", strings.Fields(q)[0:3], err)
	}
	return id
}

func insertExportAgent(t *testing.T, db *sql.DB, name, desc, arch string) string {
	t.Helper()
	return execOne(t, db, `INSERT INTO agents (name, description, architecture, finalized_at)
		VALUES ($1, NULLIF($2, ''), $3::jsonb, now()) RETURNING id::text`, name, desc, arch)
}

func insertExportVersion(t *testing.T, db *sql.DB, agentID, parentID string, status types.AgentStatus, prompts string, flowMap *string) string {
	t.Helper()
	var parent any
	if parentID != "" {
		parent = parentID
	}
	return execOne(t, db, `INSERT INTO agent_versions (agent_id, parent_version_id, status, prompts, flow_map_config)
		VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, $5::jsonb) RETURNING id::text`,
		agentID, parent, string(status), prompts, flowMap)
}

func insertExportBackend(t *testing.T, db *sql.DB, name, kind, config string) string {
	t.Helper()
	return execOne(t, db, `INSERT INTO tool_backends (name, kind, config) VALUES ($1, $2, $3::jsonb) RETURNING id::text`, name, kind, config)
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func seedExportFixture(t *testing.T, db *sql.DB) exportFixture {
	t.Helper()
	var f exportFixture
	f.researchID = insertExportAgent(t, db, "Research bot", "", `{"tools":[]}`)
	f.researchV1 = insertExportVersion(t, db, f.researchID, "", types.AgentStatusReady, `{"main_prompt":"research"}`, nil)

	flowMap := `{"name":"sales","n":2}`
	f.salesID = insertExportAgent(t, db, "Sales", "Sells things", exportSalesArch)
	f.salesV1 = insertExportVersion(t, db, f.salesID, "", types.AgentStatusReady, `{"main_prompt":"v1"}`, &flowMap)
	f.salesV2 = insertExportVersion(t, db, f.salesID, f.salesV1, types.AgentStatusReady,
		`{"main_prompt":"hi ${NOT_A_VAR} and $${y}","skill_prompts":{"a":"b"}}`, nil)
	mustExec(t, db, `UPDATE agent_versions SET agent_tool_enabled = true WHERE id = $1::uuid`, f.salesV2)

	api := insertExportBackend(t, db, "crm-api", "http_endpoint", `{"base_url":"https://api.example","default_headers":{"X-Key":"k1"}}`)
	mcpA := insertExportBackend(t, db, "crm-mcp", "mcp_client",
		`{"url":"https://mcp.example/${x}?q=$${z}","transport":"streamable_http","default_headers":{"Authorization":"Bearer s1"}}`)
	mcpB := insertExportBackend(t, db, "crm_mcp", "mcp_client",
		`{"url":"https://mcp2.example","default_headers":{"authorization":"Bearer s2"}}`)
	insertExportBackend(t, db, "unrelated", "mcp_client", `{"url":"https://nope"}`)

	mustExec(t, db, `INSERT INTO agent_endpoint_backend (agent_id, endpoint_id, backend_id) VALUES ($1::uuid, 'get_customer', $2::uuid)`, f.salesID, api)
	mustExec(t, db, `INSERT INTO agent_version_mcp_backend (agent_version_id, backend_id, note) VALUES ($1::uuid, $2::uuid, 'crm tools')`, f.salesV2, mcpA)
	mustExec(t, db, `INSERT INTO agent_version_mcp_backend (agent_version_id, backend_id, note) VALUES ($1::uuid, $2::uuid, '')`, f.salesV2, mcpB)
	mustExec(t, db, `INSERT INTO agent_version_subagent (caller_version_id, target_version_id, name, note) VALUES ($1::uuid, $2::uuid, 'researcher', 'ask it')`, f.salesV2, f.researchV1)
	return f
}

func mustExport(t *testing.T, r *SeedRepository, versionID string, req ExportRequest) (*seed.Set, []string) {
	t.Helper()
	s, env, err := r.Export(context.Background(), versionID, req)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	return s, env
}

func staticIDOf(t *testing.T, db *sql.DB, table, id string) string {
	t.Helper()
	var sid sql.NullString
	if err := db.QueryRow(`SELECT static_id FROM `+table+` WHERE id = $1::uuid`, id).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	return sid.String
}

func TestExport_StatusPrecondition(t *testing.T) {
	db := openTestDB(t)
	r := NewSeedRepository(db)
	agent := insertExportAgent(t, db, "A", "", `{}`)
	for _, st := range []types.AgentStatus{types.AgentStatusDraft, types.AgentStatusInitializing, types.AgentStatusPending} {
		mustExec(t, db, `DELETE FROM agent_versions`)
		v := insertExportVersion(t, db, agent, "", st, `{}`, nil)
		if _, _, err := r.Export(context.Background(), v, ExportRequest{}); !errors.Is(err, types.ErrVersionNotExportable) {
			t.Errorf("%s: err = %v, want ErrVersionNotExportable", st, err)
		}
		if sid := staticIDOf(t, db, "agent_versions", v); sid != "" {
			t.Errorf("%s: static_id persisted on refused export: %q", st, sid)
		}
	}
	for _, st := range []types.AgentStatus{types.AgentStatusTraining, types.AgentStatusDeployed} {
		mustExec(t, db, `DELETE FROM agent_versions`)
		v := insertExportVersion(t, db, agent, "", st, `{}`, nil)
		if _, _, err := r.Export(context.Background(), v, ExportRequest{}); err != nil {
			t.Errorf("%s: %v", st, err)
		}
	}
	if _, _, err := r.Export(context.Background(), "00000000-0000-0000-0000-000000000000", ExportRequest{}); !errors.Is(err, types.ErrNotFound) {
		t.Errorf("unknown version: err = %v, want ErrNotFound", err)
	}
}

func TestExport_ClosureAndDefaults(t *testing.T) {
	db := openTestDB(t)
	f := seedExportFixture(t, db)
	s, _ := mustExport(t, NewSeedRepository(db), f.salesV2, ExportRequest{})

	if len(s.Versions) != 2 || s.Versions[0].StaticID != "sales_v2" || s.Versions[1].StaticID != "research-bot_v1" {
		t.Fatalf("versions = %+v", s.Versions)
	}
	if len(s.Agents) != 2 || s.Agents[0].StaticID != "sales" || s.Agents[1].StaticID != "research-bot" {
		t.Fatalf("agents = %+v", s.Agents)
	}
	var names []string
	for _, b := range s.Backends {
		names = append(names, b.Name)
	}
	if !reflect.DeepEqual(names, []string{"crm-api", "crm-mcp", "crm_mcp"}) {
		t.Errorf("backends = %v", names)
	}
	if s.Versions[0].Deploy != seed.DeployIfNone || s.Versions[1].Deploy != seed.DeployNone {
		t.Errorf("deploy = %q / %q", s.Versions[0].Deploy, s.Versions[1].Deploy)
	}
	v := s.Versions[0]
	if v.Agent != "sales" || !v.AgentToolEnabled ||
		!reflect.DeepEqual(v.SubAgents, []seed.SubAgentRef{{Name: "researcher", Target: "research-bot_v1", Note: "ask it"}}) ||
		!reflect.DeepEqual(v.MCPBackends, []seed.MCPRef{{Backend: "crm-mcp", Note: "crm tools"}, {Backend: "crm_mcp"}}) {
		t.Errorf("version = %+v", v)
	}
	a := s.Agents[0]
	if a.Description != "Sells things" || !reflect.DeepEqual(a.EndpointBackends, map[string]string{"get_customer": "crm-api"}) {
		t.Errorf("agent = %+v", a)
	}
	if a.FlowMapConfig["name"] != "sales" {
		t.Errorf("flow_map_config = %v (want the root version's)", a.FlowMapConfig)
	}
	if s.Agents[1].FlowMapConfig != nil || s.Agents[1].Description != "" {
		t.Errorf("research agent = %+v", s.Agents[1])
	}

	for table, want := range map[string]map[string]string{
		"agents":         {f.salesID: "sales", f.researchID: "research-bot"},
		"agent_versions": {f.salesV2: "sales_v2", f.researchV1: "research-bot_v1", f.salesV1: ""},
	} {
		for id, sid := range want {
			if got := staticIDOf(t, db, table, id); got != sid {
				t.Errorf("%s %s static_id = %q, want %q", table, id, got, sid)
			}
		}
	}
}

func TestExport_SuppliedIDsAndDeploy(t *testing.T) {
	db := openTestDB(t)
	f := seedExportFixture(t, db)
	s, _ := mustExport(t, NewSeedRepository(db), f.salesV2, ExportRequest{
		StaticIDs: map[string]string{strings.ToUpper(f.salesID): "seller", f.researchV1: "rs1"},
		Deploy:    seed.DeployForce,
	})
	if s.Agents[0].StaticID != "seller" || s.Versions[0].StaticID != "seller_v2" || s.Versions[1].StaticID != "rs1" {
		t.Errorf("ids = %s %s %s", s.Agents[0].StaticID, s.Versions[0].StaticID, s.Versions[1].StaticID)
	}
	if s.Versions[0].Deploy != seed.DeployForce {
		t.Errorf("deploy = %q", s.Versions[0].Deploy)
	}

	// Re-supplying the same IDs is fine.
	mustExport(t, NewSeedRepository(db), f.salesV2, ExportRequest{StaticIDs: map[string]string{f.salesID: "seller"}})

	_, _, err := NewSeedRepository(db).Export(context.Background(), f.salesV2, ExportRequest{StaticIDs: map[string]string{f.salesID: "Bad ID"}})
	if !errors.Is(err, types.ErrInvalidStaticID) {
		t.Errorf("invalid static id: err = %v", err)
	}
}

func TestExport_Stable(t *testing.T) {
	db := openTestDB(t)
	f := seedExportFixture(t, db)
	r := NewSeedRepository(db)
	s1, _ := mustExport(t, r, f.salesV2, ExportRequest{})
	s2, _ := mustExport(t, r, f.salesV2, ExportRequest{})
	b1, err := seed.Marshal(s1)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := seed.Marshal(s2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b1, b2) {
		t.Errorf("exports differ:\n%s\n---\n%s", b1, b2)
	}
}

func TestExport_StaticIDConflict(t *testing.T) {
	db := openTestDB(t)
	f := seedExportFixture(t, db)
	mustExec(t, db, `UPDATE agents SET static_id = 'sales' WHERE id = $1::uuid`, f.salesID)
	r := NewSeedRepository(db)

	_, _, err := r.Export(context.Background(), f.salesV2, ExportRequest{StaticIDs: map[string]string{f.salesID: "other"}})
	if !errors.Is(err, types.ErrStaticIDConflict) {
		t.Fatalf("different id for an existing one: err = %v, want ErrStaticIDConflict", err)
	}
	// A supplied ID already used by another row is a conflict too.
	_, _, err = r.Export(context.Background(), f.salesV2, ExportRequest{StaticIDs: map[string]string{f.researchID: "sales"}})
	if !errors.Is(err, types.ErrStaticIDConflict) {
		t.Fatalf("taken id: err = %v, want ErrStaticIDConflict", err)
	}
	// Nothing persisted by the failed exports.
	if sid := staticIDOf(t, db, "agent_versions", f.salesV2); sid != "" {
		t.Errorf("version static_id persisted on failed export: %q", sid)
	}
}

func TestExport_DefaultCollisionSuffix(t *testing.T) {
	db := openTestDB(t)
	f := seedExportFixture(t, db)
	other := insertExportAgent(t, db, "Other", "", `{}`)
	mustExec(t, db, `UPDATE agents SET static_id = 'sales' WHERE id = $1::uuid`, other)
	otherV := insertExportVersion(t, db, other, "", types.AgentStatusReady, `{}`, nil)
	mustExec(t, db, `UPDATE agent_versions SET static_id = 'sales-2_v2' WHERE id = $1::uuid`, otherV)

	s, _ := mustExport(t, NewSeedRepository(db), f.salesV2, ExportRequest{})
	if s.Agents[0].StaticID != "sales-2" {
		t.Errorf("agent static_id = %q, want sales-2", s.Agents[0].StaticID)
	}
	if s.Versions[0].StaticID != "sales-2_v2-2" {
		t.Errorf("version static_id = %q, want sales-2_v2-2", s.Versions[0].StaticID)
	}
}

func TestExport_Placeholders(t *testing.T) {
	db := openTestDB(t)
	f := seedExportFixture(t, db)
	s, env := mustExport(t, NewSeedRepository(db), f.salesV2, ExportRequest{})

	byName := map[string]map[string]any{}
	for _, b := range s.Backends {
		byName[b.Name] = b.Config
	}
	headers := func(b string) map[string]any { return byName[b]["default_headers"].(map[string]any) }
	if got := headers("crm-mcp")["Authorization"]; got != "${CRM_MCP_AUTHORIZATION}" {
		t.Errorf("crm-mcp Authorization = %v", got)
	}
	if got := headers("crm_mcp")["authorization"]; got != "${CRM_MCP_AUTHORIZATION_2}" {
		t.Errorf("crm_mcp authorization = %v (want _2 suffix)", got)
	}
	if got := headers("crm-api")["X-Key"]; got != "${CRM_API_X_KEY}" {
		t.Errorf("crm-api X-Key = %v", got)
	}
	if got := byName["crm-mcp"]["url"]; got != "https://mcp.example/$${x}?q=$$${z}" {
		t.Errorf("url = %v (want literals escaped)", got)
	}
	if got := byName["crm-mcp"]["transport"]; got != "streamable_http" {
		t.Errorf("transport = %v", got)
	}
	want := []string{"CRM_API_X_KEY", "CRM_MCP_AUTHORIZATION", "CRM_MCP_AUTHORIZATION_2"}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("env vars = %v, want %v", env, want)
	}
	if got := seed.RequiredEnv(s); !reflect.DeepEqual(got, want) {
		t.Errorf("RequiredEnv(export) = %v, want %v", got, want)
	}
}

// exportSnapshot is the round-trip-relevant content of a DB, keyed by static
// IDs and backend names.
type exportSnapshot struct {
	Versions map[string]string // static_id → canonical JSON of content + agent
	Agents   map[string]string // static_id → canonical JSON of name, description, architecture, root flow map, wiring
	Backends map[string]string // name → kind + canonical config
}

func snapshotExport(t *testing.T, db *sql.DB, versionSIDs, agentSIDs, backends []string) exportSnapshot {
	t.Helper()
	ctx := context.Background()
	snap := exportSnapshot{Versions: map[string]string{}, Agents: map[string]string{}, Backends: map[string]string{}}
	canon := func(v any) string {
		b, err := seed.CanonicalJSON(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, sid := range versionSIDs {
		id := idByStaticID(t, db, "agent_versions", sid)
		c, err := dbVersionContent(ctx, db, id)
		if err != nil {
			t.Fatal(err)
		}
		var agentSID string
		if err := db.QueryRow(`SELECT a.static_id FROM agents a JOIN agent_versions v ON v.agent_id = a.id WHERE v.id = $1::uuid`, id).Scan(&agentSID); err != nil {
			t.Fatal(err)
		}
		snap.Versions[sid] = canon(map[string]any{
			"agent": agentSID, "prompts": c.Prompts, "agent_tool_enabled": c.AgentToolEnabled,
			"mcp": sortedMCP(c.MCP), "subs": sortedSubs(c.SubAgents),
		})
	}
	for _, sid := range agentSIDs {
		id := idByStaticID(t, db, "agents", sid)
		var name, desc string
		var arch, flowMap []byte
		if err := db.QueryRow(`
			SELECT a.name, COALESCE(a.description, ''), a.architecture,
			       COALESCE((SELECT flow_map_config FROM agent_versions WHERE agent_id = a.id AND parent_version_id IS NULL), 'null'::jsonb)
			FROM agents a WHERE a.id = $1::uuid`, id).Scan(&name, &desc, &arch, &flowMap); err != nil {
			t.Fatal(err)
		}
		wiring := map[string]string{}
		rows, err := db.Query(`SELECT e.endpoint_id, b.name FROM agent_endpoint_backend e JOIN tool_backends b ON b.id = e.backend_id WHERE e.agent_id = $1::uuid`, id)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var ep, b string
			if err := rows.Scan(&ep, &b); err != nil {
				t.Fatal(err)
			}
			wiring[ep] = b
		}
		rows.Close()
		snap.Agents[sid] = canon(map[string]any{
			"name": name, "description": desc, "architecture": json.RawMessage(arch),
			"flow_map_config": json.RawMessage(flowMap), "wiring": wiring,
		})
	}
	for _, name := range backends {
		var kind string
		var cfg []byte
		if err := db.QueryRow(`SELECT kind, config FROM tool_backends WHERE name = $1`, name).Scan(&kind, &cfg); err != nil {
			t.Fatal(err)
		}
		snap.Backends[name] = canon(map[string]any{"kind": kind, "config": json.RawMessage(cfg)})
	}
	return snap
}

func sortedMCP(in []seed.MCPRef) []seed.MCPRef {
	out := append([]seed.MCPRef{}, in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j].Backend < out[i].Backend {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func sortedSubs(in []seed.SubAgentRef) []seed.SubAgentRef {
	out := append([]seed.SubAgentRef{}, in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j].Name < out[i].Name {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestExportImport_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	f := seedExportFixture(t, db)
	set, env := mustExport(t, NewSeedRepository(db), f.salesV2, ExportRequest{})
	out, err := seed.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}

	versionSIDs := []string{"sales_v2", "research-bot_v1"}
	agentSIDs := []string{"sales", "research-bot"}
	backends := []string{"crm-api", "crm-mcp", "crm_mcp"}
	before := snapshotExport(t, db, versionSIDs, agentSIDs, backends)
	if !strings.Contains(before.Versions["sales_v2"], `hi ${NOT_A_VAR} and $${y}`) {
		t.Fatalf("source prompt literal missing: %s", before.Versions["sales_v2"])
	}

	// Re-open: TRUNCATE everything, then import the exported file.
	db = openTestDB(t)
	path := filepath.Join(t.TempDir(), "sales_v2.seed.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := seed.Load(path)
	if err != nil {
		t.Fatalf("Load: %v\n%s", err, out)
	}
	secrets := map[string]string{
		"CRM_API_X_KEY":           "k1",
		"CRM_MCP_AUTHORIZATION":   "Bearer s1",
		"CRM_MCP_AUTHORIZATION_2": "Bearer s2",
	}
	if len(env) != len(secrets) {
		t.Fatalf("env vars = %v", env)
	}
	if err := seed.Interpolate(loaded, func(k string) (string, bool) { v, ok := secrets[k]; return v, ok }); err != nil {
		t.Fatalf("Interpolate: %v", err)
	}
	if err := seed.Validate(loaded); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	mustApply(t, NewSeedRepository(db), loaded, ApplyOptions{})

	after := snapshotExport(t, db, versionSIDs, agentSIDs, backends)
	for _, pair := range []struct {
		what string
		b, a map[string]string
	}{{"version", before.Versions, after.Versions}, {"agent", before.Agents, after.Agents}, {"backend", before.Backends, after.Backends}} {
		for k, v := range pair.b {
			if pair.a[k] != v {
				t.Errorf("%s %s differs after round trip:\n source: %s\n import: %s", pair.what, k, v, pair.a[k])
			}
		}
	}
}

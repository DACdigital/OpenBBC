package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/seed"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// Seed import (declarative-seed spec §3, "Apply semantics").

// seedArch is the architecture of the test agent: one endpoint tool.
func seedArch() map[string]any {
	return map[string]any{"tools": []any{
		map[string]any{"id": "get_customer", "name": "get_customer"},
	}}
}

const seedArchJSON = `{"tools":[{"id":"get_customer","name":"get_customer"}]}`

// baseSeedSet is one mcp_client backend, one http_endpoint backend, one agent
// wired to the latter and one if_none version attached to the former.
func baseSeedSet() *seed.Set {
	return &seed.Set{
		Format: seed.FormatV1,
		Backends: []seed.Backend{
			{Name: "crm-mcp", Kind: "mcp_client", Config: map[string]any{"url": "https://mcp.example", "transport": "streamable_http"}},
			{Name: "crm-api", Kind: "http_endpoint", Config: map[string]any{"base_url": "https://api.example"}},
		},
		Agents: []seed.Agent{{
			StaticID:         "sales",
			Name:             "Sales assistant",
			Description:      "d",
			Architecture:     seedArch(),
			FlowMapConfig:    map[string]any{"name": "sales"},
			EndpointBackends: map[string]string{"get_customer": "crm-api"},
		}},
		Versions: []seed.Version{{
			StaticID:    "sales_v1",
			Agent:       "sales",
			Prompts:     map[string]any{"main_prompt": "hello ${NOT_A_VAR}"},
			MCPBackends: []seed.MCPRef{{Backend: "crm-mcp", Note: "crm tools"}},
			Deploy:      seed.DeployIfNone,
		}},
	}
}

func mustApply(t *testing.T, r *SeedRepository, s *seed.Set, opts ApplyOptions) []SeedAction {
	t.Helper()
	actions, err := r.Apply(context.Background(), s, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return actions
}

// actionFor returns the action logged for (kind, key), or "" if none.
func actionFor(actions []SeedAction, kind, key string) string {
	for _, a := range actions {
		if a.Kind == kind && a.Key == key {
			return a.Action
		}
	}
	return ""
}

func countActions(actions []SeedAction, action string) int {
	n := 0
	for _, a := range actions {
		if a.Action == action {
			n++
		}
	}
	return n
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func idByStaticID(t *testing.T, db *sql.DB, table, staticID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id::text FROM `+table+` WHERE static_id = $1`, staticID).Scan(&id); err != nil {
		t.Fatalf("%s static_id %s: %v", table, staticID, err)
	}
	return id
}

// rowJSON snapshots a whole row as JSON text.
func rowJSON(t *testing.T, db *sql.DB, table, id string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT row_to_json(x)::text FROM `+table+` x WHERE id = $1::uuid`, id).Scan(&s); err != nil {
		t.Fatalf("rowJSON %s: %v", table, err)
	}
	return s
}

// nothingPersisted asserts that a failed Apply left no rows behind.
func nothingPersisted(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"tool_backends", "agents", "agent_versions", "agent_endpoint_backend", "agent_version_mcp_backend", "agent_version_subagent"} {
		if n := countRows(t, db, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s has %d rows after failed Apply, want 0", table, n)
		}
	}
}

// seedStaticAgentRow raw-inserts a finalized agent with a static ID.
func seedStaticAgentRow(t *testing.T, db *sql.DB, staticID, arch string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`
		INSERT INTO agents (name, static_id, architecture, finalized_at)
		VALUES ($1, $1, $2::jsonb, now()) RETURNING id::text`, staticID, arch).Scan(&id); err != nil {
		t.Fatalf("seedStaticAgentRow: %v", err)
	}
	return id
}

func TestApply_EmptyDB_CreatesAndDeploys(t *testing.T) {
	db := openTestDB(t)
	actions := mustApply(t, NewSeedRepository(db), baseSeedSet(), ApplyOptions{Boot: true})

	if n := countActions(actions, "created"); n != 4 {
		t.Errorf("created actions = %d, want 4 (%v)", n, actions)
	}
	if a := actionFor(actions, "deploy", "sales_v1"); a != "deployed" {
		t.Errorf("deploy action = %q, want deployed (%v)", a, actions)
	}
	if n := countRows(t, db, `SELECT count(*) FROM tool_backends WHERE managed_by_seed`); n != 2 {
		t.Errorf("managed backends = %d, want 2", n)
	}

	agentID := idByStaticID(t, db, "agents", "sales")
	var finalized bool
	var name, arch string
	if err := db.QueryRow(`SELECT finalized_at IS NOT NULL, name, architecture::text FROM agents WHERE id = $1::uuid`, agentID).Scan(&finalized, &name, &arch); err != nil {
		t.Fatal(err)
	}
	if !finalized || name != "Sales assistant" {
		t.Errorf("agent finalized=%v name=%q", finalized, name)
	}
	if n := countRows(t, db, `
		SELECT count(*) FROM agent_endpoint_backend e JOIN tool_backends b ON b.id = e.backend_id
		WHERE e.agent_id = $1::uuid AND e.endpoint_id = 'get_customer' AND b.name = 'crm-api'`, agentID); n != 1 {
		t.Errorf("endpoint wiring rows = %d, want 1", n)
	}

	versionID := idByStaticID(t, db, "agent_versions", "sales_v1")
	var status string
	var parent, flowMap, prompts sql.NullString
	if err := db.QueryRow(`
		SELECT status, parent_version_id::text, flow_map_config::text, prompts::text
		FROM agent_versions WHERE id = $1::uuid`, versionID).Scan(&status, &parent, &flowMap, &prompts); err != nil {
		t.Fatal(err)
	}
	if status != string(types.AgentStatusDeployed) {
		t.Errorf("status = %q, want DEPLOYED", status)
	}
	if parent.Valid {
		t.Errorf("parent_version_id = %q, want NULL (root)", parent.String)
	}
	if !flowMap.Valid || !strings.Contains(flowMap.String, `"sales"`) {
		t.Errorf("flow_map_config = %v, want the agent's", flowMap)
	}
	if !strings.Contains(prompts.String, "hello ${NOT_A_VAR}") {
		t.Errorf("prompts = %q", prompts.String)
	}
	if n := countRows(t, db, `
		SELECT count(*) FROM agent_version_mcp_backend m JOIN tool_backends b ON b.id = m.backend_id
		WHERE m.agent_version_id = $1::uuid AND b.name = 'crm-mcp' AND m.note = 'crm tools'`, versionID); n != 1 {
		t.Errorf("mcp attachment rows = %d, want 1", n)
	}
}

func TestApply_Idempotent_ClientAddedVersions(t *testing.T) {
	db := openTestDB(t)
	r := NewSeedRepository(db)
	vr := NewAgentVersionRepository(db)
	ctx := context.Background()
	mustApply(t, r, baseSeedSet(), ApplyOptions{Boot: true})

	agentID := idByStaticID(t, db, "agents", "sales")
	seeded := idByStaticID(t, db, "agent_versions", "sales_v1")
	for i := 0; i < 2; i++ {
		if _, err := vr.CreateVersionFromPrompts(ctx, seeded, []byte(`{"main_prompt":"client"}`), types.AgentStatusDraft); err != nil {
			t.Fatalf("client fork: %v", err)
		}
	}
	agentBefore := rowJSON(t, db, "agents", agentID)
	versionBefore := rowJSON(t, db, "agent_versions", seeded)

	actions := mustApply(t, r, baseSeedSet(), ApplyOptions{Boot: true})

	for _, c := range []struct{ kind, key, want string }{
		{"backend", "crm-mcp", "updated"},
		{"backend", "crm-api", "updated"},
		{"agent", "sales", "unchanged"},
		{"version", "sales_v1", "unchanged"},
		{"deploy", "sales_v1", "unchanged"},
	} {
		if got := actionFor(actions, c.kind, c.key); got != c.want {
			t.Errorf("%s %s = %q, want %q (%v)", c.kind, c.key, got, c.want, actions)
		}
	}
	if n := mustVersionNum(t, vr, seeded); n != 1 {
		t.Errorf("seeded version number = %d, want 1", n)
	}
	if got := rowJSON(t, db, "agents", agentID); got != agentBefore {
		t.Errorf("agent row changed:\n before %s\n after  %s", agentBefore, got)
	}
	if got := rowJSON(t, db, "agent_versions", seeded); got != versionBefore {
		t.Errorf("seeded version row changed:\n before %s\n after  %s", versionBefore, got)
	}
	if n := countRows(t, db, `SELECT count(*) FROM agents`); n != 1 {
		t.Errorf("agents = %d, want 1", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM agent_versions`); n != 3 {
		t.Errorf("versions = %d, want 3 (seeded + 2 client)", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM tool_backends`); n != 2 {
		t.Errorf("backends = %d, want 2", n)
	}
}

func TestApply_AppendsAtTip(t *testing.T) {
	db := openTestDB(t)
	agentID := seedStaticAgentRow(t, db, "sales", seedArchJSON)
	var parent *string
	var tip string
	for i := 0; i < 7; i++ {
		tip = addVersion(t, db, agentID, parent, types.AgentStatusReady)
		p := tip
		parent = &p
	}

	s := baseSeedSet()
	s.Versions[0].StaticID = "sales_v8"
	s.Versions[0].Deploy = seed.DeployNone
	actions := mustApply(t, NewSeedRepository(db), s, ApplyOptions{})

	if a := actionFor(actions, "agent", "sales"); a != "unchanged" {
		t.Errorf("agent action = %q, want unchanged", a)
	}
	seeded := idByStaticID(t, db, "agent_versions", "sales_v8")
	if p := readParent(t, db, seeded); p != tip {
		t.Errorf("parent = %q, want tip v7 %q", p, tip)
	}
	if n := mustVersionNum(t, NewAgentVersionRepository(db), seeded); n != 8 {
		t.Errorf("version number = %d, want 8", n)
	}
	var flowMap sql.NullString
	if err := db.QueryRow(`SELECT flow_map_config::text FROM agent_versions WHERE id = $1::uuid`, seeded).Scan(&flowMap); err != nil {
		t.Fatal(err)
	}
	if flowMap.Valid {
		t.Errorf("non-root seeded version got flow_map_config %q", flowMap.String)
	}
	// Existing agent: name, description and wiring are left alone.
	if n := countRows(t, db, `SELECT count(*) FROM agent_endpoint_backend`); n != 0 {
		t.Errorf("wiring rows = %d, want 0 (existing agent's wiring untouched)", n)
	}
}

func TestApply_ContentDriftWarns(t *testing.T) {
	db := openTestDB(t)
	r := NewSeedRepository(db)
	mustApply(t, r, baseSeedSet(), ApplyOptions{})
	seeded := idByStaticID(t, db, "agent_versions", "sales_v1")
	before := rowJSON(t, db, "agent_versions", seeded)

	s := baseSeedSet()
	s.Versions[0].Prompts = map[string]any{"main_prompt": "changed"}
	actions := mustApply(t, r, s, ApplyOptions{})

	if a := actionFor(actions, "version", "sales_v1"); a != "warning" {
		t.Errorf("version action = %q, want warning (%v)", a, actions)
	}
	if got := rowJSON(t, db, "agent_versions", seeded); got != before {
		t.Errorf("drifted version row changed:\n before %s\n after  %s", before, got)
	}
}

// Drift is detected on every hashed field, not only prompts; unchanged MCP
// and sub-agent sets (joined back to names/static IDs) hash equal.
func TestApply_ContentDriftOnMCPNote(t *testing.T) {
	db := openTestDB(t)
	r := NewSeedRepository(db)
	mustApply(t, r, baseSeedSet(), ApplyOptions{})

	s := baseSeedSet()
	s.Versions[0].MCPBackends[0].Note = "other note"
	actions := mustApply(t, r, s, ApplyOptions{})
	if a := actionFor(actions, "version", "sales_v1"); a != "warning" {
		t.Errorf("version action = %q, want warning", a)
	}
}

func TestApply_ArchitectureMismatchFails(t *testing.T) {
	db := openTestDB(t)
	seedStaticAgentRow(t, db, "sales", `{"tools":[{"id":"other","name":"other"}]}`)

	s := baseSeedSet()
	s.Agents[0].EndpointBackends = nil
	_, err := NewSeedRepository(db).Apply(context.Background(), s, ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "sales") {
		t.Fatalf("Apply = %v, want an error naming agent sales", err)
	}
	for _, table := range []string{"tool_backends", "agent_versions"} {
		if n := countRows(t, db, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s rows = %d, want 0", table, n)
		}
	}
}

// Key order and whitespace in the stored architecture do not count as a
// mismatch: both sides are compared as canonical JSON.
func TestApply_ArchitectureCanonicalEqual(t *testing.T) {
	db := openTestDB(t)
	seedStaticAgentRow(t, db, "sales", `{"tools": [ {"name": "get_customer", "id": "get_customer"} ]}`)
	s := baseSeedSet()
	s.Versions[0].Deploy = seed.DeployNone
	actions := mustApply(t, NewSeedRepository(db), s, ApplyOptions{})
	if a := actionFor(actions, "agent", "sales"); a != "unchanged" {
		t.Errorf("agent action = %q, want unchanged", a)
	}
}

func TestApply_VersionUnderOtherAgentFails(t *testing.T) {
	db := openTestDB(t)
	other := seedStaticAgentRow(t, db, "other", seedArchJSON)
	if _, err := db.Exec(`
		INSERT INTO agent_versions (agent_id, status, static_id) VALUES ($1::uuid, 'READY', 'sales_v1')`, other); err != nil {
		t.Fatal(err)
	}

	_, err := NewSeedRepository(db).Apply(context.Background(), baseSeedSet(), ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "sales_v1") {
		t.Fatalf("Apply = %v, want an error naming version sales_v1", err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM agents WHERE static_id = 'sales'`); n != 0 {
		t.Errorf("agent sales persisted after failed Apply")
	}
	if n := countRows(t, db, `SELECT count(*) FROM tool_backends`); n != 0 {
		t.Errorf("backends persisted after failed Apply")
	}
}

func TestApply_IfNoneRespectsClientDeploy(t *testing.T) {
	db := openTestDB(t)
	agentID := seedStaticAgentRow(t, db, "sales", seedArchJSON) // get_customer never wired
	client := addVersion(t, db, agentID, nil, types.AgentStatusDeployed)

	s := baseSeedSet()
	s.Agents[0].EndpointBackends = nil
	actions := mustApply(t, NewSeedRepository(db), s, ApplyOptions{Boot: true})

	if a := actionFor(actions, "deploy", "sales_v1"); a != "deploy_skipped" {
		t.Errorf("deploy action = %q, want deploy_skipped (%v)", a, actions)
	}
	if st := versionStatus(t, db, client); st != string(types.AgentStatusDeployed) {
		t.Errorf("client version status = %q, want DEPLOYED", st)
	}
	if st := versionStatus(t, db, idByStaticID(t, db, "agent_versions", "sales_v1")); st != string(types.AgentStatusReady) {
		t.Errorf("seeded version status = %q, want READY", st)
	}
}

func TestApply_ForceRepins(t *testing.T) {
	db := openTestDB(t)
	r := NewSeedRepository(db)
	vr := NewAgentVersionRepository(db)
	ctx := context.Background()
	s := baseSeedSet()
	s.Versions[0].Deploy = seed.DeployForce
	mustApply(t, r, s, ApplyOptions{Boot: true})
	seeded := idByStaticID(t, db, "agent_versions", "sales_v1")

	// The client deploys a version of its own.
	client, err := vr.CreateVersionFromPrompts(ctx, seeded, []byte(`{"main_prompt":"client"}`), types.AgentStatusReady)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vr.Deploy(ctx, client); err != nil {
		t.Fatalf("client deploy: %v", err)
	}

	actions := mustApply(t, r, s, ApplyOptions{Boot: true})
	if a := actionFor(actions, "deploy", "sales_v1"); a != "deployed" {
		t.Errorf("deploy action = %q, want deployed (%v)", a, actions)
	}
	if st := versionStatus(t, db, seeded); st != string(types.AgentStatusDeployed) {
		t.Errorf("seeded status = %q, want DEPLOYED", st)
	}
	if st := versionStatus(t, db, client); st != string(types.AgentStatusReady) {
		t.Errorf("client status = %q, want READY (demoted)", st)
	}
}

func TestApply_ForceWithUnwiredEndpointFails(t *testing.T) {
	db := openTestDB(t)
	s := baseSeedSet()
	s.Agents[0].EndpointBackends = nil
	s.Versions[0].Deploy = seed.DeployForce

	_, err := NewSeedRepository(db).Apply(context.Background(), s, ApplyOptions{Boot: true})
	if !errors.Is(err, types.ErrUnmappedEndpoints) {
		t.Fatalf("Apply = %v, want ErrUnmappedEndpoints", err)
	}
	nothingPersisted(t, db)
}

// force on a version that is already DEPLOYED performs no deploy, so the
// deploy checks do not run: an endpoint the client unwired since does not
// fail the boot.
func TestApply_ForceAlreadyDeployedWithUnmappedEndpoint(t *testing.T) {
	db := openTestDB(t)
	r := NewSeedRepository(db)
	s := baseSeedSet()
	s.Versions[0].Deploy = seed.DeployForce
	mustApply(t, r, s, ApplyOptions{Boot: true})
	if _, err := db.Exec(`DELETE FROM agent_endpoint_backend`); err != nil {
		t.Fatal(err)
	}

	actions := mustApply(t, r, s, ApplyOptions{Boot: true})
	if a := actionFor(actions, "deploy", "sales_v1"); a != "unchanged" {
		t.Errorf("deploy action = %q, want unchanged (%v)", a, actions)
	}
	if st := versionStatus(t, db, idByStaticID(t, db, "agent_versions", "sales_v1")); st != string(types.AgentStatusDeployed) {
		t.Errorf("status = %q, want DEPLOYED", st)
	}
}

// bindingSet declares a caller listed before its sub-agent target.
func bindingSet() *seed.Set {
	s := baseSeedSet()
	s.Agents = append(s.Agents, seed.Agent{
		StaticID:     "research",
		Name:         "Research",
		Architecture: map[string]any{},
	})
	s.Versions[0].SubAgents = []seed.SubAgentRef{{Name: "researcher", Target: "research_v1", Note: "deep dives"}}
	s.Versions = append(s.Versions, seed.Version{
		StaticID: "research_v1",
		Agent:    "research",
		Prompts:  map[string]any{"main_prompt": "research"},
	})
	return s
}

func TestApply_BindingsAnyOrder(t *testing.T) {
	db := openTestDB(t)
	r := NewSeedRepository(db)
	mustApply(t, r, bindingSet(), ApplyOptions{})

	caller := idByStaticID(t, db, "agent_versions", "sales_v1")
	target := idByStaticID(t, db, "agent_versions", "research_v1")
	if n := countRows(t, db, `
		SELECT count(*) FROM agent_version_subagent
		WHERE caller_version_id = $1::uuid AND target_version_id = $2::uuid
		  AND name = 'researcher' AND note = 'deep dives'`, caller, target); n != 1 {
		t.Fatalf("binding rows = %d, want 1", n)
	}

	// Re-applying hashes the DB bindings (joined to the target static ID)
	// equal to the file's.
	actions := mustApply(t, r, bindingSet(), ApplyOptions{})
	if a := actionFor(actions, "version", "sales_v1"); a != "unchanged" {
		t.Errorf("re-apply caller action = %q, want unchanged", a)
	}
}

// The binding pass runs the whole-graph cycle check over the DB (including
// rows inserted earlier in the same transaction). Validate would reject this
// file; Apply's DB check is the backstop.
func TestApply_BindingCycleWithDBFails(t *testing.T) {
	db := openTestDB(t)
	s := bindingSet()
	s.Versions[1].SubAgents = []seed.SubAgentRef{{Name: "back", Target: "sales_v1"}}

	_, err := NewSeedRepository(db).Apply(context.Background(), s, ApplyOptions{})
	if !errors.Is(err, types.ErrTopologyCycle) {
		t.Fatalf("Apply = %v, want ErrTopologyCycle", err)
	}
	nothingPersisted(t, db)
}

func TestApply_AgentToolCollisionFails(t *testing.T) {
	db := openTestDB(t)
	s := baseSeedSet()
	s.Agents[0].Architecture = map[string]any{"tools": []any{
		map[string]any{"id": "get_customer", "name": "agent"},
	}}
	s.Versions[0].AgentToolEnabled = true

	_, err := NewSeedRepository(db).Apply(context.Background(), s, ApplyOptions{})
	if !errors.Is(err, types.ErrToolNameCollision) {
		t.Fatalf("Apply = %v, want ErrToolNameCollision", err)
	}
	nothingPersisted(t, db)
}

func TestApply_BackendKindMismatchFails(t *testing.T) {
	db := openTestDB(t)
	seedHTTPBackend(t, db, "crm-mcp")

	_, err := NewSeedRepository(db).Apply(context.Background(), baseSeedSet(), ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "crm-mcp") {
		t.Fatalf("Apply = %v, want an error naming backend crm-mcp", err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM tool_backends WHERE kind = 'http_endpoint' AND name = 'crm-mcp' AND NOT managed_by_seed`); n != 1 {
		t.Errorf("existing backend changed")
	}
}

func backendState(t *testing.T, db *sql.DB, name string) (config string, managed bool) {
	t.Helper()
	if err := db.QueryRow(`SELECT config::text, managed_by_seed FROM tool_backends WHERE name = $1`, name).Scan(&config, &managed); err != nil {
		t.Fatalf("backendState %s: %v", name, err)
	}
	return config, managed
}

func TestApply_BackendTakeoverAndUnflag(t *testing.T) {
	db := openTestDB(t)
	r := NewSeedRepository(db)
	seedHTTPBackend(t, db, "crm-api") // BO-created, base_url https://test.example

	actions := mustApply(t, r, baseSeedSet(), ApplyOptions{})
	if a := actionFor(actions, "backend", "crm-api"); a != "updated" {
		t.Errorf("takeover action = %q, want updated", a)
	}
	cfg, managed := backendState(t, db, "crm-api")
	if !managed || !strings.Contains(cfg, "https://api.example") {
		t.Fatalf("after takeover config=%s managed=%v", cfg, managed)
	}

	only := &seed.Set{Format: seed.FormatV1, Backends: []seed.Backend{
		{Name: "other", Kind: "http_endpoint", Config: map[string]any{"base_url": "https://o.example"}},
	}}
	// A standalone (non-boot) apply never clears flags.
	mustApply(t, r, only, ApplyOptions{})
	if _, managed := backendState(t, db, "crm-api"); !managed {
		t.Fatalf("non-boot apply cleared managed_by_seed")
	}
	// A boot apply clears flags on backends the set no longer declares, and
	// leaves the rows in place.
	mustApply(t, r, only, ApplyOptions{Boot: true})
	for _, name := range []string{"crm-api", "crm-mcp"} {
		if _, managed := backendState(t, db, name); managed {
			t.Errorf("%s still managed after boot apply without it", name)
		}
	}
	if _, managed := backendState(t, db, "other"); !managed {
		t.Errorf("declared backend lost managed_by_seed")
	}
}

func TestClearManagedFlags(t *testing.T) {
	db := openTestDB(t)
	r := NewSeedRepository(db)
	mustApply(t, r, baseSeedSet(), ApplyOptions{})
	if err := r.ClearManagedFlags(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM tool_backends WHERE managed_by_seed`); n != 0 {
		t.Errorf("managed backends = %d, want 0", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM tool_backends`); n != 2 {
		t.Errorf("backends = %d, want 2 (rows kept)", n)
	}
}

func TestApply_RollbackOnLateFailure(t *testing.T) {
	db := openTestDB(t)
	seedHTTPBackend(t, db, "crm-api")
	cfgBefore, _ := backendState(t, db, "crm-api")

	s := baseSeedSet()
	s.Agents[0].Architecture = map[string]any{"tools": []any{
		map[string]any{"id": "get_customer", "name": "get_customer"},
		map[string]any{"id": "list_orders", "name": "list_orders"}, // never wired
	}}
	s.Versions[0].Deploy = seed.DeployForce

	_, err := NewSeedRepository(db).Apply(context.Background(), s, ApplyOptions{Boot: true})
	if !errors.Is(err, types.ErrUnmappedEndpoints) {
		t.Fatalf("Apply = %v, want ErrUnmappedEndpoints", err)
	}
	cfg, managed := backendState(t, db, "crm-api")
	if cfg != cfgBefore || managed {
		t.Errorf("backend changed by failed Apply: config=%s managed=%v (before %s)", cfg, managed, cfgBefore)
	}
	for _, table := range []string{"agents", "agent_versions"} {
		if n := countRows(t, db, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s rows = %d, want 0", table, n)
		}
	}
	if n := countRows(t, db, `SELECT count(*) FROM tool_backends`); n != 1 {
		t.Errorf("backends = %d, want 1 (crm-mcp insert rolled back)", n)
	}
}

// jsonb prints numbers in plain form (1e-07 → 0.0000001, 1e+21 →
// 1000000000000000000000) while Go marshals floats in exponent form; the
// comparisons must not see that as a difference.
func numericSeedSet() *seed.Set {
	s := baseSeedSet()
	s.Agents[0].Architecture = map[string]any{"tools": []any{
		map[string]any{"id": "get_customer", "name": "get_customer", "minimum": 1e-07, "maximum": 1e+21},
	}}
	s.Versions[0].Prompts = map[string]any{"main_prompt": "p", "temperature": 1e-07, "budget": 1e+21}
	return s
}

func TestApply_ArchitectureNumbersReapply(t *testing.T) {
	db := openTestDB(t)
	r := NewSeedRepository(db)
	mustApply(t, r, numericSeedSet(), ApplyOptions{Boot: true})
	actions := mustApply(t, r, numericSeedSet(), ApplyOptions{Boot: true})
	if a := actionFor(actions, "agent", "sales"); a != "unchanged" {
		t.Errorf("agent action = %q, want unchanged", a)
	}
}

func TestApply_PromptNumbersNoFalseDrift(t *testing.T) {
	db := openTestDB(t)
	r := NewSeedRepository(db)
	promptsOnly := func() *seed.Set {
		s := numericSeedSet()
		s.Agents[0].Architecture = seedArch()
		return s
	}
	mustApply(t, r, promptsOnly(), ApplyOptions{Boot: true})
	actions := mustApply(t, r, promptsOnly(), ApplyOptions{Boot: true})
	if a := actionFor(actions, "version", "sales_v1"); a != "unchanged" {
		t.Errorf("version action = %q, want unchanged (%v)", a, actions)
	}
}

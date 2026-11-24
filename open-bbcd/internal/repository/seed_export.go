package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lib/pq"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/seed"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/staticid"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// ExportRequest is the optional body of POST /agent_versions/{id}/export.
// StaticIDs maps an agent or version UUID to the static ID to assign it (used
// only for rows without one). Deploy is the requested version's marker
// (default if_none).
type ExportRequest struct {
	StaticIDs map[string]string `json:"static_ids"`
	Deploy    seed.DeployMode   `json:"deploy"`
}

// Export builds the openbbc-seed-v1 set for versionID and its closure
// (declarative-seed spec §5) in one transaction: status precondition,
// closure, static-ID assignment (persisted, write-once), backend secrets as
// placeholders and literal escaping. It returns the set and the sorted
// placeholder variable names.
func (r *SeedRepository) Export(ctx context.Context, versionID string, req ExportRequest) (*seed.Set, []string, error) {
	deploy := req.Deploy
	switch deploy {
	case "":
		deploy = seed.DeployIfNone
	case seed.DeployNone, seed.DeployIfNone, seed.DeployForce:
	default:
		return nil, nil, fmt.Errorf("invalid deploy %q", req.Deploy)
	}
	supplied := make(map[string]string, len(req.StaticIDs))
	for id, sid := range req.StaticIDs {
		if err := staticid.Validate(sid); err != nil {
			return nil, nil, err
		}
		supplied[strings.ToLower(id)] = sid
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	ex := &seedExport{ctx: ctx, tx: tx}
	if err := ex.checkExportable(versionID); err != nil {
		return nil, nil, err
	}
	if err := ex.closure(versionID); err != nil {
		return nil, nil, err
	}
	if err := ex.lockAndLoad(); err != nil {
		return nil, nil, err
	}
	// Re-check under the row locks: the status read above was unlocked.
	if err := ex.checkExportable(versionID); err != nil {
		return nil, nil, err
	}
	if err := ex.assignStaticIDs(supplied); err != nil {
		return nil, nil, err
	}
	set, envVars, err := ex.build(versionID, deploy)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return set, envVars, nil
}

// ExportPreviewRow is one agent or version of an export closure as the
// backoffice export modal shows it. StaticID is the existing ID (Fixed, so
// read-only); otherwise Default is the ID Export would assign.
type ExportPreviewRow struct {
	Kind     string // "agent" | "version"
	ID       string
	Label    string
	StaticID string
	Fixed    bool
	Default  string
}

// ExportPreview lists the closure of versionID (agents first, then versions,
// requested version first among them) with the static IDs Export would use,
// without writing anything. It applies the same status precondition and the
// same default/collision logic as Export.
func (r *SeedRepository) ExportPreview(ctx context.Context, versionID string) ([]ExportPreviewRow, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	ex := &seedExport{ctx: ctx, tx: tx}
	if err := ex.checkExportable(versionID); err != nil {
		return nil, err
	}
	if err := ex.closure(versionID); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, name, COALESCE(static_id, '') FROM agents
		WHERE id = ANY($1::uuid[])`, pq.Array(ex.agentOrder))
	if err != nil {
		return nil, fmt.Errorf("read agents: %w", err)
	}
	for rows.Next() {
		var id string
		var a exportAgent
		if err := rows.Scan(&id, &a.name, &a.staticID); err != nil {
			rows.Close()
			return nil, err
		}
		a.id = id
		*ex.agents[id] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `
		SELECT id::text, status, COALESCE(static_id, '') FROM agent_versions
		WHERE id = ANY($1::uuid[])`, pq.Array(ex.versionOrder))
	if err != nil {
		return nil, fmt.Errorf("read versions: %w", err)
	}
	status := map[string]string{}
	for rows.Next() {
		var id, st, sid string
		if err := rows.Scan(&id, &st, &sid); err != nil {
			rows.Close()
			return nil, err
		}
		status[id] = st
		ex.versions[id].staticID = sid
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []ExportPreviewRow
	reservedAgents := map[string]bool{}
	effective := map[string]string{} // agent id → static ID (existing or default)
	for _, id := range ex.agentOrder {
		a := ex.agents[id]
		row := ExportPreviewRow{Kind: "agent", ID: id, Label: a.name, StaticID: a.staticID, Fixed: a.staticID != ""}
		effective[id] = a.staticID
		if !row.Fixed {
			if row.Default, err = ex.freeStaticID("agents", staticid.AgentDefault(a.name), reservedAgents); err != nil {
				return nil, err
			}
			reservedAgents[row.Default] = true
			effective[id] = row.Default
		}
		out = append(out, row)
	}
	reservedVersions := map[string]bool{}
	for _, id := range ex.versionOrder {
		v := ex.versions[id]
		n, err := ex.versionNum(id)
		if err != nil {
			return nil, fmt.Errorf("version %s: number: %w", id, err)
		}
		row := ExportPreviewRow{
			Kind: "version", ID: id, StaticID: v.staticID, Fixed: v.staticID != "",
			Label: fmt.Sprintf("%s · v%d · %s", ex.agents[v.agentID].name, n, status[id]),
		}
		if !row.Fixed {
			base := staticid.VersionDefault(effective[v.agentID], n)
			if row.Default, err = ex.freeStaticID("agent_versions", base, reservedVersions); err != nil {
				return nil, err
			}
			reservedVersions[row.Default] = true
		}
		out = append(out, row)
	}
	return out, nil
}

// seedExport is the state of one Export run.
type seedExport struct {
	ctx context.Context
	tx  *sql.Tx

	versionOrder []string // closure, requested version first (BFS by binding name)
	agentOrder   []string // agents by first appearance in versionOrder
	versions     map[string]*exportVersion
	agents       map[string]*exportAgent
}

type exportAgent struct {
	id, name, description, staticID string
	architecture                    map[string]any
	flowMap                         map[string]any    // root version's; nil when none
	endpoints                       map[string]string // endpoint id → backend name
}

type exportVersion struct {
	id, agentID, staticID string
	prompts               map[string]any
	agentToolEnabled      bool
	mcp                   []seed.MCPRef      // backend names, sorted
	subs                  []exportSubBinding // sorted by name
}

type exportSubBinding struct{ name, targetID, note string }

func (ex *seedExport) checkExportable(versionID string) error {
	var status string
	err := ex.tx.QueryRowContext(ex.ctx,
		`SELECT status FROM agent_versions WHERE id = $1::uuid`, versionID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	switch types.AgentStatus(status) {
	case types.AgentStatusReady, types.AgentStatusTraining, types.AgentStatusDeployed:
		return nil
	}
	return fmt.Errorf("%w: status %s", types.ErrVersionNotExportable, status)
}

// closure walks sub-agent bindings breadth-first from versionID, collecting
// every version and its agent. Bindings of an exportable version cannot
// change (immutable statuses), so the walk is stable.
func (ex *seedExport) closure(versionID string) error {
	ex.versions = map[string]*exportVersion{}
	ex.agents = map[string]*exportAgent{}
	queue := []string{versionID}
	ex.versions[versionID] = &exportVersion{id: versionID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		ex.versionOrder = append(ex.versionOrder, id)

		rows, err := ex.tx.QueryContext(ex.ctx, `
			SELECT name, target_version_id::text, note FROM agent_version_subagent
			WHERE caller_version_id = $1::uuid ORDER BY name`, id)
		if err != nil {
			return fmt.Errorf("read bindings: %w", err)
		}
		v := ex.versions[id]
		for rows.Next() {
			var b exportSubBinding
			if err := rows.Scan(&b.name, &b.targetID, &b.note); err != nil {
				rows.Close()
				return err
			}
			v.subs = append(v.subs, b)
			if _, seen := ex.versions[b.targetID]; !seen {
				ex.versions[b.targetID] = &exportVersion{id: b.targetID}
				queue = append(queue, b.targetID)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	ids := ex.versionOrder
	rows, err := ex.tx.QueryContext(ex.ctx,
		`SELECT id::text, agent_id::text FROM agent_versions WHERE id = ANY($1::uuid[])`, pq.Array(ids))
	if err != nil {
		return fmt.Errorf("read version agents: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, agentID string
		if err := rows.Scan(&id, &agentID); err != nil {
			return err
		}
		ex.versions[id].agentID = agentID
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ex.versionOrder {
		a := ex.versions[id].agentID
		if _, ok := ex.agents[a]; !ok {
			ex.agents[a] = &exportAgent{id: a}
			ex.agentOrder = append(ex.agentOrder, a)
		}
	}
	return nil
}

// lockAndLoad locks the closure's agent rows, then its version rows (both
// ascending id, the global lock order), and reads their exported content.
func (ex *seedExport) lockAndLoad() error {
	agentIDs := append([]string(nil), ex.agentOrder...)
	rows, err := ex.tx.QueryContext(ex.ctx, `
		SELECT id::text, name, COALESCE(description, ''), architecture, COALESCE(static_id, '')
		FROM agents WHERE id = ANY($1::uuid[]) ORDER BY id FOR UPDATE`, pq.Array(agentIDs))
	if err != nil {
		return fmt.Errorf("lock agents: %w", err)
	}
	for rows.Next() {
		var id, name, desc, sid string
		var arch []byte
		if err := rows.Scan(&id, &name, &desc, &arch, &sid); err != nil {
			rows.Close()
			return err
		}
		a := ex.agents[id]
		a.name, a.description, a.staticID = name, desc, sid
		if a.architecture, err = decodeJSONObject(arch); err != nil {
			rows.Close()
			return fmt.Errorf("agent %s: architecture: %w", id, err)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = ex.tx.QueryContext(ex.ctx, `
		SELECT id::text, prompts, agent_tool_enabled, COALESCE(static_id, '')
		FROM agent_versions WHERE id = ANY($1::uuid[]) ORDER BY id FOR UPDATE`, pq.Array(ex.versionOrder))
	if err != nil {
		return fmt.Errorf("lock versions: %w", err)
	}
	for rows.Next() {
		var id, sid string
		var prompts []byte
		var enabled bool
		if err := rows.Scan(&id, &prompts, &enabled, &sid); err != nil {
			rows.Close()
			return err
		}
		v := ex.versions[id]
		v.agentToolEnabled, v.staticID = enabled, sid
		if v.prompts, err = decodeJSONObject(prompts); err != nil {
			rows.Close()
			return fmt.Errorf("version %s: prompts: %w", id, err)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, id := range ex.agentOrder {
		if err := ex.loadAgentExtras(ex.agents[id]); err != nil {
			return err
		}
	}
	for _, id := range ex.versionOrder {
		if err := ex.loadMCP(ex.versions[id]); err != nil {
			return err
		}
	}
	return nil
}

// loadAgentExtras reads the root version's flow_map_config and the endpoint
// wiring of a.
func (ex *seedExport) loadAgentExtras(a *exportAgent) error {
	var flowMap []byte
	err := ex.tx.QueryRowContext(ex.ctx, `
		SELECT flow_map_config FROM agent_versions
		WHERE agent_id = $1::uuid AND parent_version_id IS NULL`, a.id).Scan(&flowMap)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("agent %s: root version: %w", a.id, err)
	}
	if a.flowMap, err = decodeJSONObject(flowMap); err != nil {
		return fmt.Errorf("agent %s: flow_map_config: %w", a.id, err)
	}

	rows, err := ex.tx.QueryContext(ex.ctx, `
		SELECT e.endpoint_id, b.name FROM agent_endpoint_backend e
		JOIN tool_backends b ON b.id = e.backend_id
		WHERE e.agent_id = $1::uuid`, a.id)
	if err != nil {
		return fmt.Errorf("agent %s: endpoint wiring: %w", a.id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var ep, backend string
		if err := rows.Scan(&ep, &backend); err != nil {
			return err
		}
		if a.endpoints == nil {
			a.endpoints = map[string]string{}
		}
		a.endpoints[ep] = backend
	}
	return rows.Err()
}

func (ex *seedExport) loadMCP(v *exportVersion) error {
	rows, err := ex.tx.QueryContext(ex.ctx, `
		SELECT b.name, m.note FROM agent_version_mcp_backend m
		JOIN tool_backends b ON b.id = m.backend_id
		WHERE m.agent_version_id = $1::uuid
		ORDER BY b.name`, v.id)
	if err != nil {
		return fmt.Errorf("version %s: mcp backends: %w", v.id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var m seed.MCPRef
		if err := rows.Scan(&m.Backend, &m.Note); err != nil {
			return err
		}
		v.mcp = append(v.mcp, m)
	}
	return rows.Err()
}

// assignStaticIDs gives every closure row without a static ID the supplied
// value or a default (agents first, since version defaults derive from the
// agent's), and persists it write-once. A supplied value that differs from an
// existing ID, or is already taken, is ErrStaticIDConflict.
func (ex *seedExport) assignStaticIDs(supplied map[string]string) error {
	for _, id := range ex.agentOrder {
		a := ex.agents[id]
		sid, err := ex.assignOne("agents", a.id, a.staticID, supplied[a.id], func() string {
			return staticid.AgentDefault(a.name)
		})
		if err != nil {
			return fmt.Errorf("agent %s: %w", a.id, err)
		}
		a.staticID = sid
	}
	for _, id := range ex.versionOrder {
		v := ex.versions[id]
		var genErr error
		sid, err := ex.assignOne("agent_versions", v.id, v.staticID, supplied[v.id], func() string {
			var n int
			if n, genErr = ex.versionNum(v.id); genErr != nil {
				return ""
			}
			return staticid.VersionDefault(ex.agents[v.agentID].staticID, n)
		})
		if genErr != nil {
			return fmt.Errorf("version %s: number: %w", v.id, genErr)
		}
		if err != nil {
			return fmt.Errorf("version %s: %w", v.id, err)
		}
		v.staticID = sid
	}
	return nil
}

// assignOne returns the static ID of the row id in table. table is a fixed
// identifier ("agents" or "agent_versions"), never user input.
func (ex *seedExport) assignOne(table, id, existing, want string, def func() string) (string, error) {
	if existing != "" {
		if want != "" && want != existing {
			return "", fmt.Errorf("%w: has %q, %q requested", types.ErrStaticIDConflict, existing, want)
		}
		return existing, nil
	}

	sid := want
	if sid != "" {
		used, err := ex.staticIDTaken(table, sid)
		if err != nil {
			return "", err
		}
		if used {
			return "", fmt.Errorf("%w: %q", types.ErrStaticIDConflict, sid)
		}
	} else {
		var err error
		if sid, err = ex.freeStaticID(table, def(), nil); err != nil {
			return "", err
		}
	}

	res, err := ex.tx.ExecContext(ex.ctx,
		`UPDATE `+table+` SET static_id = $2 WHERE id = $1::uuid AND static_id IS NULL`, id, sid)
	if isUniqueViolation(err) {
		return "", fmt.Errorf("%w: %q", types.ErrStaticIDConflict, sid)
	}
	if err != nil {
		return "", fmt.Errorf("persist static_id: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", fmt.Errorf("%w: static_id set concurrently", types.ErrStaticIDConflict)
	}
	return sid, nil
}

// staticIDTaken reports whether sid is already used in table (a fixed
// identifier, never user input).
func (ex *seedExport) staticIDTaken(table, sid string) (bool, error) {
	var one int
	err := ex.tx.QueryRowContext(ex.ctx,
		`SELECT 1 FROM `+table+` WHERE static_id = $1`, sid).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// freeStaticID returns base, or base with the first "-k" suffix, that is
// neither used in table nor in reserved (IDs a preview has already handed out).
func (ex *seedExport) freeStaticID(table, base string, reserved map[string]bool) (string, error) {
	if base == "" {
		return "", errors.New("no default static ID")
	}
	for k := 1; ; k++ {
		sid := staticid.WithSuffix(base, k)
		if reserved[sid] {
			continue
		}
		used, err := ex.staticIDTaken(table, sid)
		if err != nil {
			return "", err
		}
		if !used {
			return sid, nil
		}
	}
}

// versionNum is the version's position in its agent's chain (root = 1).
func (ex *seedExport) versionNum(id string) (int, error) {
	var n int
	err := ex.tx.QueryRowContext(ex.ctx, versionChainCTE+`
		SELECT num FROM chain WHERE id = $1::uuid`, id).Scan(&n)
	return n, err
}

// build assembles the seed set: agents and versions in closure order, the
// backends they reference with default_headers turned into placeholders and
// every other config string escaped.
func (ex *seedExport) build(versionID string, deploy seed.DeployMode) (*seed.Set, []string, error) {
	set := &seed.Set{Format: seed.FormatV1}
	backendNames := map[string]bool{}

	for _, id := range ex.agentOrder {
		a := ex.agents[id]
		for _, b := range a.endpoints {
			backendNames[b] = true
		}
		set.Agents = append(set.Agents, seed.Agent{
			StaticID:         a.staticID,
			Name:             a.name,
			Description:      a.description,
			Architecture:     a.architecture,
			FlowMapConfig:    a.flowMap,
			EndpointBackends: a.endpoints,
		})
	}
	for _, id := range ex.versionOrder {
		v := ex.versions[id]
		mode := seed.DeployNone
		if id == versionID {
			mode = deploy
		}
		var subs []seed.SubAgentRef
		for _, b := range v.subs {
			subs = append(subs, seed.SubAgentRef{Name: b.name, Target: ex.versions[b.targetID].staticID, Note: b.note})
		}
		for _, m := range v.mcp {
			backendNames[m.Backend] = true
		}
		set.Versions = append(set.Versions, seed.Version{
			StaticID:         v.staticID,
			Agent:            ex.agents[v.agentID].staticID,
			Prompts:          v.prompts,
			AgentToolEnabled: v.agentToolEnabled,
			MCPBackends:      v.mcp,
			SubAgents:        subs,
			Deploy:           mode,
		})
	}

	names := make([]string, 0, len(backendNames))
	for n := range backendNames {
		names = append(names, n)
	}
	sort.Strings(names)
	rows, err := ex.tx.QueryContext(ex.ctx, `
		SELECT name, kind, config FROM tool_backends
		WHERE name = ANY($1::text[]) ORDER BY name`, pq.Array(names))
	if err != nil {
		return nil, nil, fmt.Errorf("read backends: %w", err)
	}
	defer rows.Close()
	used := map[string]bool{}
	var envVars []string
	for rows.Next() {
		var b seed.Backend
		var cfg []byte
		if err := rows.Scan(&b.Name, &b.Kind, &cfg); err != nil {
			return nil, nil, err
		}
		if b.Config, err = decodeJSONObject(cfg); err != nil {
			return nil, nil, fmt.Errorf("backend %s: config: %w", b.Name, err)
		}
		if b.Config == nil {
			b.Config = map[string]any{}
		}
		envVars = append(envVars, exportBackendConfig(b.Name, b.Config, used)...)
		set.Backends = append(set.Backends, b)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	sort.Strings(envVars)
	return set, envVars, nil
}

// exportBackendConfig rewrites cfg in place: every default_headers value
// becomes a ${NAME} placeholder (names unique across the export via used, in
// header order) and every other string is escaped for interpolation. It
// returns the placeholder names it created.
func exportBackendConfig(backend string, cfg map[string]any, used map[string]bool) []string {
	var names []string
	for k, v := range cfg {
		if k == "default_headers" {
			if headers, ok := v.(map[string]any); ok {
				keys := make([]string, 0, len(headers))
				for h := range headers {
					keys = append(keys, h)
				}
				sort.Strings(keys)
				for _, h := range keys {
					name := placeholderName(backend, h, used)
					headers[h] = "${" + name + "}"
					names = append(names, name)
				}
				continue
			}
		}
		cfg[k] = escapeStrings(v)
	}
	return names
}

var nonAlnumRe = regexp.MustCompile(`[^A-Za-z0-9]+`)

// placeholderName is upper-snake(<backend>_<header>), suffixed _2, _3, … when
// already used. A name that would start with a digit is prefixed with "_" so
// it stays a valid ${VAR} name.
func placeholderName(backend, header string, used map[string]bool) string {
	base := strings.Trim(strings.ToUpper(nonAlnumRe.ReplaceAllString(backend+"_"+header, "_")), "_")
	if base == "" || (base[0] >= '0' && base[0] <= '9') {
		base = "_" + base
	}
	name := base
	for k := 2; used[name]; k++ {
		name = base + "_" + strconv.Itoa(k)
	}
	used[name] = true
	return name
}

func escapeStrings(v any) any {
	switch t := v.(type) {
	case string:
		return seed.EscapeLiteral(t)
	case map[string]any:
		for k, val := range t {
			t[k] = escapeStrings(val)
		}
	case []any:
		for i, val := range t {
			t[i] = escapeStrings(val)
		}
	}
	return v
}

// decodeJSONObject decodes a JSON object (nil for SQL NULL / JSON null).
// Integers that fit int64 stay int64, so YAML output keeps their exact value;
// other numbers become float64.
func decodeJSONObject(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	return fromJSONNumbers(m).(map[string]any), nil
}

func fromJSONNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = fromJSONNumbers(val)
		}
	case []any:
		for i, val := range t {
			t[i] = fromJSONNumbers(val)
		}
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		f, _ := t.Float64()
		return f
	}
	return v
}

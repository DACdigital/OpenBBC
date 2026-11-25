package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/lib/pq"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/seed"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// SeedRepository applies an openbbc-seed-v1 seed set to the database
// (declarative-seed spec §3, "Apply semantics").
type SeedRepository struct{ db *sql.DB }

func NewSeedRepository(db *sql.DB) *SeedRepository { return &SeedRepository{db: db} }

// ApplyOptions tunes Apply. Boot enables clearing managed_by_seed on backends
// the set no longer declares; only the boot import (SEED_PATH) sets it, so a
// standalone `seed apply` of a partial file cannot unlock the boot seed's
// backends.
type ApplyOptions struct{ Boot bool }

// Seed action kinds and actions (one log line per resource).
const (
	SeedKindBackend = "backend"
	SeedKindAgent   = "agent"
	SeedKindVersion = "version"
	SeedKindDeploy  = "deploy"

	SeedActionCreated       = "created"
	SeedActionUpdated       = "updated"
	SeedActionUnchanged     = "unchanged"
	SeedActionWarning       = "warning"
	SeedActionDeployed      = "deployed"
	SeedActionDeploySkipped = "deploy_skipped"
)

// SeedAction is what Apply did to one resource. Key is the backend name or
// the agent/version static ID.
type SeedAction struct{ Kind, Key, Action, Detail string }

// Apply imports s in one transaction, in spec §3 order: backends → agents →
// versions (pass 1) → sub-agent bindings (pass 2) → deploys. Any error rolls
// everything back. s is expected to have passed seed.Validate; references
// are resolved inside the set only.
//
// Static IDs are written only on INSERT (write-once). Existing agents and
// versions are never written to.
func (r *SeedRepository) Apply(ctx context.Context, s *seed.Set, opts ApplyOptions) ([]SeedAction, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	ap := &seedApply{ctx: ctx, tx: tx, set: s}
	steps := []func() error{
		func() error { return ap.backends(opts.Boot) },
		ap.agents,
		ap.versions,
		ap.bindings,
		ap.deploys,
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ap.actions, nil
}

// ClearManagedFlags unflags every seed-managed backend. Boot path when
// SEED_PATH is unset: switching seeding off unlocks the backends.
func (r *SeedRepository) ClearManagedFlags(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE tool_backends SET managed_by_seed = false, updated_at = now() WHERE managed_by_seed`)
	return err
}

// seedApply is the state of one Apply run.
type seedApply struct {
	ctx     context.Context
	tx      *sql.Tx
	set     *seed.Set
	actions []SeedAction

	backendIDs map[string]string // backend name → id
	agentIDs   map[string]string // agent static_id → id
	versionIDs map[string]string // version static_id → id
	created    map[string]bool   // version static_ids inserted by this run
}

func (ap *seedApply) log(kind, key, action, detail string) {
	ap.actions = append(ap.actions, SeedAction{Kind: kind, Key: key, Action: action, Detail: detail})
}

// backends upserts every backend by name: the file wins on every run. kind is
// set on insert only and a differing kind is an error. On boot, backends the
// set no longer declares are unflagged (the rows stay).
func (ap *seedApply) backends(boot bool) error {
	ap.backendIDs = map[string]string{}
	names := make([]string, 0, len(ap.set.Backends))
	for _, b := range ap.set.Backends {
		names = append(names, b.Name)
		cfg := b.Config
		if cfg == nil {
			cfg = map[string]any{}
		}
		cfgJSON, err := json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("backend %s: config: %w", b.Name, err)
		}

		var id, kind string
		err = ap.tx.QueryRowContext(ap.ctx,
			`SELECT id::text, kind FROM tool_backends WHERE name = $1 FOR UPDATE`, b.Name,
		).Scan(&id, &kind)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if err := ap.tx.QueryRowContext(ap.ctx, `
				INSERT INTO tool_backends (name, kind, config, managed_by_seed)
				VALUES ($1, $2, $3::jsonb, true) RETURNING id::text`, b.Name, b.Kind, cfgJSON,
			).Scan(&id); err != nil {
				return fmt.Errorf("backend %s: insert: %w", b.Name, err)
			}
			ap.log(SeedKindBackend, b.Name, SeedActionCreated, "")
		case err != nil:
			return fmt.Errorf("backend %s: %w", b.Name, err)
		default:
			if kind != b.Kind {
				return fmt.Errorf("backend %s: kind %s in the seed differs from existing kind %s", b.Name, b.Kind, kind)
			}
			// updated_at moves only when something actually changes.
			if _, err := ap.tx.ExecContext(ap.ctx, `
				UPDATE tool_backends
				SET updated_at = CASE WHEN config IS DISTINCT FROM $2::jsonb OR NOT managed_by_seed
				                      THEN now() ELSE updated_at END,
				    config = $2::jsonb, managed_by_seed = true
				WHERE id = $1::uuid`, id, cfgJSON); err != nil {
				return fmt.Errorf("backend %s: update: %w", b.Name, err)
			}
			ap.log(SeedKindBackend, b.Name, SeedActionUpdated, "")
		}
		ap.backendIDs[b.Name] = id
	}

	if boot {
		if _, err := ap.tx.ExecContext(ap.ctx, `
			UPDATE tool_backends SET managed_by_seed = false, updated_at = now()
			WHERE managed_by_seed AND NOT (name = ANY($1::text[]))`, pq.Array(names)); err != nil {
			return fmt.Errorf("unflag undeclared backends: %w", err)
		}
	}
	return nil
}

func (ap *seedApply) backendID(name string) (string, error) {
	id, ok := ap.backendIDs[name]
	if !ok {
		return "", fmt.Errorf("backend %s is not declared in the seed set", name)
	}
	return id, nil
}

// agents locks the existing agents (FOR UPDATE, ascending id — before any
// version row is touched), checks their architecture against the file and
// creates the missing ones with their endpoint wiring. Existing agents are
// never written to.
func (ap *seedApply) agents() error {
	ap.agentIDs = map[string]string{}
	staticIDs := make([]string, 0, len(ap.set.Agents))
	for _, a := range ap.set.Agents {
		staticIDs = append(staticIDs, a.StaticID)
	}

	rows, err := ap.tx.QueryContext(ap.ctx, `
		SELECT id::text, static_id, architecture FROM agents
		WHERE static_id = ANY($1::text[])
		ORDER BY id
		FOR UPDATE`, pq.Array(staticIDs))
	if err != nil {
		return fmt.Errorf("lock agents: %w", err)
	}
	existingArch := map[string][]byte{}
	for rows.Next() {
		var id, sid string
		var arch []byte
		if err := rows.Scan(&id, &sid, &arch); err != nil {
			rows.Close()
			return err
		}
		ap.agentIDs[sid] = id
		existingArch[sid] = arch
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, a := range ap.set.Agents {
		arch := a.Architecture
		if arch == nil {
			arch = map[string]any{}
		}
		fileArch, err := seed.CanonicalJSON(arch)
		if err != nil {
			return fmt.Errorf("agent %s: architecture: %w", a.StaticID, err)
		}

		if dbArch, ok := existingArch[a.StaticID]; ok {
			canonDB, err := seed.CanonicalJSON(json.RawMessage(dbArch))
			if err != nil {
				return fmt.Errorf("agent %s: stored architecture: %w", a.StaticID, err)
			}
			if !bytes.Equal(canonDB, fileArch) {
				return fmt.Errorf("agent %s: architecture in the seed differs from the existing agent's (architecture is frozen; use a new static_id)", a.StaticID)
			}
			ap.log(SeedKindAgent, a.StaticID, SeedActionUnchanged, "")
			continue
		}

		var id string
		if err := ap.tx.QueryRowContext(ap.ctx, `
			INSERT INTO agents (name, description, architecture, finalized_at, static_id)
			VALUES ($1, $2, $3::jsonb, now(), $4) RETURNING id::text`,
			a.Name, a.Description, fileArch, a.StaticID,
		).Scan(&id); err != nil {
			return fmt.Errorf("agent %s: insert: %w", a.StaticID, err)
		}
		ap.agentIDs[a.StaticID] = id

		endpoints := make([]string, 0, len(a.EndpointBackends))
		for ep := range a.EndpointBackends {
			endpoints = append(endpoints, ep)
		}
		sort.Strings(endpoints)
		for _, ep := range endpoints {
			backendID, err := ap.backendID(a.EndpointBackends[ep])
			if err != nil {
				return fmt.Errorf("agent %s: endpoint %s: %w", a.StaticID, ep, err)
			}
			if _, err := ap.tx.ExecContext(ap.ctx, `
				INSERT INTO agent_endpoint_backend (agent_id, endpoint_id, backend_id)
				VALUES ($1::uuid, $2, $3::uuid)`, id, ep, backendID); err != nil {
				return fmt.Errorf("agent %s: wire endpoint %s: %w", a.StaticID, ep, err)
			}
		}
		ap.log(SeedKindAgent, a.StaticID, SeedActionCreated, "")
	}
	return nil
}

// versions is pass 1: an existing version is only checked (owning agent,
// content drift — a warning, or an error when the file also asks to deploy
// it); a missing one is inserted READY at its agent's tip, in file
// order, with prompts, the agent-tool flag and MCP attachments. Sub-agent
// bindings wait for pass 2.
func (ap *seedApply) versions() error {
	ap.versionIDs = map[string]string{}
	ap.created = map[string]bool{}
	flowMaps := map[string]map[string]any{}
	for _, a := range ap.set.Agents {
		flowMaps[a.StaticID] = a.FlowMapConfig
	}

	for _, v := range ap.set.Versions {
		agentID, ok := ap.agentIDs[v.Agent]
		if !ok {
			return fmt.Errorf("version %s: agent %s is not declared in the seed set", v.StaticID, v.Agent)
		}
		content, err := v.Content()
		if err != nil {
			return fmt.Errorf("version %s: content: %w", v.StaticID, err)
		}

		var id, ownerID string
		err = ap.tx.QueryRowContext(ap.ctx,
			`SELECT id::text, agent_id::text FROM agent_versions WHERE static_id = $1`, v.StaticID,
		).Scan(&id, &ownerID)
		if err == nil {
			if ownerID != agentID {
				return fmt.Errorf("version %s: exists under a different agent than %s", v.StaticID, v.Agent)
			}
			ap.versionIDs[v.StaticID] = id
			dbContent, err := dbVersionContent(ap.ctx, ap.tx, id)
			if err != nil {
				return fmt.Errorf("version %s: read content: %w", v.StaticID, err)
			}
			if seed.ContentHash(dbContent) != seed.ContentHash(content) {
				if v.Deploy == seed.DeployIfNone || v.Deploy == seed.DeployForce {
					return fmt.Errorf("version %s: content differs from the seed and the file asks to deploy it (deploy: %s); new content needs a new static_id", v.StaticID, v.Deploy)
				}
				ap.log(SeedKindVersion, v.StaticID, SeedActionWarning,
					"content differs from the seed; left unchanged (new content needs a new static_id)")
			} else {
				ap.log(SeedKindVersion, v.StaticID, SeedActionUnchanged, "")
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("version %s: %w", v.StaticID, err)
		}

		id, err = ap.insertVersion(agentID, v, content, flowMaps[v.Agent])
		if err != nil {
			return fmt.Errorf("version %s: %w", v.StaticID, err)
		}
		ap.versionIDs[v.StaticID] = id
		ap.created[v.StaticID] = true
		ap.log(SeedKindVersion, v.StaticID, SeedActionCreated, "")
	}
	return nil
}

// insertVersion appends a READY version at the agent's tip (read under the
// agent lock). The root (no tip) receives the agent's flow_map_config.
func (ap *seedApply) insertVersion(agentID string, v seed.Version, content seed.VersionContent, flowMap map[string]any) (string, error) {
	tipID, err := lockAgentTip(ap.ctx, ap.tx, agentID)
	if err != nil {
		return "", err
	}
	var parent, flowMapJSON any // NULL unless set below
	if tipID != "" {
		parent = tipID
	} else if flowMap != nil {
		b, err := json.Marshal(flowMap)
		if err != nil {
			return "", fmt.Errorf("flow_map_config: %w", err)
		}
		flowMapJSON = b
	}

	var id string
	if err := ap.tx.QueryRowContext(ap.ctx, `
		INSERT INTO agent_versions (agent_id, parent_version_id, status, prompts, agent_tool_enabled, flow_map_config, static_id)
		VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, $5, $6::jsonb, $7)
		RETURNING id::text`,
		agentID, parent, string(types.AgentStatusReady), []byte(content.Prompts), v.AgentToolEnabled, flowMapJSON, v.StaticID,
	).Scan(&id); err != nil {
		return "", fmt.Errorf("insert: %w", err)
	}

	for _, m := range v.MCPBackends {
		backendID, err := ap.backendID(m.Backend)
		if err != nil {
			return "", err
		}
		if _, err := ap.tx.ExecContext(ap.ctx, `
			INSERT INTO agent_version_mcp_backend (agent_version_id, backend_id, note)
			VALUES ($1::uuid, $2::uuid, $3)`, id, backendID, m.Note); err != nil {
			return "", fmt.Errorf("attach mcp backend %s: %w", m.Backend, err)
		}
	}

	if v.AgentToolEnabled {
		if err := checkNoCollision(ap.ctx, ap.tx, id); err != nil {
			return "", err
		}
	}
	return id, nil
}

// dbVersionContent builds the hashable content of a stored version with the
// same shape and normalisation as seed.Version.Content: prompts as canonical
// JSON, MCP attachments by backend name, bindings by target static_id ("" for
// a target without one).
func dbVersionContent(ctx context.Context, q sqlQueryer, versionID string) (seed.VersionContent, error) {
	var c seed.VersionContent
	var prompts []byte
	if err := q.QueryRowContext(ctx,
		`SELECT prompts, agent_tool_enabled FROM agent_versions WHERE id = $1::uuid`, versionID,
	).Scan(&prompts, &c.AgentToolEnabled); err != nil {
		return c, err
	}
	canon, err := seed.CanonicalJSON(json.RawMessage(prompts))
	if err != nil {
		return c, fmt.Errorf("prompts: %w", err)
	}
	c.Prompts = canon

	rows, err := q.QueryContext(ctx, `
		SELECT b.name, m.note FROM agent_version_mcp_backend m
		JOIN tool_backends b ON b.id = m.backend_id
		WHERE m.agent_version_id = $1::uuid`, versionID)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var m seed.MCPRef
		if err := rows.Scan(&m.Backend, &m.Note); err != nil {
			rows.Close()
			return c, err
		}
		c.MCP = append(c.MCP, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return c, err
	}

	rows, err = q.QueryContext(ctx, `
		SELECT s.name, COALESCE(t.static_id, ''), s.note FROM agent_version_subagent s
		JOIN agent_versions t ON t.id = s.target_version_id
		WHERE s.caller_version_id = $1::uuid`, versionID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var b seed.SubAgentRef
		if err := rows.Scan(&b.Name, &b.Target, &b.Note); err != nil {
			return c, err
		}
		c.SubAgents = append(c.SubAgents, b)
	}
	return c, rows.Err()
}

// bindings is pass 2: every version created by this run gets its sub-agent
// bindings, now that all versions of the set exist. It goes through the
// binding checks with the caller-status rule skipped (the seed import is the
// one sanctioned exception), including the whole-graph cycle check.
func (ap *seedApply) bindings() error {
	for _, v := range ap.set.Versions {
		if !ap.created[v.StaticID] {
			continue
		}
		callerID := ap.versionIDs[v.StaticID]
		for _, b := range v.SubAgents {
			targetID, ok := ap.versionIDs[b.Target]
			if !ok {
				return fmt.Errorf("version %s: sub-agent %s: target %s is not declared in the seed set", v.StaticID, b.Name, b.Target)
			}
			if err := addBindingTx(ap.ctx, ap.tx, callerID, b.Name, targetID, b.Note, true); err != nil {
				return fmt.Errorf("version %s: sub-agent %s → %s: %w", v.StaticID, b.Name, b.Target, err)
			}
		}
	}
	return nil
}

// deploys applies each version's deploy marker. if_none deploys only when the
// agent has no DEPLOYED version; force deploys unless already DEPLOYED. A
// performed deploy runs the shared deploy checks (READY, endpoints mapped) and
// any failure is an error.
func (ap *seedApply) deploys() error {
	for _, v := range ap.set.Versions {
		if v.Deploy != seed.DeployIfNone && v.Deploy != seed.DeployForce {
			continue
		}
		id := ap.versionIDs[v.StaticID]
		agentID := ap.agentIDs[v.Agent]
		if err := lockAgent(ap.ctx, ap.tx, agentID); err != nil {
			return fmt.Errorf("version %s: deploy: %w", v.StaticID, err)
		}
		var current sql.NullString
		if err := ap.tx.QueryRowContext(ap.ctx, `
			SELECT id::text FROM agent_versions WHERE agent_id = $1::uuid AND status = 'DEPLOYED'`, agentID,
		).Scan(&current); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("version %s: deploy: %w", v.StaticID, err)
		}

		switch {
		case current.String == id:
			ap.log(SeedKindDeploy, v.StaticID, SeedActionUnchanged, "already deployed")
			continue
		case current.Valid && v.Deploy == seed.DeployIfNone:
			ap.log(SeedKindDeploy, v.StaticID, SeedActionDeploySkipped, "agent already has deployed version "+current.String)
			continue
		}
		prev, err := deployTx(ap.ctx, ap.tx, id)
		if err != nil {
			return fmt.Errorf("version %s: deploy: %w", v.StaticID, err)
		}
		detail := ""
		if prev != nil {
			detail = "demoted " + *prev
		}
		ap.log(SeedKindDeploy, v.StaticID, SeedActionDeployed, detail)
	}
	return nil
}

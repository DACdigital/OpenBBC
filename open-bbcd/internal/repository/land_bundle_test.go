package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestLandBundle_SplitsBundleAndStampsFinalizedAt(t *testing.T) {
	db := openTestDB(t)
	repo := NewAgentVersionRepository(db)
	ctx := context.Background()

	agentID, versionID := seedAgent(t, db)

	bundle := []byte(`{
		"main_prompt": "<role>coffee bot</role>",
		"tools": [{"id":"orders.create","name":"orders_create","method":"POST","path":"/api/orders"}],
		"skills": [
			{"name":"place_order","description":"Take an order","prompt":"<role>order</role>"}
		],
		"external_actions": [{"skill_id":"escalate","external_note":"file in support portal"}]
	}`)

	if err := repo.LandBundle(ctx, versionID, bundle, false); err != nil {
		t.Fatalf("LandBundle: %v", err)
	}

	// Agent row: architecture populated, finalized_at non-null.
	var archBytes []byte
	var finalized *string // text scan to detect non-null without time parsing
	err := db.QueryRow(
		`SELECT architecture::text, finalized_at::text FROM agents WHERE id = $1::uuid`,
		agentID,
	).Scan(&archBytes, &finalized)
	if err != nil {
		t.Fatalf("read agent: %v", err)
	}
	if finalized == nil || *finalized == "" {
		t.Fatalf("finalized_at not stamped")
	}

	var arch types.Architecture
	if err := json.Unmarshal(archBytes, &arch); err != nil {
		t.Fatalf("parse architecture: %v", err)
	}
	if len(arch.SkillsMeta) != 1 || arch.SkillsMeta[0].Name != "place_order" {
		t.Fatalf("skills_meta wrong: %+v", arch.SkillsMeta)
	}
	if len(arch.Tools) == 0 {
		t.Fatalf("tools missing from architecture")
	}

	// Version row: prompts populated, status READY.
	var promptsBytes []byte
	var status string
	if err := db.QueryRow(
		`SELECT prompts::text, status FROM agent_versions WHERE id = $1::uuid`,
		versionID,
	).Scan(&promptsBytes, &status); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if status != "READY" {
		t.Fatalf("status: want READY, got %q", status)
	}
	var prompts types.Prompts
	if err := json.Unmarshal(promptsBytes, &prompts); err != nil {
		t.Fatalf("parse prompts: %v", err)
	}
	if prompts.MainPrompt != "<role>coffee bot</role>" {
		t.Fatalf("main_prompt wrong: %q", prompts.MainPrompt)
	}
	if prompts.SkillPrompts["place_order"] != "<role>order</role>" {
		t.Fatalf("skill_prompts wrong: %+v", prompts.SkillPrompts)
	}

	// Re-land without --force is rejected (prompts already populated).
	err = repo.LandBundle(ctx, versionID, bundle, false)
	if !errors.Is(err, types.ErrBundleAlreadySet) {
		t.Fatalf("re-land: want ErrBundleAlreadySet, got %v", err)
	}

	// Re-land WITH --force succeeds and does NOT re-stamp finalized_at.
	if err := repo.LandBundle(ctx, versionID, bundle, true); err != nil {
		t.Fatalf("re-land force: %v", err)
	}
	var finalized2 *string
	_ = db.QueryRow(
		`SELECT finalized_at::text FROM agents WHERE id = $1::uuid`,
		agentID,
	).Scan(&finalized2)
	if finalized2 == nil || *finalized2 != *finalized {
		t.Fatalf("finalized_at changed on force re-land: was %v, now %v", *finalized, finalized2)
	}
}

func TestCreateVersionFromPrompts_ForksAndCopiesMCP(t *testing.T) {
	db := openTestDB(t)
	repo := NewAgentVersionRepository(db)
	wiring := NewVersionWiringRepository(db)
	ctx := context.Background()

	_, parentID := seedAgent(t, db)
	mcp := seedMCPBackend(t, db, "slack")
	if err := wiring.AttachMCP(ctx, parentID, mcp, "escalations only"); err != nil {
		t.Fatalf("AttachMCP: %v", err)
	}

	newPrompts, _ := json.Marshal(types.Prompts{
		MainPrompt:   "edited",
		SkillPrompts: map[string]string{"place_order": "edited skill"},
	})

	newID, err := repo.CreateVersionFromPrompts(ctx, parentID, newPrompts)
	if err != nil {
		t.Fatalf("CreateVersionFromPrompts: %v", err)
	}
	if newID == "" || newID == parentID {
		t.Fatalf("expected a new distinct id, got %q (parent=%q)", newID, parentID)
	}

	// New row: status DRAFT, parent set, prompts persisted.
	var status, parent string
	var promptsBytes []byte
	if err := db.QueryRow(
		`SELECT status, parent_version_id::text, prompts::text FROM agent_versions WHERE id = $1::uuid`,
		newID,
	).Scan(&status, &parent, &promptsBytes); err != nil {
		t.Fatalf("read new version: %v", err)
	}
	if status != "DRAFT" {
		t.Fatalf("status: want DRAFT, got %q", status)
	}
	if parent != parentID {
		t.Fatalf("parent_version_id: want %q, got %q", parentID, parent)
	}
	var got types.Prompts
	if err := json.Unmarshal(promptsBytes, &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.MainPrompt != "edited" || got.SkillPrompts["place_order"] != "edited skill" {
		t.Fatalf("prompts not persisted: %+v", got)
	}

	// MCP attachment was copied forward with the same note.
	atts, err := wiring.ListMCPAttachments(ctx, newID)
	if err != nil {
		t.Fatalf("ListMCPAttachments: %v", err)
	}
	if len(atts) != 1 || atts[0].BackendID != mcp || atts[0].Note != "escalations only" {
		t.Fatalf("mcp copy wrong: %+v", atts)
	}
}

package flowmap_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/flowmap"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// zipDir walks dir and returns an in-memory zip with paths relative to dir.
// Mirrors what r.FormFile receives at runtime.
func zipDir(t *testing.T, dir string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		fw, err := w.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(fw, f)
		return err
	})
	if err != nil {
		t.Fatalf("zipDir: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zipDir close: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

// zipDirOverride builds a zip from dir but replaces the bytes of files
// whose relative path matches a key in overrides.
func zipDirOverride(t *testing.T, dir string, overrides map[string]string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		fw, err := w.Create(relSlash)
		if err != nil {
			return err
		}
		if override, ok := overrides[relSlash]; ok {
			_, err = io.WriteString(fw, override)
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(fw, f)
		return err
	})
	if err != nil {
		t.Fatalf("zipDirOverride: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zipDirOverride close: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestParse_HappyPath(t *testing.T) {
	r := zipDir(t, "testdata/sample-flowmap")

	cfg, err := flowmap.Parse(r)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if cfg.SchemaVersion != 2 {
		t.Errorf("SchemaVersion = %d, want 2", cfg.SchemaVersion)
	}
	if cfg.Source.AppName != "sample-flowmap" {
		t.Errorf("AppName = %q, want sample-flowmap", cfg.Source.AppName)
	}
	if len(cfg.Flows) != 1 {
		t.Fatalf("Flows = %d, want 1", len(cfg.Flows))
	}
	flow := cfg.Flows[0]
	if flow.ID != "update-profile" {
		t.Errorf("Flow.ID = %q, want update-profile", flow.ID)
	}
	if !flow.Included {
		t.Error("Flow.Included should default to true")
	}
	if flow.Origin != "discovered" {
		t.Errorf("Flow.Origin = %q, want discovered", flow.Origin)
	}
	if !strings.HasPrefix(flow.Workflow.Mermaid, "flowchart TD") {
		t.Errorf("Flow.Workflow.Mermaid does not start with flowchart TD: %q", flow.Workflow.Mermaid)
	}
	if !strings.Contains(flow.ProseMD, "How the agent handles this") {
		t.Errorf("Flow.ProseMD should preserve the body: %q", flow.ProseMD[:min(80, len(flow.ProseMD))])
	}

	if len(cfg.Skills) != 1 || cfg.Skills[0].ID != "account" {
		t.Errorf("Skills = %+v", cfg.Skills)
	}
	if len(cfg.Skills[0].SuggestedEndpoints) != 1 {
		t.Fatalf("Skill.SuggestedEndpoints = %d, want 1", len(cfg.Skills[0].SuggestedEndpoints))
	}
	if cfg.Skills[0].SuggestedEndpoints[0].Endpoint != "users.update" {
		t.Errorf("Skill.SuggestedEndpoints[0].Endpoint = %q, want users.update",
			cfg.Skills[0].SuggestedEndpoints[0].Endpoint)
	}
	if cfg.Skills[0].SuggestedEndpoints[0].Role != "write" {
		t.Errorf("Skill.SuggestedEndpoints[0].Role = %q, want write",
			cfg.Skills[0].SuggestedEndpoints[0].Role)
	}

	if len(cfg.Endpoints) != 1 || cfg.Endpoints[0].ID != "users.update" {
		t.Errorf("Endpoints = %+v", cfg.Endpoints)
	}
	if cfg.Endpoints[0].Method != "PATCH" {
		t.Errorf("Endpoint.Method = %q, want PATCH", cfg.Endpoints[0].Method)
	}
	if cfg.Endpoints[0].Auth != "bearer" {
		t.Errorf("Endpoint.Auth = %q, want bearer", cfg.Endpoints[0].Auth)
	}
	if len(cfg.Endpoints[0].UsedBySkills) != 1 || cfg.Endpoints[0].UsedBySkills[0] != "account" {
		t.Errorf("Endpoint.UsedBySkills = %v, want [account]", cfg.Endpoints[0].UsedBySkills)
	}
}

func TestParse_MissingWorkflowFallback(t *testing.T) {
	r := zipDirOverride(t, "testdata/sample-flowmap", map[string]string{
		"flows/update-profile.md": `---
schema_version: 2
id: update-profile
name: Update profile
description: "Use when the user wants to change their profile"
intent: "Update the acting user's name or email"
user_phrases:
  - "update my name"
entry: src/pages/Profile.tsx
trigger: user clicks Save
preconditions:
  - User is signed in
skills_used:
  - skill: account
    skill_ref: ../skills/account.md
postconditions:
  - The profile is persisted
side_effects: []
related_flows: []
confidence: high
---

# Update profile

stub body
`,
	})

	cfg, err := flowmap.Parse(r)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wf := cfg.Flows[0].Workflow.Mermaid
	if !strings.Contains(wf, "s_account[account]") {
		t.Errorf("Fallback workflow does not contain expected linear node:\n%s", wf)
	}
}

func TestParse_InvalidSkillReference(t *testing.T) {
	r := zipDirOverride(t, "testdata/sample-flowmap", map[string]string{
		"flows/update-profile.md": `---
schema_version: 2
id: update-profile
name: Update profile
description: "Use when the user wants to change their profile"
intent: "Update the acting user's name or email"
user_phrases: ["update my name"]
preconditions: []
skills_used:
  - skill: account
    skill_ref: ../skills/account.md
postconditions: []
side_effects: []
related_flows: []
confidence: high
workflow: |
  flowchart TD
    start([start]) --> s_x[ghost-skill]
    s_x --> e([end])
---

# Update profile

stub
`,
	})

	_, err := flowmap.Parse(r)
	if err == nil {
		t.Fatal("Parse should fail when workflow references an unknown skill")
	}
	if !errors.Is(err, types.ErrFlowMapInvalid) {
		t.Errorf("err = %v, want errors.Is(types.ErrFlowMapInvalid)", err)
	}
}

func TestParse_RejectsV1Schema(t *testing.T) {
	r := zipDirOverride(t, "testdata/sample-flowmap", map[string]string{
		"AGENTS.md": `---
schema_version: 1
generated_by: flow-map-compiler
generated_at: 2026-01-01T00:00:00Z
generated_from_sha: deadbeef
app_name: sample-flowmap
stack:
  framework: react
  version: "18.0.0"
  router: react-router-dom
  language: ts
counts:
  skills: 1
  flows: 1
  endpoints: 1
freshness:
  last_verified: 2026-01-01T00:00:00Z
  staleness_check: weekly
files:
  app_context: APP.md
  glossary: glossary.md
  skills_dir: skills/
  flows_dir: flows/
  endpoints_dir: endpoints/
---

# sample-flowmap — flow map

stub
`,
	})

	_, err := flowmap.Parse(r)
	if err == nil {
		t.Fatal("Parse should reject schema_version 1 inputs")
	}
	if !errors.Is(err, types.ErrFlowMapInvalid) {
		t.Errorf("err = %v, want errors.Is(types.ErrFlowMapInvalid)", err)
	}
	if !strings.Contains(err.Error(), "schema_version 1") {
		t.Errorf("err = %v, want mention of schema_version 1", err)
	}
}

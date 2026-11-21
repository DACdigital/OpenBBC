package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// skillGoldenVersion is a production-shaped version: skills_meta on the
// architecture (no "skills" key), prompts on prompts.skill_prompts. One
// skill's prompt is whitespace-only, so it is listed but not loadable.
const (
	skillGoldenArch    = `{"tools":[],"skills_meta":[{"name":"pricing","description":"Price rules"},{"name":"blank","description":"No prompt yet"},{"name":"returns"}]}`
	skillGoldenPrompts = `{"main_prompt":"You are a shop assistant.","skill_prompts":{"pricing":"<rules>\n  Quote list price; \"VAT\" included.\n</rules>","blank":"  \n","returns":"Returns within 30 days.","orphan":"never served"}}`
	// No loadable skill: Skill is not offered, yet a call still answers.
	skillGoldenNoneArch    = `{"tools":[],"skills_meta":[{"name":"blank"}]}`
	skillGoldenNonePrompts = `{"main_prompt":"No skills here."}`
)

type skillGoldenCall struct {
	Input   json.RawMessage `json:"input"`
	IsError bool            `json:"is_error"`
	Output  json.RawMessage `json:"output"`
}

type skillGoldenTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type skillGoldenCase struct {
	// Bundle is the eval-input-v1 agent_version.bundle, joined the way the
	// export (evalStoreAdapter.GetBundle) joins it.
	Bundle json.RawMessage `json:"bundle"`
	// Tool is the Skill tool the runtime offers, or null when not offered.
	Tool    *skillGoldenTool           `json:"tool"`
	Results map[string]skillGoldenCall `json:"results"`
}

func skillGoldenRun(t *testing.T, archJSON, promptsJSON string, calls map[string]string) skillGoldenCase {
	t.Helper()
	var arch types.Architecture
	var prompts types.Prompts
	if err := json.Unmarshal([]byte(archJSON), &arch); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(promptsJSON), &prompts); err != nil {
		t.Fatal(err)
	}
	skills := types.JoinSkills(arch, prompts)
	bundle, err := json.Marshal(types.Bundle{MainPrompt: prompts.MainPrompt, Tools: arch.Tools, Skills: skills})
	if err != nil {
		t.Fatal(err)
	}

	h := tools.NewComposite(nil)
	defs, err := h.Tools(json.RawMessage(archJSON), skills)
	if err != nil {
		t.Fatal(err)
	}
	out := skillGoldenCase{Bundle: bundle, Results: map[string]skillGoldenCall{}}
	for _, d := range defs {
		if d.Name == tools.SkillToolName {
			out.Tool = &skillGoldenTool{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema}
		}
	}
	for key, input := range calls {
		r, err := h.Call(context.Background(), json.RawMessage(archJSON), skills,
			tools.Call{ID: "c-" + key, Name: tools.SkillToolName, Input: json.RawMessage(input)})
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		out.Results[key] = skillGoldenCall{Input: json.RawMessage(input), IsError: r.IsError, Output: r.Output}
	}
	return out
}

// TestSkillToolGoldenParity writes aikdm/tests/fixtures/skill_tool_parity.json:
// the runtime Skill tool definition and call results for fixture versions.
// aikdm's test_skill_tool_golden.py asserts its eval target matches.
func TestSkillToolGoldenParity(t *testing.T) {
	golden := map[string]skillGoldenCase{
		"with_skills": skillGoldenRun(t, skillGoldenArch, skillGoldenPrompts, map[string]string{
			"hit":          `{"name":"pricing"}`,
			"miss_unknown": `{"name":"orphan"}`,
			"miss_empty":   `{"name":"blank"}`,
		}),
		"no_loadable_skills": skillGoldenRun(t, skillGoldenNoneArch, skillGoldenNonePrompts, map[string]string{
			"miss_not_offered": `{"name":"pricing"}`,
		}),
	}
	ws := golden["with_skills"]
	if ws.Tool == nil || ws.Results["hit"].IsError || !ws.Results["miss_unknown"].IsError || !ws.Results["miss_empty"].IsError {
		t.Fatalf("unexpected runtime behaviour: %+v", ws)
	}
	if golden["no_loadable_skills"].Tool != nil {
		t.Fatal("Skill offered with no loadable skills")
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(golden); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(goldenDir, "skill_tool_parity.json")
	if *update {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run: go test ./internal/eval -run TestSkillToolGoldenParity -update)", path, err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("%s is stale; regenerate with: go test ./internal/eval -run TestSkillToolGoldenParity -update\n--- got ---\n%s", path, buf.Bytes())
	}
}

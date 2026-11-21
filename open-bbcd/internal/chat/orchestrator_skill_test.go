package chat

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
)

// Spec skill-tool-version-prompts: the Skill tool resolves from the running
// version's skills_meta + prompts.skill_prompts, with a real Composite.

func skillResult(t *testing.T, r llm.ToolResultBlock) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(r.Result, &m); err != nil {
		t.Fatalf("skill result %s: %v", r.Result, err)
	}
	return m
}

func TestSkill_RootTurnResolvesVersionPrompts(t *testing.T) {
	e := newMAEnv(t)
	e.repo.add("vR", sysRoot)
	e.repo.setSkills("vR",
		`[{"name":"pricing","description":"d"},{"name":"empty"},{"name":"faq"}]`,
		`{"pricing":"PRICING RULES","faq":"FAQ RULES","orphan":"X"}`)
	e.builder.override = map[string]tools.Handler{"vR": tools.NewComposite(nil)}
	e.llm.Route(sysRoot, callsStep("",
		toolCallSpec{Name: "Skill", Input: `{"name":"pricing"}`},
		toolCallSpec{Name: "Skill", Input: `{"name":"empty"}`},
		toolCallSpec{Name: "Skill", Input: `{"name":"orphan"}`},
	), textStep("done"))

	if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
		t.Fatal(err)
	}

	req := e.llm.Requests(sysRoot)[0]
	if len(req.Tools) == 0 || req.Tools[0].Name != tools.SkillToolName {
		t.Fatalf("Skill not offered first: %+v", req.Tools)
	}
	var schema struct {
		Properties struct {
			Name struct {
				Enum []string `json:"enum"`
			} `json:"name"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(req.Tools[0].InputSchema, &schema)
	if got := schema.Properties.Name.Enum; len(got) != 2 || got[0] != "pricing" || got[1] != "faq" {
		t.Fatalf("enum = %v, want [pricing faq]", got)
	}

	rs := resultsOf(e.toolMsgs("R")[0])
	if len(rs) != 3 {
		t.Fatalf("results = %d", len(rs))
	}
	if got := skillResult(t, rs[0]); rs[0].IsError || got["prompt"] != "PRICING RULES" {
		t.Fatalf("hit = %v isErr=%v", got, rs[0].IsError)
	}
	for i, name := range []string{"empty", "orphan"} {
		r := rs[i+1]
		if got := skillResult(t, r); !r.IsError || got["error"] != "unknown skill: "+name {
			t.Fatalf("%s: %v isErr=%v", name, got, r.IsError)
		}
	}
}

func TestSkill_NoLoadableSkillsNotOffered(t *testing.T) {
	e := newMAEnv(t)
	e.repo.add("vR", sysRoot)
	e.repo.setSkills("vR", `[{"name":"empty"}]`, `{}`)
	e.builder.override = map[string]tools.Handler{"vR": tools.NewComposite(nil)}
	e.llm.Route(sysRoot, textStep("done"))
	if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
		t.Fatal(err)
	}
	for _, d := range e.llm.Requests(sysRoot)[0].Tools {
		if d.Name == tools.SkillToolName {
			t.Fatalf("Skill offered with no loadable skills: %+v", d)
		}
	}
}

func TestSkill_ChildSessionResolvesTargetVersionSkills(t *testing.T) {
	e := newMAEnv(t)
	e.standardRoot()
	e.repo.setSkills("vR", `[{"name":"shared"},{"name":"root_only"}]`, `{"shared":"ROOT PROMPT","root_only":"R"}`)
	e.repo.setSkills("vW", `[{"name":"shared"}]`, `{"shared":"WORKER PROMPT"}`)
	e.builder.override = map[string]tools.Handler{
		"vR": tools.NewComposite(nil),
		"vW": tools.NewComposite(nil),
	}
	e.llm.Route(sysRoot, callsStep("", agentCall("researcher", "d", "P")), textStep("root done"))
	e.llm.Route(sysW, callsStep("",
		toolCallSpec{Name: "Skill", Input: `{"name":"shared"}`},
		toolCallSpec{Name: "Skill", Input: `{"name":"root_only"}`},
	), textStep("W done"))

	if _, err := e.turn(context.Background(), &syncRecordingSink{}); err != nil {
		t.Fatal(err)
	}
	calls := e.childCalls()
	if len(calls) != 1 {
		t.Fatalf("child calls = %d", len(calls))
	}
	rs := resultsOf(e.toolMsgs(calls[0].ChildID)[0])
	if got := skillResult(t, rs[0]); rs[0].IsError || got["prompt"] != "WORKER PROMPT" {
		t.Fatalf("child hit = %v isErr=%v", got, rs[0].IsError)
	}
	if got := skillResult(t, rs[1]); !rs[1].IsError || got["error"] != "unknown skill: root_only" {
		t.Fatalf("child parent-only skill = %v isErr=%v", got, rs[1].IsError)
	}
}

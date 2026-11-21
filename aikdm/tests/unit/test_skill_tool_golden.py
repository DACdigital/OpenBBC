# tests/unit/test_skill_tool_golden.py
"""Cross-language parity (spec skill-tool-version-prompts, Scope 5). The
fixture is written by open-bbcd's TestSkillToolGoldenParity with -update;
regenerate it there, never by hand."""

import json
from pathlib import Path

import pytest

from aikdm.eval import target as target_mod

FIXTURES = Path(__file__).parents[1] / "fixtures"
GOLDEN = json.loads((FIXTURES / "skill_tool_parity.json").read_text(encoding="utf-8"))


class _NeverCalled:
    def call(self, name, args):
        raise AssertionError("Skill must be answered locally")


@pytest.mark.parametrize("case", sorted(GOLDEN))
def test_skill_tool_definition_matches_runtime(case):
    g = GOLDEN[case]
    tools, schema_by_name = target_mod.build_tool_specs(g["bundle"])
    skill = [t["function"] for t in tools if t["function"]["name"] == "Skill"]
    if g["tool"] is None:
        assert skill == []
    else:
        assert skill == [{"name": g["tool"]["name"], "description": g["tool"]["description"],
                          "parameters": g["tool"]["input_schema"]}]
        assert tools[0]["function"]["name"] == "Skill"  # first, like the runtime
    assert "Skill" not in schema_by_name


@pytest.mark.parametrize("case,key", [(c, k) for c in sorted(GOLDEN) for k in GOLDEN[c]["results"]])
def test_skill_call_results_match_runtime(case, key):
    g = GOLDEN[case]
    want = g["results"][key]
    caller = target_mod.SkillToolCaller(g["bundle"], _NeverCalled())
    out = caller.call("Skill", want["input"])
    assert out["result"] == want["output"]
    assert out["source"] == ("error" if want["is_error"] else "skill")


def test_golden_covers_hit_and_all_misses():
    assert set(GOLDEN["with_skills"]["results"]) == {"hit", "miss_unknown", "miss_empty"}
    assert GOLDEN["no_loadable_skills"]["tool"] is None

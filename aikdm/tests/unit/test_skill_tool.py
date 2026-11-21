# tests/unit/test_skill_tool.py
"""Skill tool in the eval target (spec skill-tool-version-prompts, Scope 4)."""

import json
from types import SimpleNamespace as NS

import httpx

from aikdm.eval import runner as runner_mod
from aikdm.eval import target as target_mod
from aikdm.eval.schemas import InputMessage, InputSession
from aikdm.eval.tool_mock import ToolMock
from aikdm.eval.tool_real import RealHTTPToolCaller

BUNDLE = {
    "main_prompt": "MAIN",
    "tools": [{"name": "lookup", "description": "d"}],
    "skills": [
        {"name": "pricing", "description": "d", "prompt": "PRICING RULES"},
        {"name": "blank", "prompt": " \n\t"},
        {"name": "none"},
        {"name": "returns", "prompt": "RETURNS"},
    ],
}


class _Inner:
    def __init__(self):
        self.calls = []

    def call(self, name, args):
        self.calls.append(name)
        return {"source": "mocked", "result": {"inner": name}}


def test_system_prompt_is_main_prompt_only():
    assert target_mod.build_system_prompt(BUNDLE) == "MAIN"
    assert "<skill" not in target_mod.build_system_prompt(BUNDLE)


def test_skill_tool_offered_first_with_loadable_enum():
    tools, schema_by_name = target_mod.build_tool_specs(BUNDLE)
    assert [t["function"]["name"] for t in tools] == ["Skill", "lookup"]
    params = tools[0]["function"]["parameters"]
    assert params == {"type": "object",
                      "properties": {"name": {"type": "string", "enum": ["pricing", "returns"]}},
                      "required": ["name"]}
    assert set(schema_by_name) == {"lookup"}


def test_skill_tool_omitted_without_loadable_skills():
    for skills in ([], None, [{"name": "blank", "prompt": "  "}, {"name": "none"}]):
        tools, _ = target_mod.build_tool_specs({**BUNDLE, "skills": skills})
        assert [t["function"]["name"] for t in tools] == ["lookup"]


def test_skill_caller_answers_locally_and_delegates_others():
    inner = _Inner()
    c = target_mod.SkillToolCaller(BUNDLE, inner)
    assert c.call("Skill", {"name": "pricing"}) == {"source": "skill",
                                                    "result": {"prompt": "PRICING RULES"}}
    for name in ("blank", "none", "nope"):
        assert c.call("Skill", {"name": name}) == {
            "source": "error", "result": {"error": f"unknown skill: {name}"}}
    assert c.call("Skill", {}) == {"source": "error", "result": {"error": "unknown skill: "}}
    assert c.call("lookup", {}) == {"source": "mocked", "result": {"inner": "lookup"}}
    assert inner.calls == ["lookup"]


def test_skill_answered_even_when_not_offered():
    c = target_mod.SkillToolCaller({"main_prompt": "M", "skills": []}, _Inner())
    assert c.call("Skill", {"name": "pricing"}) == {
        "source": "error", "result": {"error": "unknown skill: pricing"}}


def _resp(content="", tool_calls=None):
    return NS(choices=[NS(message=NS(content=content, tool_calls=tool_calls or []))], usage=None)


def _session():
    return InputSession(session_id="s", transcript=[
        InputMessage(message_id="u1", role="user", content="price?"),
        InputMessage(message_id="a1", role="assistant", content="ref"),
    ], criteria=[])


def _real_caller():
    def handler(request):
        raise AssertionError("Skill must never reach a real backend")
    return RealHTTPToolCaller(bundle=BUNDLE, tool_backends={}, header_overrides={},
                              transport=httpx.MockTransport(handler))


async def _simulated_skill_turn(monkeypatch, caller):
    seen = []
    responses = iter([
        _resp(tool_calls=[NS(id="k1", function=NS(name="Skill",
                                                  arguments=json.dumps({"name": "pricing"})))]),
        _resp("quoted"),
    ])

    async def fake(**kw):
        seen.append(kw)
        return next(responses)

    monkeypatch.setattr(target_mod.litellm, "acompletion", fake)
    out = await runner_mod.run_session(
        session=_session(), bundle=BUNDLE, simulator_agent=object(), judge_agent=object(),
        target_model="m", tool_caller=caller)
    return seen, out


async def test_simulated_turn_loads_skill_and_continues_mock_and_real(monkeypatch):
    results = []
    for caller in (ToolMock([m.model_dump() for m in _session().transcript],
                            {"lookup": {}}), _real_caller()):
        seen, out = await _simulated_skill_turn(monkeypatch, caller)
        assert seen[0]["messages"][0] == {"role": "system", "content": "MAIN"}
        assert seen[0]["tools"][0]["function"]["name"] == "Skill"
        assert seen[1]["messages"][-1] == {"role": "tool", "tool_call_id": "k1",
                                           "content": json.dumps({"prompt": "PRICING RULES"})}
        transcript = out.result.transcript
        results.append(transcript)
        assert transcript[-1]["content"] == "quoted"
    # Same answer whatever mock_mcp_tools selects.
    assert results[0] == results[1]
    call = results[0][1]["tool_calls"][0]
    assert call == {"name": "Skill", "args": {"name": "pricing"},
                    "result": {"prompt": "PRICING RULES"}, "source": "skill"}


def test_patched_skill_prompt_is_served_and_empty_skill_becomes_loadable():
    from aikdm.train.patcher import apply_patches
    from aikdm.train.schemas import SectionPatch

    patched = apply_patches(BUNDLE, [
        SectionPatch(section_id="skill.pricing.prompt", new="NEW PRICING", rationale="x"),
        SectionPatch(section_id="skill.blank.prompt", new="FIRST BLANK PROMPT", rationale="x"),
    ])
    c = target_mod.SkillToolCaller(patched, _Inner())
    assert c.call("Skill", {"name": "pricing"})["result"] == {"prompt": "NEW PRICING"}
    assert c.call("Skill", {"name": "blank"})["result"] == {"prompt": "FIRST BLANK PROMPT"}
    tools, _ = target_mod.build_tool_specs(patched)
    assert tools[0]["function"]["parameters"]["properties"]["name"]["enum"] == [
        "pricing", "blank", "returns"]

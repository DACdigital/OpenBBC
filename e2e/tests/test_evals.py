"""Scenarios 4-5: real-mode and mock-mode evals through run_eval.sh in the runner."""

import json
import re

from support.evals import EVAL_IDENTITY, EvalRun, run_eval, transcript_tool_calls
from support.factory import SKILL_PROMPT

# A skill inlined into the system prompt (`<skill name=...>`); `<skills_index>` is expected.
_INLINED_SKILL = re.compile(r"<skill[\s>/]")
_DISCOVERY = {"initialize", "notifications/initialized", "tools/list"}


def _tool_calls(run: EvalRun, name: str) -> list[dict]:
    calls = [tc for tc in transcript_tool_calls(run) if tc["name"] == name]
    assert calls, f"no {name} call in the transcripts: {[s['transcript'] for s in run.sessions]}"
    return calls


def _assert_done(run: EvalRun) -> None:
    assert run.status["status"] == "DONE", (run.status, run.runner_output)
    assert len(run.sessions) == 1, run.sessions


def test_real_eval(real_eval: EvalRun):
    run, seeded = real_eval, real_eval.seeded

    backend = run.export["mcp_backends"][seeded.backend_name]
    assert {t["name"] for t in backend["tools"]} == {
        seeded.search_tool, f"{seeded.backend_name}__get_project"}, backend

    tool_calls = [c for c in run.mcp_calls if c["method"] == "tools/call"]
    assert tool_calls, run.mcp_calls
    assert all(c["headers"].get("x-test-identity") == EVAL_IDENTITY for c in tool_calls), \
        tool_calls

    _assert_done(run)
    assert run.status["score"] == 0.5, run.status

    searches = _tool_calls(run, seeded.search_tool)
    assert all(c["source"] == "real" for c in searches), searches
    assert any(f"as {EVAL_IDENTITY}" in json.dumps(c["result"]) for c in searches), searches
    skills = _tool_calls(run, "Skill")
    assert all(c["source"] == "skill" for c in skills), skills
    assert all(SKILL_PROMPT in json.dumps(c["result"]) for c in skills), skills

    assert run.target_requests, "fake-target saw no requests"
    for req in run.target_requests:
        system = [m["content"] for m in req["messages"] if m.get("role") == "system"]
        assert len(system) == 1, req["messages"]
        assert not _INLINED_SKILL.search(system[0]), system[0]
        assert SKILL_PROMPT not in system[0], system[0]


def test_mock_eval(factory, api, db, llm, mcp, seeded):
    dataset_version_id = factory.dataset(seeded)
    run = run_eval(factory, api, db, llm, mcp, seeded, dataset_version_id, mock=True)

    methods = {c["method"] for c in run.mcp_calls}
    assert "tools/list" in methods, run.mcp_calls
    jsonrpc = {m for m in methods if not m.isupper()}  # skip transport-level GET/DELETE
    assert jsonrpc <= _DISCOVERY, run.mcp_calls

    searches = _tool_calls(run, seeded.search_tool)
    assert all(c["source"] == "replayed" for c in searches), searches
    _assert_done(run)

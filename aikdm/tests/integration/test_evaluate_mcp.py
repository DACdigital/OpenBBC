"""End-to-end run_eval in real mode against a live in-process MCP server:
the model (stubbed LiteLLM) is offered kb__* tools, calls one, the call
reaches the server with the exported headers, and the live result lands in
the transcript the judge sees (spec eval-mcp-tools Acceptance 2/6)."""

import json
from types import SimpleNamespace as NS

from mcp.server.mcpserver import Context, MCPServer

from aikdm import models
from aikdm.config import Settings
from aikdm.eval import judge as judge_mod
from aikdm.eval import orchestrator as orch_mod
from aikdm.eval import simulator as sim_mod
from aikdm.eval import target as target_mod
from aikdm.eval.schemas import EvalInput, Judgment
from tests.mcp_server import serve as _serve


def _input(url: str) -> EvalInput:
    return EvalInput.model_validate({
        "schema_version": "eval-input-v1", "eval_id": "e", "mock_mcp_tools": False,
        "mcp_backends": {"kb": {
            "url": url, "transport": "streamable_http", "headers": {"X-Identity": "eval-bot"},
            "tools": [{"name": "kb__search", "description": "Search",
                       "input_schema": {"type": "object",
                                        "properties": {"q": {"type": "string"}}}}],
        }},
        "agent_version": {"id": "av", "bundle": {"main_prompt": "p", "tools": []}},
        "dataset_version": {"id": "dv", "sessions": [{
            "session_id": "s1",
            "transcript": [
                {"message_id": "u", "role": "user", "content": "find pricing"},
                {"message_id": "a", "role": "assistant", "content": "found"},
            ],
            "criteria": [{"message_id": "a", "rating": "up", "items": ["uses the KB"]}],
        }]},
    })


async def test_real_mode_eval_calls_live_mcp_tool(monkeypatch):
    seen_headers = []
    srv = MCPServer("kb")

    @srv.tool()
    def search(q: str, ctx: Context) -> str:
        req = ctx.request_context.request
        seen_headers.append(req.headers.get("x-identity") if req else None)
        return f"live hits for {q}"

    url, server, t = _serve(srv)
    offered = []
    turns = iter([
        NS(content="", tool_calls=[NS(id="c1", function=NS(
            name="kb__search", arguments=json.dumps({"q": "pricing"})))]),
        NS(content="done", tool_calls=[]),
    ])

    async def fake_completion(**kw):
        offered.append([t["function"]["name"] for t in kw.get("tools") or []])
        return NS(choices=[NS(message=next(turns))], usage=None)

    judged = {}

    async def fake_judge(*, agent, context, turn_under_test, criteria_items):
        judged["turn"] = turn_under_test
        return judge_mod.JudgeResult(
            judgments=[Judgment(message_id="a", criterion="uses the KB", passed=True)],
            passed=1, total=1, tokens_in=0, tokens_out=0)

    monkeypatch.setattr(target_mod.litellm, "acompletion", fake_completion)
    monkeypatch.setattr(judge_mod, "judge_turn", fake_judge)
    monkeypatch.setattr(models, "build_model", lambda role, settings: object())
    monkeypatch.setattr(sim_mod, "build_simulator_agent", lambda m: object())
    monkeypatch.setattr(judge_mod, "build_judge_agent", lambda m: object())
    try:
        res = await orch_mod.run_eval(_input(url), Settings())
    finally:
        server.should_exit = True
        t.join(timeout=5)

    assert offered[0] == ["kb__search"]
    assert seen_headers == ["eval-bot"]
    call = res.sessions[0].transcript[1]["tool_calls"][0]
    assert call["source"] == "real"
    assert call["result"]["text"] == "live hits for pricing"
    assert "live hits for pricing" in json.dumps(judged["turn"])
    assert res.passed_criteria == 1

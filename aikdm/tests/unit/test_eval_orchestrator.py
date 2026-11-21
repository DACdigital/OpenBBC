from aikdm import models
from aikdm.config import Settings
from aikdm.eval import judge as judge_mod
from aikdm.eval import orchestrator as orch_mod
from aikdm.eval import runner as runner_mod
from aikdm.eval import simulator as sim_mod
from aikdm.eval.schemas import EvalInput


def _input():
    return EvalInput.model_validate(
        {
            "schema_version": "eval-input-v1",
            "eval_id": "e",
            "agent_version": {
                "id": "av",
                "bundle": {"main_prompt": "p", "tools": [], "skills": []},
            },
            "dataset_version": {
                "id": "dv",
                "sessions": [
                    {
                        "session_id": "s1",
                        "transcript": [
                            {"message_id": "u", "role": "user", "content": "hi"},
                            {"message_id": "a", "role": "assistant", "content": "yo"},
                        ],
                        "criteria": [{"message_id": "a", "rating": "up", "items": ["c1", "c2"]}],
                    }
                ],
            },
        }
    )


def _stub(monkeypatch):
    monkeypatch.setattr(models, "build_model", lambda role, settings: object())
    monkeypatch.setattr(sim_mod, "build_simulator_agent", lambda m: object())
    monkeypatch.setattr(judge_mod, "build_judge_agent", lambda m: object())


async def test_modes_passed_through_and_recorded(monkeypatch):
    _stub(monkeypatch)
    seen = {}

    async def fake_run_session(**kw):
        seen.update(kw)
        return runner_mod._outcome(kw["session"], [], [], 0, 0)

    monkeypatch.setattr(runner_mod, "run_session", fake_run_session)
    s = Settings(eval_user_turn_mode="verbatim", eval_history_mode="reference")
    res = await orch_mod.run_eval(_input(), s)
    assert seen["user_turn_mode"] == "verbatim" and seen["history_mode"] == "reference"
    assert res.aikdm_meta.history_mode == "reference"
    assert res.aikdm_meta.user_turn_mode == "verbatim"


async def test_session_error_emits_inconclusive_per_criterion(monkeypatch):
    _stub(monkeypatch)

    async def boom(**kw):
        raise ValueError("compile blew up")

    monkeypatch.setattr(runner_mod, "run_session", boom)
    res = await orch_mod.run_eval(_input(), Settings())
    sess = res.sessions[0]
    assert sess.total_criteria == 2 and sess.passed_criteria == 0
    assert [j.criterion for j in sess.judgments] == ["c1", "c2"]
    assert all(
        j.inconclusive and j.reason == "session error: compile blew up" for j in sess.judgments
    )


def _mcp_input(mock: bool):
    raw = _input().model_dump()
    raw["mock_mcp_tools"] = mock
    raw["mcp_backends"] = {"kb": {
        "url": "" if mock else "http://127.0.0.1:1/mcp",
        "tools": [{"name": "kb__search", "input_schema": {"type": "object"}}],
    }}
    return EvalInput.model_validate(raw)


async def test_mock_mode_offers_mcp_tools_and_never_builds_an_mcp_client(monkeypatch):
    _stub(monkeypatch)
    seen = {}

    def no_client(*a, **kw):
        raise AssertionError("MCPToolCaller built in mock mode")

    monkeypatch.setattr(orch_mod, "MCPToolCaller", no_client)

    async def fake_run_session(**kw):
        seen.update(kw)
        return runner_mod._outcome(kw["session"], [], [], 0, 0)

    monkeypatch.setattr(runner_mod, "run_session", fake_run_session)
    await orch_mod.run_eval(_mcp_input(mock=True), Settings())
    assert "kb__search" in seen["tool_caller"].tools_schema
    assert seen["mcp_backends"]["kb"]["tools"][0]["name"] == "kb__search"


async def test_real_mode_routes_through_a_per_session_mcp_caller_closed_at_end(monkeypatch):
    _stub(monkeypatch)
    events = []

    class FakeMCP:
        def __init__(self, backends):
            events.append(("new", sorted(backends)))

        def owns(self, name):
            return name.startswith("kb__")

        async def call(self, name, args):
            return {"source": "real", "result": {"ok": name}}

        async def aclose(self):
            events.append(("close",))

    monkeypatch.setattr(orch_mod, "MCPToolCaller", FakeMCP)

    async def fake_run_session(**kw):
        out = await kw["tool_caller"].call("kb__search", {})
        events.append(("call", out["result"]))
        return runner_mod._outcome(kw["session"], [], [], 0, 0)

    monkeypatch.setattr(runner_mod, "run_session", fake_run_session)
    await orch_mod.run_eval(_mcp_input(mock=False), Settings())
    assert events == [("new", ["kb"]), ("call", {"ok": "kb__search"}), ("close",)]


async def test_no_mcp_backends_real_mode_uses_plain_http_caller(monkeypatch):
    _stub(monkeypatch)
    seen = {}

    async def fake_run_session(**kw):
        seen.update(kw)
        return runner_mod._outcome(kw["session"], [], [], 0, 0)

    monkeypatch.setattr(runner_mod, "run_session", fake_run_session)
    inp = _input().model_copy(update={"mock_mcp_tools": False})
    await orch_mod.run_eval(inp, Settings())
    assert type(seen["tool_caller"]).__name__ == "RealHTTPToolCaller"
    assert seen["mcp_backends"] == {}

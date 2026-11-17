# tests/unit/test_target.py
import json
from types import SimpleNamespace as NS

from aikdm.eval import target as target_mod

BUDGET = "(target agent exceeded tool-call budget)"


def _resp(content="", tool_calls=None):
    msg = NS(content=content, tool_calls=tool_calls or [])
    return NS(choices=[NS(message=msg)], usage=NS(prompt_tokens=3, completion_tokens=2))


def _tc(cid, name, args):
    return NS(id=cid, function=NS(name=name, arguments=json.dumps(args)))


class _Caller:
    def call(self, name, args):
        return {"source": "mocked", "result": {"name": name}}


async def test_returns_span_without_mutating_history(monkeypatch):
    calls = []
    responses = iter([_resp(tool_calls=[_tc("p1", "lookup", {"id": 1})]), _resp("final")])

    async def fake(**kw):
        calls.append(kw["messages"])
        return next(responses)

    monkeypatch.setattr(target_mod.litellm, "acompletion", fake)
    history = [{"role": "user", "content": "hi"}]
    snapshot = json.loads(json.dumps(history))
    t = await target_mod.target_turn(model="m", system_prompt="sys", tools_spec=[],
                                     history=history, tool_caller=_Caller())
    assert history == snapshot
    assert t.span == [
        {"role": "assistant", "content": "",
         "tool_calls": [{"name": "lookup", "args": {"id": 1},
                         "result": {"name": "lookup"}, "source": "mocked"}]},
        {"role": "assistant", "content": "final"},
    ]
    assert t.budget_exceeded is False
    assert t.tool_call_count == 1
    # second completion saw the provider ids paired correctly
    second = calls[1]
    assert second[-2]["tool_calls"][0]["id"] == "p1"
    assert second[-1] == {"role": "tool", "tool_call_id": "p1",
                          "content": json.dumps({"name": "lookup"})}


async def test_history_with_tool_round_is_rendered_well_formed(monkeypatch):
    seen = {}

    async def fake(**kw):
        seen["messages"] = kw["messages"]
        return _resp("ok")

    monkeypatch.setattr(target_mod.litellm, "acompletion", fake)
    history = [
        {"role": "user", "content": "q1"},
        {"role": "assistant", "content": "", "tool_calls": [
            {"name": "lookup", "args": {}, "result": 1, "source": "mocked"}]},
        {"role": "assistant", "content": "a1"},
        {"role": "user", "content": "q2"},
    ]
    await target_mod.target_turn(model="m", system_prompt="sys", tools_spec=[],
                                 history=history, tool_caller=_Caller())
    msgs = seen["messages"]
    assert msgs[0] == {"role": "system", "content": "sys"}
    assert msgs[2]["tool_calls"][0]["function"]["name"] == "lookup"
    assert msgs[3]["role"] == "tool" and msgs[3]["tool_call_id"] == msgs[2]["tool_calls"][0]["id"]
    assert "name" not in msgs[2]["tool_calls"][0]  # no aikdm-shaped entries leak


async def test_budget_exceeded_flag(monkeypatch):
    async def fake(**kw):
        return _resp(tool_calls=[_tc("p", "loop", {})])

    monkeypatch.setattr(target_mod.litellm, "acompletion", fake)
    t = await target_mod.target_turn(model="m", system_prompt="s", tools_spec=[],
                                     history=[{"role": "user", "content": "x"}],
                                     tool_caller=_Caller())
    assert t.budget_exceeded is True
    assert t.span[-1] == {"role": "assistant", "content": BUDGET}
    assert len(t.span) == 9  # 8 tool rounds + sentinel

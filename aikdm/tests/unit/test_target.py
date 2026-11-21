# tests/unit/test_target.py
import json
from types import SimpleNamespace as NS

from aikdm.eval import target as target_mod


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
    t = await target_mod.target_turn(
        model="m", system_prompt="sys", tools_spec=[], history=history, tool_caller=_Caller()
    )
    assert history == snapshot
    assert t.span == [
        {
            "role": "assistant",
            "content": "",
            "tool_calls": [
                {
                    "name": "lookup",
                    "args": {"id": 1},
                    "result": {"name": "lookup"},
                    "source": "mocked",
                }
            ],
        },
        {"role": "assistant", "content": "final"},
    ]
    assert t.budget_exceeded is False
    assert t.tool_call_count == 1
    # second completion saw the provider ids paired correctly
    second = calls[1]
    assert second[-2]["tool_calls"][0]["id"] == "p1"
    assert second[-1] == {
        "role": "tool",
        "tool_call_id": "p1",
        "content": json.dumps({"name": "lookup"}),
    }


async def test_history_with_tool_round_is_rendered_well_formed(monkeypatch):
    seen = {}

    async def fake(**kw):
        seen["messages"] = kw["messages"]
        return _resp("ok")

    monkeypatch.setattr(target_mod.litellm, "acompletion", fake)
    history = [
        {"role": "user", "content": "q1"},
        {
            "role": "assistant",
            "content": "",
            "tool_calls": [{"name": "lookup", "args": {}, "result": 1, "source": "mocked"}],
        },
        {"role": "assistant", "content": "a1"},
        {"role": "user", "content": "q2"},
    ]
    await target_mod.target_turn(
        model="m", system_prompt="sys", tools_spec=[], history=history, tool_caller=_Caller()
    )
    msgs = seen["messages"]
    assert msgs[0] == {"role": "system", "content": "sys"}
    assert msgs[2]["tool_calls"][0]["function"]["name"] == "lookup"
    assert msgs[3]["role"] == "tool" and msgs[3]["tool_call_id"] == msgs[2]["tool_calls"][0]["id"]
    assert "name" not in msgs[2]["tool_calls"][0]  # no aikdm-shaped entries leak


async def test_budget_exceeded_flag(monkeypatch):
    async def fake(**kw):
        return _resp(tool_calls=[_tc("p", "loop", {})])

    monkeypatch.setattr(target_mod.litellm, "acompletion", fake)
    t = await target_mod.target_turn(
        model="m",
        system_prompt="s",
        tools_spec=[],
        history=[{"role": "user", "content": "x"}],
        tool_caller=_Caller(),
    )
    assert t.budget_exceeded is True
    assert t.span[-1] == {"role": "assistant", "content": target_mod.BUDGET_SENTINEL}
    assert len(t.span) == 9  # 8 tool rounds + sentinel


async def test_invalid_tool_args_become_tool_error(monkeypatch):
    calls = []
    bad = NS(id="p1", function=NS(name="lookup", arguments="{not json"))
    responses = iter([_resp(tool_calls=[bad]), _resp("recovered")])

    async def fake(**kw):
        calls.append(list(kw["messages"]))
        return next(responses)

    class _NeverCalled:
        def call(self, name, args):
            raise AssertionError("tool_caller must not be called on invalid JSON")

    monkeypatch.setattr(target_mod.litellm, "acompletion", fake)
    t = await target_mod.target_turn(
        model="m",
        system_prompt="s",
        tools_spec=[],
        history=[{"role": "user", "content": "x"}],
        tool_caller=_NeverCalled(),
    )
    call = t.span[0]["tool_calls"][0]
    assert call["name"] == "lookup"
    assert call["args"] == {"_raw": "{not json"}
    assert call["source"] == "error"
    assert call["result"]["error"].startswith("invalid JSON arguments: ")
    assert t.span[-1] == {"role": "assistant", "content": "recovered"}
    assert t.budget_exceeded is False
    tool_msg = calls[1][-1]
    assert tool_msg["role"] == "tool" and tool_msg["tool_call_id"] == "p1"
    assert json.loads(tool_msg["content"]) == call["result"]


KB = {"kb": {"tools": [
    {"name": "kb__search", "description": "Search",
     "input_schema": {"type": "object", "properties": {"q": {"type": "string"}}},
     "output_schema": {"type": "object", "properties": {"hits": {"type": "integer"}}}},
    {"name": "kb__raw", "description": "", "input_schema": {"type": "object"}},
]}}


def test_build_tool_specs_appends_mcp_tools_exactly_as_exported():
    bundle = {"tools": [{"name": "lookup", "description": "d"}]}
    tools, schemas = target_mod.build_tool_specs(bundle, KB)
    assert [t["function"]["name"] for t in tools] == ["lookup", "kb__search", "kb__raw"]
    assert tools[1]["function"] == {
        "name": "kb__search", "description": "Search",
        "parameters": {"type": "object", "properties": {"q": {"type": "string"}}},
    }
    assert schemas["kb__search"]["response_shape"] == {
        "type": "object", "properties": {"hits": {"type": "integer"}}}
    assert schemas["kb__raw"]["response_shape"] is None


def test_build_tool_specs_without_mcp_backends_is_unchanged():
    bundle = {"tools": [{"name": "lookup", "description": "d"}]}
    assert target_mod.build_tool_specs(bundle) == target_mod.build_tool_specs(bundle, {})
    assert target_mod.build_tool_specs(bundle, None)[0][0]["function"]["name"] == "lookup"


async def test_async_tool_caller_is_awaited(monkeypatch):
    responses = iter([_resp(tool_calls=[_tc("p1", "kb__search", {"q": "x"})]), _resp("done")])

    async def fake(**kw):
        return next(responses)

    class AsyncCaller:
        async def call(self, name, args):
            return {"source": "real", "result": {"text": f"{name}:{args['q']}"}}

    monkeypatch.setattr(target_mod.litellm, "acompletion", fake)
    t = await target_mod.target_turn(model="m", system_prompt="s", tools_spec=[],
                                     history=[{"role": "user", "content": "hi"}],
                                     tool_caller=AsyncCaller())
    assert t.span[0]["tool_calls"][0]["result"] == {"text": "kb__search:x"}
    assert t.span[0]["tool_calls"][0]["source"] == "real"


def test_build_tool_specs_offers_skill_first_then_bundle_then_mcp_tools():
    bundle = {
        "skills": [{"name": "draft", "prompt": "DRAFT"}, {"name": "empty", "prompt": ""}],
        "tools": [{"name": "lookup", "description": "d"}],
    }
    mcp = {"kb": {"tools": [{"name": "kb__search", "description": "s",
                             "input_schema": {"type": "object"}}]}}
    tools, schema = target_mod.build_tool_specs(bundle, mcp)
    assert [t["function"]["name"] for t in tools] == ["Skill", "lookup", "kb__search"]
    assert tools[0]["function"]["parameters"]["properties"]["name"]["enum"] == ["draft"]
    assert "Skill" not in schema and "kb__search" in schema


async def test_skill_caller_answers_skill_and_passes_mcp_calls_to_async_router():
    class _Router:
        def __init__(self):
            self.seen = []

        async def call(self, name, args):
            self.seen.append(name)
            return {"source": "real", "result": {"text": "hit"}}

    router = _Router()
    bundle = {"skills": [{"name": "draft", "prompt": "DRAFT"}]}
    caller = target_mod.SkillToolCaller(bundle, router)
    assert caller.call("Skill", {"name": "draft"}) == {
        "source": "skill", "result": {"prompt": "DRAFT"}}
    assert await caller.call("kb__search", {"query": "q"}) == {
        "source": "real", "result": {"text": "hit"}}
    assert router.seen == ["kb__search"]

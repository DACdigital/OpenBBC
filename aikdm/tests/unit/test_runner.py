# tests/unit/test_runner.py
from types import SimpleNamespace as NS

import pytest
from pydantic import ValidationError

from aikdm.eval import judge as judge_mod
from aikdm.eval import runner as runner_mod
from aikdm.eval import simulator as sim_mod
from aikdm.eval import target as target_mod
from aikdm.eval.schemas import (
    InputCriterion,
    InputMessage,
    InputSession,
    InputToolCall,
    Judgment,
)

BUNDLE = {"main_prompt": "sys", "tools": [{"name": "lookup", "description": ""}], "skills": []}


class _Caller:
    def call(self, name, args):
        return {"source": "mocked", "result": {}}


def _m(mid, role, content="x", tool_calls=None):
    return InputMessage(message_id=mid, role=role, content=content, tool_calls=tool_calls or [])


def _two_turn_session(criteria=None):
    return InputSession(session_id="s", transcript=[
        _m("u1", "user", "first?"), _m("a1", "assistant", "ref reply 1"),
        _m("u2", "user", "second"), _m("a2", "assistant", "ref reply 2"),
    ], criteria=criteria if criteria is not None else [
        InputCriterion(message_id="a1", rating="up", items=["t1 crit"]),
        InputCriterion(message_id="a2", rating="up", items=["t2 crit"]),
    ])


@pytest.fixture
def rec(monkeypatch):
    """Records target/judge/simulator calls; behaviour configurable per test."""
    state = {"target": [], "judge": [], "sim": [], "target_fail": set(), "budget": set(),
             "judge_fail": set(), "sim_out": None, "spans": {}}

    async def fake_target(*, model, system_prompt, tools_spec, history, tool_caller):
        k = len(state["target"]) + 1
        state["target"].append([dict(e) for e in history])
        if k in state["target_fail"]:
            raise RuntimeError(f"boom{k}")
        if k in state["budget"]:
            return target_mod.TargetTurn(
                span=[{"role": "assistant", "content": target_mod.BUDGET_SENTINEL}],
                tokens_in=1, tokens_out=1, tool_call_count=8, budget_exceeded=True)
        return target_mod.TargetTurn(
            span=state["spans"].get(k) or [{"role": "assistant", "content": f"gen reply {k}?"}],
            tokens_in=1, tokens_out=1, tool_call_count=0)

    async def fake_judge(*, agent, context, turn_under_test, criteria_items):
        state["judge"].append({"context": context, "turn": turn_under_test,
                               "items": criteria_items})
        if len(state["judge"]) in state["judge_fail"]:
            raise judge_mod.JudgeError("expected 1 judgments, got 0")
        js = [Judgment(message_id=c.message_id, criterion=c.criterion, passed=True,
                       reason="ok") for c in criteria_items]
        return judge_mod.JudgeResult(judgments=js, passed=len(js), total=len(js),
                                     tokens_in=1, tokens_out=1)

    async def fake_sim(*, agent, reference_transcript, simulated_so_far, intent):
        state["sim"].append(intent)
        out = state["sim_out"]
        if isinstance(out, Exception):
            raise out
        return sim_mod.SimulatorTurn(content=out if out is not None else f"P:{intent}",
                                     tokens_in=1, tokens_out=1)

    monkeypatch.setattr(target_mod, "target_turn", fake_target)
    monkeypatch.setattr(judge_mod, "judge_turn", fake_judge)
    monkeypatch.setattr(sim_mod, "next_user_turn", fake_sim)
    return state


async def _run(session, **kw):
    return await runner_mod.run_session(
        session=session, bundle=BUNDLE, simulator_agent=object(), judge_agent=object(),
        target_model="m", tool_caller=_Caller(), **kw)


async def test_one_turn_makes_one_target_call(rec):
    s = InputSession(session_id="s", transcript=[_m("u1", "user", "hi"),
                                                 _m("a1", "assistant")],
                     criteria=[InputCriterion(message_id="a1", rating="up", items=["c"])])
    out = await _run(s)
    assert len(rec["target"]) == 1
    assert [e["role"] for e in out.result.transcript] == ["user", "assistant"]
    assert out.result.score == 1.0


@pytest.mark.parametrize("user_mode,history_mode", [
    ("verbatim", "simulated"), ("paraphrase", "simulated"), ("verbatim", "reference")])
async def test_n_turns_n_calls_every_mode(rec, user_mode, history_mode):
    await _run(_two_turn_session(), user_turn_mode=user_mode, history_mode=history_mode)
    assert len(rec["target"]) == 2


async def test_verbatim_sends_reference_text_even_after_question(rec):
    await _run(_two_turn_session())
    # gen reply 1 ends with "?" — turn 2 still sends reference turn 2 verbatim
    assert rec["target"][1][-1] == {"role": "user", "content": "second"}
    assert rec["sim"] == []


async def test_paraphrase_called_exactly_n_times(rec):
    await _run(_two_turn_session(), user_turn_mode="paraphrase")
    assert rec["sim"] == ["first?", "second"]
    assert rec["target"][0][-1]["content"] == "P:first?"


def _validation_error():
    try:
        sim_mod._SimulatorOutput.model_validate_json("nope")
    except ValidationError as e:
        return e
    raise AssertionError("expected ValidationError")


@pytest.mark.parametrize("bad", ["", RuntimeError("sim down"), _validation_error()])
async def test_paraphrase_failure_falls_back_to_verbatim(rec, bad):
    rec["sim_out"] = bad
    await _run(_two_turn_session(), user_turn_mode="paraphrase")
    assert rec["target"][0][-1]["content"] == "first?"


async def test_simulated_history_contains_generated_span(rec):
    await _run(_two_turn_session())
    contents = [e["content"] for e in rec["target"][1]]
    assert contents == ["first?", "gen reply 1?", "second"]


async def test_reference_history_contains_reference_not_generated(rec):
    await _run(_two_turn_session(), history_mode="reference")
    contents = [e["content"] for e in rec["target"][1]]
    assert contents == ["first?", "ref reply 1", "second"]


async def test_reference_history_tool_calls_and_undeclared(rec):
    s = InputSession(session_id="s", transcript=[
        _m("u1", "user", "q"),
        _m("a1t", "assistant", "", [InputToolCall(name="lookup", args={}, result=1),
                                    InputToolCall(name="gone", args={}, result=2)]),
        _m("t1", "tool", "r"), _m("a1", "assistant", "ans"),
        _m("u2", "user", "q2"), _m("a2", "assistant", "ans2"),
    ], criteria=[])
    await _run(s, history_mode="reference")
    hist = rec["target"][1]
    assert hist[1]["tool_calls"] == [{"name": "lookup", "args": {}, "result": 1}]
    assert "[called tool gone" in hist[1]["content"]
    assert all(e["role"] != "tool" for e in hist)


async def test_judge_per_turn_and_later_turn_absent(rec):
    await _run(_two_turn_session())
    assert len(rec["judge"]) == 2
    first = rec["judge"][0]
    assert [c.criterion for c in first["items"]] == ["t1 crit"]
    assert first["context"] == []
    assert first["turn"][-1]["content"] == "gen reply 1?"
    blob = repr(first)
    assert "gen reply 2" not in blob and "second" not in blob


async def test_judge_context_in_reference_mode_is_reference(rec):
    await _run(_two_turn_session(), history_mode="reference")
    assert [e["content"] for e in rec["judge"][1]["context"]] == ["first?", "ref reply 1"]


async def test_target_error_simulated_makes_both_inconclusive(rec):
    rec["target_fail"] = {1}
    out = await _run(_two_turn_session())
    js = out.result.judgments
    assert [j.inconclusive for j in js] == [True, True]
    assert js[0].reason == "turn 1 target error: boom1"
    assert js[1].reason == "turn 2 not reached: turn 1 target error: boom1"
    assert rec["judge"] == [] and len(rec["target"]) == 1
    assert out.result.passed_criteria == 0 and out.result.total_criteria == 2


async def test_target_error_reference_mode_still_judges_turn_2(rec):
    rec["target_fail"] = {1}
    out = await _run(_two_turn_session(), history_mode="reference")
    js = out.result.judgments
    assert js[0].inconclusive and not js[1].inconclusive and js[1].passed


async def test_budget_turn_is_judged_and_later_not_reached(rec):
    rec["budget"] = {1}
    out = await _run(_two_turn_session())
    js = out.result.judgments
    assert not js[0].inconclusive
    assert js[1].reason == "turn 2 not reached: turn 1 exceeded tool-call budget"


async def test_judge_error_keeps_other_turn(rec):
    rec["judge_fail"] = {2}
    out = await _run(_two_turn_session())
    js = out.result.judgments
    assert js[0].passed and not js[0].inconclusive
    assert js[1].inconclusive and js[1].reason == "judge error: expected 1 judgments, got 0"
    assert out.result.score == 0.5


async def test_unmapped_judged_with_prefix_and_can_pass(rec):
    s = _two_turn_session([InputCriterion(message_id="zzz", rating="up", items=["c"])])
    out = await _run(s)
    j = out.result.judgments[0]
    assert j.passed and not j.inconclusive
    assert j.reason.startswith("[unmapped message_id zzz, judged against last turn] ")
    assert rec["judge"][0]["turn"][-1]["content"] == "gen reply 2?"


async def test_empty_script_all_inconclusive(rec):
    s = InputSession(session_id="s", transcript=[],
                     criteria=[InputCriterion(message_id="a", rating="up", items=["c1", "c2"])])
    out = await _run(s)
    assert rec["target"] == []
    assert out.result.total_criteria == 2 and out.result.passed_criteria == 0
    assert {j.reason for j in out.result.judgments} == {"no user turns in reference transcript"}


async def test_no_criteria_scores_one(rec):
    out = await _run(_two_turn_session([]))
    assert out.result.score == 1.0 and out.result.total_criteria == 0


async def test_tool_calls_appear_in_turn_under_test(rec):
    rec["spans"] = {1: [
        {"role": "assistant", "content": "",
         "tool_calls": [{"name": "lookup", "args": {}, "result": 1, "source": "mocked"}]},
        {"role": "assistant", "content": "done"}]}
    await _run(_two_turn_session())
    turn = rec["judge"][0]["turn"]
    assert turn[1]["tool_calls"] == [
        {"name": "lookup", "args": {}, "result": 1, "source": "mocked"}]
    assert turn[-1]["content"] == "done"


async def test_tool_round_replayed_well_formed_on_next_turn(monkeypatch):
    calls = []
    responses = iter([
        NS(choices=[NS(message=NS(content="", tool_calls=[NS(
            id="p1", function=NS(name="lookup", arguments="{}"))]))], usage=None),
        NS(choices=[NS(message=NS(content="answer 1", tool_calls=[]))], usage=None),
        NS(choices=[NS(message=NS(content="answer 2", tool_calls=[]))], usage=None),
    ])

    async def fake_acompletion(**kw):
        calls.append([dict(m) for m in kw["messages"]])
        return next(responses)

    async def fake_judge(*, agent, context, turn_under_test, criteria_items):
        js = [Judgment(message_id=c.message_id, criterion=c.criterion, passed=True,
                       reason="ok") for c in criteria_items]
        return judge_mod.JudgeResult(judgments=js, passed=len(js), total=len(js),
                                     tokens_in=0, tokens_out=0)

    monkeypatch.setattr(target_mod.litellm, "acompletion", fake_acompletion)
    monkeypatch.setattr(judge_mod, "judge_turn", fake_judge)
    out = await _run(_two_turn_session())
    assert out.result.score == 1.0
    assert len(calls) == 3  # turn 1: tool round + answer; turn 2: direct answer
    msgs = calls[2]
    idx = next(i for i, m in enumerate(msgs) if m.get("tool_calls"))
    asst, tool = msgs[idx], msgs[idx + 1]
    assert asst["role"] == "assistant"
    assert asst["tool_calls"][0]["function"]["name"] == "lookup"
    assert tool["role"] == "tool" and tool["tool_call_id"] == asst["tool_calls"][0]["id"]
    assert all("name" not in tc and "args" not in tc
               for m in msgs for tc in m.get("tool_calls", []))
    assert [m["role"] for m in msgs] == [
        "system", "user", "assistant", "tool", "assistant", "user"]


async def test_criteria_on_turns_1_and_3_only(rec):
    s = InputSession(session_id="s", transcript=[
        _m("u1", "user", "one"), _m("a1", "assistant", "ref 1"),
        _m("u2", "user", "two"), _m("a2", "assistant", "ref 2"),
        _m("u3", "user", "three"), _m("a3", "assistant", "ref 3"),
    ], criteria=[
        InputCriterion(message_id="a1", rating="up", items=["t1 crit"]),
        InputCriterion(message_id="a3", rating="up", items=["t3 crit"]),
    ])
    out = await _run(s)
    assert len(rec["target"]) == 3
    assert len(rec["judge"]) == 2
    third = rec["judge"][1]
    assert [c.criterion for c in third["items"]] == ["t3 crit"]
    assert third["context"] == [
        {"role": "user", "content": "one"}, {"role": "assistant", "content": "gen reply 1?"},
        {"role": "user", "content": "two"}, {"role": "assistant", "content": "gen reply 2?"},
    ]
    assert third["turn"] == [{"role": "user", "content": "three"},
                             {"role": "assistant", "content": "gen reply 3?"}]
    js = out.result.judgments
    assert [(j.message_id, j.criterion, j.reason) for j in js] == [
        ("a1", "t1 crit", "ok"), ("a3", "t3 crit", "ok")]

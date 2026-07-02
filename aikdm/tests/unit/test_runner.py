import asyncio

from aikdm.eval import judge as judge_mod
from aikdm.eval import runner as runner_mod
from aikdm.eval import simulator as sim_mod
from aikdm.eval import target as target_mod
from aikdm.eval.schemas import InputCriterion, InputMessage, InputSession, Judgment


def test_run_session_stops_when_simulator_says_stop(monkeypatch):
    async def fake_next(*, agent, reference_transcript, simulated_so_far):
        return sim_mod.SimulatorTurn(content="", stop=True, tokens_in=1, tokens_out=1)
    async def fake_target(*, model, system_prompt, tools_spec, conversation, tool_mock):
        raise AssertionError("should not be called when simulator stops immediately")
    async def fake_judge(*, agent, transcript, criteria_items):
        return judge_mod.JudgeResult(
            judgments=[Judgment(message_id="m", criterion="c", passed=True, reason="ok")],
            passed=1, total=1, tokens_in=2, tokens_out=2,
        )
    monkeypatch.setattr(sim_mod, "next_user_turn", fake_next)
    monkeypatch.setattr(target_mod, "target_turn", fake_target)
    monkeypatch.setattr(judge_mod, "judge_transcript", fake_judge)
    session = InputSession(
        session_id="s-1", title="x",
        transcript=[InputMessage(message_id="m", role="user", content="hi")],
        criteria=[InputCriterion(message_id="m", rating="up", items=["c"])],
    )
    outcome = asyncio.run(runner_mod.run_session(
        session=session, bundle={"main_prompt": "sys", "tools": [], "skills": []},
        simulator_agent=object(), judge_agent=object(), target_model="claude-haiku-4-5",
    ))
    assert outcome.result.total_criteria == 1
    assert outcome.result.passed_criteria == 1
    assert outcome.result.score == 1.0

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

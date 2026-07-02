import json
from pathlib import Path

from aikdm.eval import runner as runner_mod
from aikdm.eval import simulator as sim_mod
from aikdm.eval import target as target_mod
from aikdm.eval import judge as judge_mod
from aikdm.eval.schemas import Judgment


def test_evaluate_cli_produces_result_shape(monkeypatch, tmp_path):
    async def fake_next(*, agent, reference_transcript, simulated_so_far):
        if not simulated_so_far:
            return sim_mod.SimulatorTurn(content="hi", stop=False, tokens_in=0, tokens_out=0)
        return sim_mod.SimulatorTurn(content="", stop=True, tokens_in=0, tokens_out=0)

    async def fake_target(*, model, system_prompt, tools_spec, conversation, tool_mock):
        return target_mod.TargetTurn(
            assistant_message={"role": "assistant", "content": "hello, how can I help?"},
            tokens_in=1, tokens_out=1, tool_call_count=0,
        )

    async def fake_judge(*, agent, transcript, criteria_items):
        return judge_mod.JudgeResult(
            judgments=[Judgment(message_id="m-a-1", criterion="Greets politely", passed=True, reason="ok")],
            passed=1, total=1, tokens_in=1, tokens_out=1,
        )

    monkeypatch.setattr(sim_mod, "next_user_turn", fake_next)
    monkeypatch.setattr(target_mod, "target_turn", fake_target)
    monkeypatch.setattr(judge_mod, "judge_transcript", fake_judge)

    # Stub build_model so we don't need API keys.
    from aikdm import models
    monkeypatch.setattr(models, "build_model", lambda role, settings: object())

    # Stub agent builders so ADK LlmAgent construction (which validates the
    # model field) doesn't run against the placeholder object above. The
    # fake sim/judge callables ignore the agent argument anyway.
    monkeypatch.setattr(sim_mod, "build_simulator_agent", lambda model: object())
    monkeypatch.setattr(judge_mod, "build_judge_agent", lambda model: object())

    # Stub load_settings to skip env checks.
    from aikdm import cli
    from aikdm.config import Settings
    monkeypatch.setattr(cli, "load_settings", lambda: Settings(
        model_generator="claude-haiku-4-5", model_critic="claude-haiku-4-5",
        model_user_simulator="claude-haiku-4-5", model_judge="claude-haiku-4-5",
        model_target="claude-haiku-4-5",
    ))

    input_path = Path(__file__).parent.parent / "fixtures" / "eval_input_minimal.yaml"
    output_path = tmp_path / "result.json"

    from click.testing import CliRunner
    runner = CliRunner()
    res = runner.invoke(cli.main, ["evaluate", "--input", str(input_path), "--output", str(output_path)])
    assert res.exit_code == 0, res.output

    data = json.loads(output_path.read_text())
    assert data["schema_version"] == "eval-result-v1"
    assert data["status"] == "DONE"
    assert data["total_criteria"] == 1
    assert data["passed_criteria"] == 1
    assert data["score"] == 1.0
    assert len(data["sessions"]) == 1

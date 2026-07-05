"""End-to-end train-agent CLI test with all LLM seams stubbed."""

from __future__ import annotations

import csv
import json
from pathlib import Path

import yaml as _yaml
from click.testing import CliRunner

from aikdm import cli, models
from aikdm.config import Settings
from aikdm.eval.schemas import (
    AikdmMeta, EvalResult, Judgment, ResultSession,
)
from aikdm.train import orchestrator as train_orch_mod
from aikdm.train import teacher as teacher_mod
from aikdm.train.schemas import SectionPatch, TeacherOutput


def _result(score: float) -> EvalResult:
    return EvalResult(
        schema_version="eval-result-v1", status="DONE",
        score=score, total_criteria=1, passed_criteria=int(round(score)),
        aikdm_meta=AikdmMeta(
            user_simulator_model="m", judge_model="m", target_agent_model="m",
            duration_seconds=0.0,
        ),
        sessions=[ResultSession(
            session_id="s-1", score=score, total_criteria=1,
            passed_criteria=int(round(score)),
            judgments=[Judgment(message_id="m-a-1", criterion="greets politely",
                                passed=bool(round(score)), reason="ok")],
        )],
    )


def test_train_agent_produces_bundle_and_report(monkeypatch, tmp_path: Path):
    # Stub build_model everywhere so ADK/LiteLLM isn't invoked.
    monkeypatch.setattr(models, "build_model", lambda role, settings: object())
    monkeypatch.setattr(train_orch_mod.models, "build_model", lambda role, settings: object())
    monkeypatch.setattr(train_orch_mod, "build_teacher_agent", lambda model: object())

    # Stub the training orchestrator's run_eval — sequence: baseline=0.4, cand=0.7.
    scores = iter([0.4, 0.7])
    async def fake_run_eval(inp, settings):     # noqa: ARG001
        return _result(next(scores))
    monkeypatch.setattr(train_orch_mod, "run_eval", fake_run_eval)

    async def fake_propose(*, agent, bundle, eval_result, tried):    # noqa: ARG001
        return teacher_mod.TeacherTurn(
            output=TeacherOutput(patches=[
                SectionPatch(section_id="main_prompt",
                             new="Say hi politely.", rationale="fix greeting"),
            ], focus_notes="explicit greeting cue"),
            tokens_in=100, tokens_out=20,
        )
    monkeypatch.setattr(train_orch_mod, "propose_patches", fake_propose)

    # Stub load_settings so env checks are skipped.
    monkeypatch.setattr(cli, "load_settings", lambda: Settings(
        model_generator="claude-haiku-4-5", model_critic="claude-haiku-4-5",
        model_user_simulator="claude-haiku-4-5", model_judge="claude-haiku-4-5",
        model_target="claude-haiku-4-5", model_teacher="claude-haiku-4-5",
    ))

    input_path = Path(__file__).parent.parent / "fixtures" / "train_input_minimal.yaml"
    out_dir = tmp_path / "out"

    runner = CliRunner()
    res = runner.invoke(cli.main, [
        "train-agent",
        "--input", str(input_path),
        "--epochs", "1", "--patience", "1",
        "--out", str(out_dir),
    ])
    assert res.exit_code == 0, res.output

    # Bundle file exists and reflects the promoted patch.
    bundle = _yaml.safe_load((out_dir / "bundle.yaml").read_text())
    assert bundle["main_prompt"] == "Say hi politely."

    # JSON report shape.
    report = json.loads((out_dir / "training-report.json").read_text())
    assert report["schema_version"] == "training-report-v1"
    assert report["initial_score"] == 0.4
    assert report["final_score"] == 0.7
    assert report["stopped_reason"] == "max_epochs"
    assert report["epochs"][0]["promoted"] is True

    # CSV report has one row per epoch with the right columns.
    with open(out_dir / "training-report.csv", encoding="utf-8") as f:
        rows = list(csv.DictReader(f))
    assert len(rows) == 1
    assert rows[0]["promoted"] == "True"
    assert rows[0]["candidate_score"] == "0.7"


def test_train_agent_refuses_populated_out_dir(monkeypatch, tmp_path: Path):
    out_dir = tmp_path / "out"
    out_dir.mkdir()
    (out_dir / "existing").write_text("hi")

    input_path = Path(__file__).parent.parent / "fixtures" / "train_input_minimal.yaml"
    runner = CliRunner()
    res = runner.invoke(cli.main, [
        "train-agent",
        "--input", str(input_path),
        "--epochs", "1", "--patience", "1",
        "--out", str(out_dir),
    ])
    assert res.exit_code == 2, res.output
    assert "already exists" in res.output or "populated" in res.output

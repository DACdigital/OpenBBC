"""Orchestrator tests. run_eval and teacher are stubbed — no LLM, no network."""

from __future__ import annotations

import asyncio
from typing import Any

import pytest

from aikdm.config import Settings
from aikdm.eval.schemas import (
    AikdmMeta, EvalInput, EvalResult, InputAgentVersion, InputDatasetVersion,
    Judgment, ResultSession,
)
from aikdm.train import orchestrator as orch_mod
from aikdm.train import teacher as teacher_mod
from aikdm.train.schemas import SectionPatch, TeacherOutput


def _input(bundle: dict[str, Any] | None = None) -> EvalInput:
    return EvalInput(
        schema_version="eval-input-v1",
        eval_id="e-1",
        agent_version=InputAgentVersion(id="av-1", bundle=bundle or {"main_prompt": "orig", "skills": []}),
        dataset_version=InputDatasetVersion(id="dv-1", sessions=[]),
    )


def _result(score: float) -> EvalResult:
    return EvalResult(
        schema_version="eval-result-v1", status="DONE",
        score=score, total_criteria=10, passed_criteria=int(score * 10),
        aikdm_meta=AikdmMeta(
            user_simulator_model="m", judge_model="m", target_agent_model="m",
            duration_seconds=0.1,
        ),
        sessions=[ResultSession(
            session_id="s-1", score=score, total_criteria=10,
            passed_criteria=int(score * 10),
            judgments=[Judgment(message_id="m-1", criterion="c", passed=True, reason="ok")],
        )],
    )


def _settings() -> Settings:
    return Settings(
        model_generator="claude-haiku-4-5", model_critic="claude-haiku-4-5",
        model_user_simulator="claude-haiku-4-5", model_judge="claude-haiku-4-5",
        model_target="claude-haiku-4-5", model_teacher="claude-haiku-4-5",
    )


def test_promotes_when_candidate_better(monkeypatch):
    scores = iter([0.4, 0.6])                       # baseline, candidate
    async def fake_run_eval(inp, settings):         # noqa: ARG001
        return _result(next(scores))
    async def fake_propose(*, agent, bundle, eval_result, tried):   # noqa: ARG001
        return teacher_mod.TeacherTurn(
            output=TeacherOutput(patches=[
                SectionPatch(section_id="main_prompt", new="tighter", rationale="x"),
            ], focus_notes="try tighter"),
            tokens_in=100, tokens_out=20,
        )

    monkeypatch.setattr(orch_mod, "run_eval", fake_run_eval)
    monkeypatch.setattr(orch_mod, "propose_patches", fake_propose)
    monkeypatch.setattr(orch_mod, "build_teacher_agent", lambda m: object())
    monkeypatch.setattr(orch_mod.models, "build_model", lambda role, s: object())

    final_bundle, report = asyncio.run(orch_mod.run_training(
        _input(), _settings(), epochs=1, patience=1,
    ))
    assert report.initial_score == 0.4
    assert report.final_score == 0.6
    assert report.total_epochs_run == 1
    assert report.stopped_reason == "max_epochs"
    assert report.epochs[0].promoted is True
    assert final_bundle["main_prompt"] == "tighter"


def test_discards_when_candidate_worse(monkeypatch):
    scores = iter([0.7, 0.5])
    async def fake_run_eval(inp, settings):         # noqa: ARG001
        return _result(next(scores))
    async def fake_propose(*, agent, bundle, eval_result, tried):   # noqa: ARG001
        return teacher_mod.TeacherTurn(
            output=TeacherOutput(patches=[
                SectionPatch(section_id="main_prompt", new="bad", rationale="x"),
            ]),
            tokens_in=10, tokens_out=1,
        )

    monkeypatch.setattr(orch_mod, "run_eval", fake_run_eval)
    monkeypatch.setattr(orch_mod, "propose_patches", fake_propose)
    monkeypatch.setattr(orch_mod, "build_teacher_agent", lambda m: object())
    monkeypatch.setattr(orch_mod.models, "build_model", lambda role, s: object())

    final_bundle, report = asyncio.run(orch_mod.run_training(
        _input(), _settings(), epochs=1, patience=1,
    ))
    assert report.final_score == 0.7           # baseline preserved
    assert report.epochs[0].promoted is False
    assert final_bundle["main_prompt"] == "orig"  # original kept


def test_patience_early_stop_after_three_non_improvements(monkeypatch):
    # baseline + 3 non-improving candidates
    scores = iter([0.5, 0.4, 0.4, 0.4, 0.4, 0.4, 0.4])
    async def fake_run_eval(inp, settings):         # noqa: ARG001
        return _result(next(scores))
    async def fake_propose(*, agent, bundle, eval_result, tried):   # noqa: ARG001
        return teacher_mod.TeacherTurn(
            output=TeacherOutput(patches=[
                SectionPatch(section_id="main_prompt", new=f"try-{len(tried)}", rationale="x"),
            ]),
            tokens_in=1, tokens_out=1,
        )

    monkeypatch.setattr(orch_mod, "run_eval", fake_run_eval)
    monkeypatch.setattr(orch_mod, "propose_patches", fake_propose)
    monkeypatch.setattr(orch_mod, "build_teacher_agent", lambda m: object())
    monkeypatch.setattr(orch_mod.models, "build_model", lambda role, s: object())

    _final, report = asyncio.run(orch_mod.run_training(
        _input(), _settings(), epochs=10, patience=3,
    ))
    assert report.stopped_reason == "plateau"
    assert report.total_epochs_run == 3
    # tried_patches accumulated across the three discarded epochs
    # (visible as unique "new" values in the patches column)
    news = {p.new for ep in report.epochs for p in ep.patches}
    assert len(news) == 3


def test_empty_patches_counts_toward_patience_and_no_eval(monkeypatch):
    scores = iter([0.5])                            # only baseline eval consumed
    async def fake_run_eval(inp, settings):         # noqa: ARG001
        return _result(next(scores))
    async def fake_propose(*, agent, bundle, eval_result, tried):   # noqa: ARG001
        return teacher_mod.TeacherTurn(
            output=TeacherOutput(patches=[], focus_notes="giving up"),
            tokens_in=1, tokens_out=1,
        )

    monkeypatch.setattr(orch_mod, "run_eval", fake_run_eval)
    monkeypatch.setattr(orch_mod, "propose_patches", fake_propose)
    monkeypatch.setattr(orch_mod, "build_teacher_agent", lambda m: object())
    monkeypatch.setattr(orch_mod.models, "build_model", lambda role, s: object())

    _final, report = asyncio.run(orch_mod.run_training(
        _input(), _settings(), epochs=10, patience=2,
    ))
    assert report.stopped_reason == "plateau"
    assert report.total_epochs_run == 2
    for ep in report.epochs:
        assert ep.promoted is False
        assert ep.patches == []
    # If fake_run_eval had been called for candidates, `next(scores)` would raise
    # StopIteration — the assertion above proves the empty-patch branch skipped eval.


def test_failed_candidate_eval_doesnt_kill_loop(monkeypatch):
    baseline = iter([0.5])
    async def fake_run_eval(inp, settings):         # noqa: ARG001
        # first call = baseline (0.5); subsequent = raise
        try:
            return _result(next(baseline))
        except StopIteration:
            raise RuntimeError("candidate eval failed")

    async def fake_propose(*, agent, bundle, eval_result, tried):   # noqa: ARG001
        return teacher_mod.TeacherTurn(
            output=TeacherOutput(patches=[
                SectionPatch(section_id="main_prompt", new="attempt", rationale="x"),
            ]),
            tokens_in=1, tokens_out=1,
        )

    monkeypatch.setattr(orch_mod, "run_eval", fake_run_eval)
    monkeypatch.setattr(orch_mod, "propose_patches", fake_propose)
    monkeypatch.setattr(orch_mod, "build_teacher_agent", lambda m: object())
    monkeypatch.setattr(orch_mod.models, "build_model", lambda role, s: object())

    _final, report = asyncio.run(orch_mod.run_training(
        _input(), _settings(), epochs=2, patience=2,
    ))
    assert report.stopped_reason == "plateau"
    assert report.epochs[0].error != ""
    assert report.epochs[0].promoted is False
    assert report.final_score == 0.5


def test_failed_apply_patches_blacklists_patch(monkeypatch):
    scores = iter([0.5])
    async def fake_run_eval(inp, settings):     # noqa: ARG001
        return _result(next(scores))

    async def fake_propose(*, agent, bundle, eval_result, tried):    # noqa: ARG001
        # Always propose a malformed section_id — apply_patches will raise.
        return teacher_mod.TeacherTurn(
            output=TeacherOutput(patches=[
                SectionPatch(section_id="skill.ghost.prompt", new="x", rationale="x"),
            ]),
            tokens_in=1, tokens_out=1,
        )

    monkeypatch.setattr(orch_mod, "run_eval", fake_run_eval)
    monkeypatch.setattr(orch_mod, "propose_patches", fake_propose)
    monkeypatch.setattr(orch_mod, "build_teacher_agent", lambda m: object())
    monkeypatch.setattr(orch_mod.models, "build_model", lambda role, s: object())

    _final, report = asyncio.run(orch_mod.run_training(
        _input(), _settings(), epochs=3, patience=2,
    ))
    # Loop should exit on plateau (structural failures still count).
    assert report.stopped_reason == "plateau"
    assert report.total_epochs_run == 2
    for ep in report.epochs:
        assert ep.error.startswith("apply_patches:")
        assert ep.promoted is False

"""Pydantic model validation for the training-report-v1 wire format."""

from __future__ import annotations

import pytest
from pydantic import ValidationError

from aikdm.train.schemas import (
    EpochRecord,
    SectionPatch,
    TeacherOutput,
    TrainingModels,
    TrainingReport,
)


def _models() -> TrainingModels:
    return TrainingModels(
        teacher_model="claude-opus-4-7", judge_model="openai/gpt-4o",
        user_simulator_model="gemini-2.0-flash", target_agent_model="claude-haiku-4-5",
        user_turn_mode="verbatim", history_mode="simulated",
    )


def test_section_patch_requires_non_empty_new():
    with pytest.raises(ValidationError):
        SectionPatch(section_id="main_prompt", new="", rationale="x")


def test_section_patch_rejects_whitespace_only_new():
    with pytest.raises(ValidationError):
        SectionPatch(section_id="main_prompt", new="   ", rationale="x")


def test_section_patch_forbids_extra_fields():
    with pytest.raises(ValidationError):
        SectionPatch(section_id="main_prompt", new="hi", rationale="x", extra="nope")


def test_teacher_output_defaults_to_empty_patches():
    out = TeacherOutput()
    assert out.patches == []
    assert out.focus_notes == ""


def test_epoch_record_roundtrip():
    rec = EpochRecord(
        epoch=1, baseline_score=0.5, candidate_score=0.7, promoted=True,
        patches=[SectionPatch(section_id="main_prompt", new="hi", rationale="x")],
        teacher_notes="tightened persona",
        duration_seconds=12.3, tokens_in=1000, tokens_out=200,
    )
    dumped = rec.model_dump()
    assert dumped["promoted"] is True
    assert dumped["patches"][0]["section_id"] == "main_prompt"
    assert dumped["error"] == ""


def test_training_report_schema_version_default():
    r = TrainingReport(
        input_eval_id="e-1",
        initial_score=0.4, final_score=0.6,
        total_epochs_run=3, stopped_reason="plateau",
        epochs=[], final_bundle_path="/tmp/bundle.yaml",
        models=_models(),
    )
    assert r.schema_version == "training-report-v1"


def test_training_report_stopped_reason_literal():
    with pytest.raises(ValidationError):
        TrainingReport(
            input_eval_id="e-1", initial_score=0.0, final_score=0.0,
            total_epochs_run=0, stopped_reason="bogus",
            epochs=[], final_bundle_path="/x",
            models=_models(),
        )


def test_training_report_requires_models():
    with pytest.raises(ValidationError, match="models"):
        TrainingReport(
            input_eval_id="e-1", initial_score=0.4, final_score=0.6,
            total_epochs_run=1, stopped_reason="plateau",
            epochs=[], final_bundle_path="/x",
        )


def test_training_report_dumps_models_with_eval_meta_key_names():
    r = TrainingReport(
        input_eval_id="e-1", initial_score=0.4, final_score=0.6,
        total_epochs_run=1, stopped_reason="plateau",
        epochs=[], final_bundle_path="/x", models=_models(),
    )
    assert r.model_dump()["models"] == {
        "teacher_model": "claude-opus-4-7",
        "judge_model": "openai/gpt-4o",
        "user_simulator_model": "gemini-2.0-flash",
        "target_agent_model": "claude-haiku-4-5",
        "user_turn_mode": "verbatim",
        "history_mode": "simulated",
    }


def test_training_models_rejects_unknown_mode_and_extra_keys():
    base = _models().model_dump()
    with pytest.raises(ValidationError):
        TrainingModels(**{**base, "history_mode": "bogus"})
    with pytest.raises(ValidationError):
        TrainingModels(**{**base, "user_turn_mode": "bogus"})
    with pytest.raises(ValidationError):
        TrainingModels(**base, generator_model="x")

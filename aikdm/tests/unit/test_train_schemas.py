"""Pydantic model validation for the training-report-v1 wire format."""

from __future__ import annotations

import pytest
from pydantic import ValidationError

from aikdm.train.schemas import (
    EpochRecord,
    SectionPatch,
    TeacherOutput,
    TrainingReport,
)


def test_section_patch_requires_non_empty_new():
    with pytest.raises(ValidationError):
        SectionPatch(section_id="main_prompt", new="", rationale="x")


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
    )
    assert r.schema_version == "training-report-v1"


def test_training_report_stopped_reason_literal():
    with pytest.raises(ValidationError):
        TrainingReport(
            input_eval_id="e-1", initial_score=0.0, final_score=0.0,
            total_epochs_run=0, stopped_reason="bogus",
            epochs=[], final_bundle_path="/x",
        )

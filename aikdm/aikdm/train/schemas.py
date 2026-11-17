"""Pydantic wire format for the training loop (train-report-v1)."""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, field_validator


class SectionPatch(BaseModel):
    model_config = ConfigDict(extra="forbid")
    section_id: str                  # "main_prompt" | "skill.<name>.prompt"
    new: str                         # full replacement content
    rationale: str                   # 1-2 sentences: why this change

    @field_validator("new")
    @classmethod
    def _new_non_empty(cls, v: str) -> str:
        if not v.strip():
            raise ValueError("must be non-empty")
        return v


class TeacherOutput(BaseModel):
    model_config = ConfigDict(extra="forbid")
    patches: list[SectionPatch] = Field(default_factory=list)   # 0..K
    focus_notes: str = ""


class EpochRecord(BaseModel):
    model_config = ConfigDict(extra="forbid")
    epoch: int
    baseline_score: float
    candidate_score: float
    promoted: bool
    patches: list[SectionPatch] = Field(default_factory=list)
    teacher_notes: str = ""
    duration_seconds: float = 0.0
    tokens_in: int = 0
    tokens_out: int = 0
    error: str = ""


class TrainingModels(BaseModel):
    """Models and eval modes that produced a training run. Values are the
    strings as configured in Settings, not normalized. Key names match the
    eval contract's AikdmMeta (aikdm/eval/schemas.py), plus teacher_model."""
    model_config = ConfigDict(extra="forbid")
    teacher_model: str
    judge_model: str
    user_simulator_model: str
    target_agent_model: str
    user_turn_mode: Literal["verbatim", "paraphrase"]
    history_mode: Literal["simulated", "reference"]


class TrainingReport(BaseModel):
    model_config = ConfigDict(extra="forbid")
    schema_version: Literal["training-report-v1"] = "training-report-v1"
    input_eval_id: str
    initial_score: float
    final_score: float
    total_epochs_run: int
    stopped_reason: Literal["max_epochs", "plateau", "perfect_score"]
    epochs: list[EpochRecord] = Field(default_factory=list)
    final_bundle_path: str
    models: TrainingModels

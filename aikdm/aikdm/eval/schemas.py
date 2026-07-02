"""Pydantic models for the eval input/output wire formats."""

from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field


class InputMessage(BaseModel):
    model_config = ConfigDict(extra="ignore")
    message_id: str
    role: Literal["user", "assistant", "tool"]
    content: Any  # opaque JSON — Anthropic-style content-blocks array or string


class InputCriterion(BaseModel):
    model_config = ConfigDict(extra="ignore")
    message_id: str
    rating: Literal["up", "down"]
    items: list[str] = Field(default_factory=list)


class InputSession(BaseModel):
    model_config = ConfigDict(extra="ignore")
    session_id: str
    title: str = ""
    transcript: list[InputMessage] = Field(default_factory=list)
    criteria: list[InputCriterion] = Field(default_factory=list)


class InputAgentVersion(BaseModel):
    model_config = ConfigDict(extra="ignore")
    id: str
    bundle: dict[str, Any]  # loose — bundle shape lives in aikdm.schemas.Bundle


class InputDatasetVersion(BaseModel):
    model_config = ConfigDict(extra="ignore")
    id: str
    sessions: list[InputSession] = Field(default_factory=list)


class EvalInput(BaseModel):
    model_config = ConfigDict(extra="ignore")
    schema_version: Literal["eval-input-v1"]
    eval_id: str
    agent_version: InputAgentVersion
    dataset_version: InputDatasetVersion


class Judgment(BaseModel):
    model_config = ConfigDict(extra="forbid")
    message_id: str            # original assistant msg id from the input session
    criterion: str
    passed: bool
    reason: str = ""


class ResultSession(BaseModel):
    model_config = ConfigDict(extra="forbid")
    session_id: str
    score: float
    total_criteria: int
    passed_criteria: int
    transcript: list[dict[str, Any]] = Field(default_factory=list)  # simulated
    judgments: list[Judgment] = Field(default_factory=list)


class AikdmMeta(BaseModel):
    model_config = ConfigDict(extra="allow")
    user_simulator_model: str
    judge_model: str
    target_agent_model: str
    duration_seconds: float
    aikdm_version: str = "0.1.0"


class EvalResult(BaseModel):
    model_config = ConfigDict(extra="forbid")
    schema_version: Literal["eval-result-v1"] = "eval-result-v1"
    status: Literal["DONE", "FAILED"]
    error_message: str = ""
    score: float = 0.0
    total_criteria: int = 0
    passed_criteria: int = 0
    aikdm_meta: AikdmMeta | dict[str, Any] = Field(default_factory=dict)
    sessions: list[ResultSession] = Field(default_factory=list)

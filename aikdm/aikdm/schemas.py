"""Pydantic models for aikdm inputs and outputs.

Input mirrors open-bbcd/internal/types/flow_map.go. Adding a field there
requires adding it here too (and bumping schema_version).
"""

from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field


class FlowMapSource(BaseModel):
    model_config = ConfigDict(extra="forbid")

    compiler_schema_version: int
    generated_from_sha: str
    app_name: str
    stack: dict[str, str] = Field(default_factory=dict)


class Capability(BaseModel):
    model_config = ConfigDict(extra="forbid")

    name: str
    summary: str = ""
    tools: list[dict[str, Any]] = Field(default_factory=list)
    prose_md: str = ""


class Skill(BaseModel):
    model_config = ConfigDict(extra="forbid")

    id: str
    origin: Literal["discovered", "custom"]
    name: str
    description: str = ""
    user_phrases: list[str] = Field(default_factory=list)
    role: Literal["read", "write"]
    capability_ref: str = ""
    external: bool = False
    external_note: str = ""
    proposed_tool: str = ""
    prose_md: str = ""


class Position(BaseModel):
    model_config = ConfigDict(extra="forbid")

    x: int
    y: int


class Workflow(BaseModel):
    model_config = ConfigDict(extra="forbid")

    mermaid: str
    layout: dict[str, Position] = Field(default_factory=dict)


class Flow(BaseModel):
    model_config = ConfigDict(extra="forbid")

    id: str
    origin: str
    included: bool
    name: str
    description: str = ""
    intent: str = ""
    user_phrases: list[str] = Field(default_factory=list)
    preconditions: list[str] = Field(default_factory=list)
    postconditions: list[str] = Field(default_factory=list)
    side_effects: list[str] = Field(default_factory=list)
    confidence: str = ""
    workflow: Workflow
    prose_md: str = ""


class FlowMapConfig(BaseModel):
    """Input contract: full agent configuration produced by open-bbcd's
    configurator. Mirrors the Go struct in open-bbcd/internal/types/flow_map.go.
    """

    model_config = ConfigDict(extra="forbid")

    schema_version: int
    name: str
    scope: str = ""
    should_do: str = ""
    should_not_do: str = ""
    business_domain: str = ""
    source: FlowMapSource
    capabilities: list[Capability] = Field(default_factory=list)
    skills: list[Skill] = Field(default_factory=list)
    flows: list[Flow] = Field(default_factory=list)

"""Pydantic models for aikdm inputs and outputs.

The input contract mirrors the upstream FlowMapConfig shape that
producers emit. Adding a field upstream requires adding it here too
(and bumping schema_version).
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
    origin: Literal["discovered", "custom"]
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
    """Input contract: full agent configuration emitted by an upstream
    producer. Field shape and semantics are versioned via schema_version.
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


# ---------- Output bundle ----------


class TokenUsage(BaseModel):
    model_config = ConfigDict(extra="forbid")

    generator_in: int = 0
    generator_out: int = 0
    critic_in: int = 0
    critic_out: int = 0


class BundleMetadata(BaseModel):
    model_config = ConfigDict(extra="forbid")

    config_schema_version: int
    prompt_schema_version: str
    model_generator: str
    model_critic: str
    generated_at: str
    critic_rounds_run: int
    critic_notes: list[str] = Field(default_factory=list)
    tokens_used: TokenUsage


class SkillPrompt(BaseModel):
    """Anthropic-style skill: name + description (dispatcher-visible) + prompt
    (loaded when the dispatcher selects this skill)."""

    model_config = ConfigDict(extra="forbid")

    name: str
    description: str
    prompt: str


class ExternalAction(BaseModel):
    model_config = ConfigDict(extra="forbid")

    skill_id: str
    external_note: str


class BundleCapability(BaseModel):
    """Capability passed through from FlowMapConfig.capabilities at bundle generation time."""

    model_config = ConfigDict(extra="forbid")

    name: str
    description: str
    proposed_tool: str


class Bundle(BaseModel):
    model_config = ConfigDict(extra="forbid")

    metadata: BundleMetadata
    main_prompt: str
    capabilities: list[BundleCapability] = Field(default_factory=list)
    skills: list[SkillPrompt] = Field(default_factory=list)
    external_actions: list[ExternalAction] = Field(default_factory=list)


# ---------- Prompt schema (drives section/tag layout) ----------


SectionSource = Literal["wizard_copied", "llm_synthesized", "config_derived"]


class PromptSection(BaseModel):
    model_config = ConfigDict(extra="forbid")

    name: str                           # logical identifier
    tag: str                            # XML tag rendered in the final prompt
    source: SectionSource
    source_field: str = ""              # for wizard_copied: which FlowMapConfig field
    required: bool = True
    guidance: str = ""                  # short note rendered into the generator system prompt


class PromptSchema(BaseModel):
    model_config = ConfigDict(extra="forbid")

    version: str
    main_prompt: list[PromptSection]
    skill_prompt: list[PromptSection]

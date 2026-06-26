"""Pydantic models for aikdm inputs and outputs.

The input contract mirrors the v3 FlowMapConfig shape that producers emit
(open-bbcd's flowmap.Parse → JSONB row → CLI stdin YAML). Adding a field
upstream requires adding it here too (and bumping schema_version).
"""

from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, model_validator


class FlowMapSource(BaseModel):
    model_config = ConfigDict(extra="forbid")

    compiler_schema_version: int
    generated_from_sha: str
    app_name: str
    stack: dict[str, str] = Field(default_factory=dict)


class ParamSpec(BaseModel):
    model_config = ConfigDict(extra="forbid")

    name: str
    type: str = ""
    required: bool = False


class Endpoint(BaseModel):
    """Discovered HTTP endpoint, sourced from a flow-map-compiler wiki.

    ``body_shape`` and ``response_shape`` must be JSON Schema objects
    or ``None`` / ``"unknown"`` respectively. Free-form TypeScript-style
    type literal strings are tolerated here for backwards compatibility
    with older wikis, but they will not produce a usable LLM-visible
    tool-argument schema downstream; flow-map-compiler v2+ rule 16
    forbids them.
    """

    model_config = ConfigDict(extra="forbid")

    id: str
    proposed: bool = True
    method: str
    path: str
    path_params: list[ParamSpec] = Field(default_factory=list)
    query_params: list[ParamSpec] = Field(default_factory=list)
    body_shape: Any = None
    response_shape: Any = None
    auth: str = ""
    source: str = ""
    used_by_skills: list[str] = Field(default_factory=list)
    confidence: str = ""
    prose_md: str = ""


class SkillEndpointRef(BaseModel):
    model_config = ConfigDict(extra="forbid")

    endpoint: str
    role: Literal["read", "write", "side-effect"] = "read"
    when: str = ""


class Skill(BaseModel):
    model_config = ConfigDict(extra="forbid")

    id: str
    origin: Literal["discovered", "custom"]
    name: str
    description: str = ""
    domain: str = ""
    user_phrases: list[str] = Field(default_factory=list)
    suggested_endpoints: list[SkillEndpointRef] = Field(default_factory=list)
    external: bool = False
    external_note: str = ""
    confidence: str = ""
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


class AttachedMCP(BaseModel):
    """A user-attached MCP server. Carries server identity + a per-attachment
    operator note that aikdm folds into the main prompt as guidance for
    when/how to use the server.

    Provided by open-bbcd as part of the aikdm-input YAML (a single source
    of truth for the agent's configuration). Discovery itself doesn't know
    about attached MCPs — they're added in the BO configurator.
    """

    model_config = ConfigDict(extra="forbid")

    name: str
    url: str
    note: str = ""


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
    endpoints: list[Endpoint] = Field(default_factory=list)
    skills: list[Skill] = Field(default_factory=list)
    flows: list[Flow] = Field(default_factory=list)
    attached_mcps: list[AttachedMCP] = Field(default_factory=list)

    @model_validator(mode="after")
    def _require_v3(self) -> "FlowMapConfig":
        if self.schema_version != 3:
            raise ValueError(
                f"schema_version {self.schema_version} not supported; require 3"
            )
        return self


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


class BundleTool(BaseModel):
    """Atomic tool the agent can call. One entry per discovered endpoint
    (flattened from FlowMapConfig.endpoints[]). Carries the information
    open-bbcd needs to wrap the endpoint as an MCP tool (name, method,
    path, auth) plus runtime context (description, source for provenance,
    confidence) and the JSON Schema fragments (path_params, query_params,
    body_shape) the LLM uses to call it.

    Contract with discovery: ``body_shape`` and ``response_shape`` are
    JSON Schema objects (``{"type": "object", "properties": {...},
    "required": [...]}``) or ``None`` / ``"unknown"`` respectively.
    TypeScript-style type literals (``"{ items: ... }"``) are not a
    valid shape — open-bbcd's runtime schema builder cannot merge them
    with the path/query param schema and the LLM ends up with a tool
    that accepts no arguments. See flow-map-compiler
    ``references/lint-contract.md`` rule 16.
    """

    model_config = ConfigDict(extra="forbid")

    id: str
    name: str
    description: str
    method: str
    path: str
    auth: str = ""
    confidence: str = ""
    source: str = ""
    path_params: list[ParamSpec] = Field(default_factory=list)
    query_params: list[ParamSpec] = Field(default_factory=list)
    body_shape: Any | None = None
    response_shape: Any | None = None


class Bundle(BaseModel):
    model_config = ConfigDict(extra="forbid")

    metadata: BundleMetadata
    main_prompt: str
    tools: list[BundleTool] = Field(default_factory=list)
    skills: list[SkillPrompt] = Field(default_factory=list)
    external_actions: list[ExternalAction] = Field(default_factory=list)


# ---------- Prompt schema (drives section/tag layout) ----------


SectionSource = Literal["wizard_copied", "llm_synthesized", "config_derived"]


class PromptSection(BaseModel):
    model_config = ConfigDict(extra="forbid")

    name: str
    tag: str
    source: SectionSource
    source_field: str = ""
    required: bool = True
    guidance: str = ""


class PromptSchema(BaseModel):
    model_config = ConfigDict(extra="forbid")

    version: str
    main_prompt: list[PromptSection]
    skill_prompt: list[PromptSection]

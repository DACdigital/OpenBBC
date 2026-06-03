"""ADK agent factories + thin callers.

aikdm uses four agent kinds, each with a focused system prompt:
- main_prompt agent emits the main_prompt XML body
- main_prompt_critic agent critiques a main_prompt in isolation
- skill_prompt agent emits a single skill's XML body
- skill_prompt_critic agent critiques a single skill prompt in isolation

Each unit (main_prompt, every internal skill) runs its OWN atomic gen→crit
loop. There is no bundle-wide critic — cross-unit consistency comes from
construction (orchestrator iterates over input skills) and shared inputs
(same flow_map_config + prompt schema).

Tests substitute the `_run_*` helpers to avoid LLM traffic.
"""

from __future__ import annotations

import uuid
import xml.sax.saxutils as _su
from dataclasses import dataclass
from typing import Any

from google.adk.agents import LlmAgent  # type: ignore[import-untyped]
from google.adk.runners import InMemoryRunner
from google.genai import types as _genai_types
from pydantic import BaseModel

from aikdm.schemas import Capability, FlowMapConfig, Skill

MAIN_PROMPT_SYSTEM_PROMPT = """\
You are aikdm's main-prompt generator.

You receive a <flow_map_config> describing a planned AI agent and a
<main_prompt_scaffold> showing the section layout you must produce.

Your only output is the main system prompt as a single XML string.
You do NOT emit skills — each skill is generated separately. The main
prompt references skills by name through <skills_index>; you do not
write their prompt bodies.

Rules:
- Fill every LLM-synthesized tag with your own prose. Never copy the
  wizard fields (scope, business_domain, should_do, should_not_do) verbatim
  — refine them into polished, concrete instructions.
- The <workflows> section must begin with the <usage> sub-block copied
  verbatim from the scaffold, then synthesize each included flow
  (name, intent, preconditions, ordered steps referencing skill names,
  postconditions, side-effects). Exclude flows with included=false.
- The <examples> section shows 2-3 routing examples (user says X ->
  agent picks skill Y / declines / redirects). Distinct from per-skill
  examples.
- The <skills_index> lists every internal skill (name + one-line
  description) — keep it in sync with the input.
- The <external_actions> section names every external skill from input
  (skill id + external_note). Empty if there are none.
- All XML tags use Anthropic-style.

Return structured output: {"main_prompt": "<role>...</role>...<external_actions/>"}
"""

SKILL_PROMPT_SYSTEM_PROMPT = """\
You are aikdm's skill-prompt generator.

You receive a <flow_map_config>, a single <target_skill> (the skill you
must write the prompt for), and a <skill_scaffold> showing the section
layout. You produce the prompt body for THIS ONE skill.

Rules:
- Fill every LLM-synthesized tag in the scaffold with your own prose.
- The <resources> block names a single mcp_server with name=proposed_tool
  from the input. Never include HTTP method, path, or parameters.
- The <examples> block shows 2-3 execution-level examples for this skill
  (a representative turn inside the skill, not skill routing).
- Tone and guardrails must be consistent with the wider agent context
  (flow_map_config.scope, business_domain, should_not_do). You do not
  see the main_prompt body — rely on shared input for consistency.
- All XML tags use Anthropic-style.

Return structured output: {"name": "<skill-id>", "prompt": "<role>...</role>...<examples/>"}

`name` must equal the target_skill's id verbatim.
"""

MAIN_PROMPT_CRITIC_SYSTEM_PROMPT = """\
You are aikdm's main-prompt critic. You receive the original
<flow_map_config> and a <main_prompt> body. Your job: identify
substantive issues IN THE MAIN PROMPT that would mislead an agent.

Focus on:
- Wizard fields copied verbatim instead of refined into instructions.
- Missing <workflows> sub-block <usage>, or workflows omitted that have
  included=true, or workflows included that are included=false.
- Workflow steps referencing skill names not present in the input.
- <skills_index> entries not matching the input's internal skills (missing
  any, listing extra, name mismatches).
- <external_actions> not matching the input's external skills.
- Examples that imply forbidden behavior (contradicting should_not_do).
- Internal contradictions between sections (e.g. should_do allowing a
  thing the guardrails forbid).

Do NOT critique skill prompt bodies — you don't see them. Do NOT flag
stylistic preferences. Return an empty list if the main_prompt is acceptable.

Return structured output: {"issues": [string, ...]}.
"""

SKILL_PROMPT_CRITIC_SYSTEM_PROMPT = """\
You are aikdm's skill-prompt critic. You receive the original
<flow_map_config>, a single <target_skill>, and a <skill_prompt> body.
Your job: identify substantive issues IN THIS SKILL PROMPT that would
mislead an agent.

Focus on:
- The <resources> block: mcp_server name must equal target_skill.proposed_tool.
  No HTTP method/path/parameters allowed.
- <examples> being routing examples rather than execution examples.
- Skill body contradicting the input scope or should_not_do.
- Missing or empty required sections.
- Tone/guardrails clearly inconsistent with the input's business_domain.

Do NOT critique the main_prompt — you don't see it. Do NOT flag
stylistic preferences. Return an empty list if the skill prompt is
acceptable.

Return structured output: {"issues": [string, ...]}.
"""


@dataclass(frozen=True)
class MainPromptResult:
    main_prompt: str
    tokens_in: int
    tokens_out: int


@dataclass(frozen=True)
class SkillPromptResult:
    skill_name: str
    prompt: str
    tokens_in: int
    tokens_out: int


@dataclass(frozen=True)
class CriticResult:
    issues: list[str]
    tokens_in: int
    tokens_out: int


class _MainPromptOutput(BaseModel):
    main_prompt: str


class _SkillPromptOutput(BaseModel):
    name: str
    prompt: str


class _CriticOutput(BaseModel):
    issues: list[str]


def build_main_prompt_agent(model: Any) -> LlmAgent:
    return LlmAgent(
        name="aikdm_main_prompt",
        model=model,
        instruction=MAIN_PROMPT_SYSTEM_PROMPT,
        output_schema=_MainPromptOutput,
    )


def build_skill_prompt_agent(model: Any) -> LlmAgent:
    return LlmAgent(
        name="aikdm_skill_prompt",
        model=model,
        instruction=SKILL_PROMPT_SYSTEM_PROMPT,
        output_schema=_SkillPromptOutput,
    )


def build_main_prompt_critic_agent(model: Any) -> LlmAgent:
    return LlmAgent(
        name="aikdm_main_prompt_critic",
        model=model,
        instruction=MAIN_PROMPT_CRITIC_SYSTEM_PROMPT,
        output_schema=_CriticOutput,
    )


def build_skill_prompt_critic_agent(model: Any) -> LlmAgent:
    return LlmAgent(
        name="aikdm_skill_prompt_critic",
        model=model,
        instruction=SKILL_PROMPT_CRITIC_SYSTEM_PROMPT,
        output_schema=_CriticOutput,
    )


def _esc(s: str) -> str:
    return _su.escape(s, {'"': "&quot;"})


def _render_config_as_xml(config: FlowMapConfig) -> str:
    lines: list[str] = ["<flow_map_config>"]
    lines.append(f"  <name>{_esc(config.name)}</name>")
    lines.append(f"  <business_domain>{_esc(config.business_domain)}</business_domain>")
    lines.append(f"  <scope>{_esc(config.scope)}</scope>")
    lines.append(f"  <should_do>{_esc(config.should_do)}</should_do>")
    lines.append(f"  <should_not_do>{_esc(config.should_not_do)}</should_not_do>")
    lines.append("  <capabilities>")
    for c in config.capabilities:
        lines.append(f'    <capability name="{_esc(c.name)}">')
        lines.append(f"      <summary>{_esc(c.summary)}</summary>")
        lines.append(f"      <prose>{_esc(c.prose_md)}</prose>")
        lines.append("    </capability>")
    lines.append("  </capabilities>")
    lines.append("  <skills>")
    for s in config.skills:
        lines.append(
            f'    <skill id="{_esc(s.id)}" role="{s.role}" external="{str(s.external).lower()}">'
        )
        lines.append(f"      <name>{_esc(s.name)}</name>")
        lines.append(f"      <description>{_esc(s.description)}</description>")
        lines.append(f"      <user_phrases>{_esc(', '.join(s.user_phrases))}</user_phrases>")
        lines.append(f"      <capability_ref>{_esc(s.capability_ref)}</capability_ref>")
        lines.append(f"      <proposed_tool>{_esc(s.proposed_tool)}</proposed_tool>")
        lines.append(f"      <external_note>{_esc(s.external_note)}</external_note>")
        lines.append(f"      <prose>{_esc(s.prose_md)}</prose>")
        lines.append("    </skill>")
    lines.append("  </skills>")
    lines.append("  <flows>")
    for f in config.flows:
        lines.append(
            f'    <flow id="{_esc(f.id)}" included="{str(f.included).lower()}" '
            f'intent="{_esc(f.intent)}">'
        )
        lines.append(f"      <name>{_esc(f.name)}</name>")
        lines.append(f"      <description>{_esc(f.description)}</description>")
        lines.append(f"      <preconditions>{_esc('; '.join(f.preconditions))}</preconditions>")
        lines.append(f"      <postconditions>{_esc('; '.join(f.postconditions))}</postconditions>")
        lines.append(f"      <side_effects>{_esc('; '.join(f.side_effects))}</side_effects>")
        lines.append(f"      <prose>{_esc(f.prose_md)}</prose>")
        lines.append("    </flow>")
    lines.append("  </flows>")
    lines.append("</flow_map_config>")
    return "\n".join(lines)


def _render_target_skill_as_xml(skill: Skill, capability: Capability | None) -> str:
    lines: list[str] = [f'<target_skill id="{_esc(skill.id)}">']
    lines.append(f"  <name>{_esc(skill.name)}</name>")
    lines.append(f"  <description>{_esc(skill.description)}</description>")
    lines.append(f"  <proposed_tool>{_esc(skill.proposed_tool)}</proposed_tool>")
    lines.append(f"  <capability_ref>{_esc(skill.capability_ref)}</capability_ref>")
    if capability is not None:
        lines.append(f'  <linked_capability name="{_esc(capability.name)}">')
        lines.append(f"    <summary>{_esc(capability.summary)}</summary>")
        lines.append(f"    <prose>{_esc(capability.prose_md)}</prose>")
        lines.append("  </linked_capability>")
    lines.append(f"  <prose>{_esc(skill.prose_md)}</prose>")
    lines.append("</target_skill>")
    return "\n".join(lines)


def _adk_run_once(*, agent: LlmAgent, user_message_xml: str) -> tuple[str, int, int]:
    runner = InMemoryRunner(agent=agent)

    user_id = "aikdm"
    session_id = uuid.uuid4().hex
    runner.session_service.create_session_sync(
        app_name=runner.app_name,
        user_id=user_id,
        session_id=session_id,
    )

    new_message = _genai_types.Content(
        parts=[_genai_types.Part.from_text(text=user_message_xml)],
        role="user",
    )

    response_text = ""
    tokens_in = 0
    tokens_out = 0

    for event in runner.run(
        user_id=user_id,
        session_id=session_id,
        new_message=new_message,
    ):
        if event.usage_metadata is not None:
            um = event.usage_metadata
            tokens_in = um.prompt_token_count or 0
            tokens_out = um.candidates_token_count or 0

        if event.is_final_response() and event.content and event.content.parts:
            response_text = "".join(
                part.text
                for part in event.content.parts
                if part.text and not getattr(part, "thought", False)
            )

    if not response_text:
        raise RuntimeError(
            "ADK runner produced no final response text — "
            "check model credentials and agent configuration."
        )

    return response_text, tokens_in, tokens_out


def _run_main_prompt(*, agent: LlmAgent, user_message_xml: str) -> MainPromptResult:
    response_text, tokens_in, tokens_out = _adk_run_once(
        agent=agent, user_message_xml=user_message_xml
    )
    parsed = _MainPromptOutput.model_validate_json(response_text)
    return MainPromptResult(
        main_prompt=parsed.main_prompt, tokens_in=tokens_in, tokens_out=tokens_out
    )


def _run_skill_prompt(*, agent: LlmAgent, user_message_xml: str) -> SkillPromptResult:
    response_text, tokens_in, tokens_out = _adk_run_once(
        agent=agent, user_message_xml=user_message_xml
    )
    parsed = _SkillPromptOutput.model_validate_json(response_text)
    return SkillPromptResult(
        skill_name=parsed.name,
        prompt=parsed.prompt,
        tokens_in=tokens_in,
        tokens_out=tokens_out,
    )


def _run_critic(*, agent: LlmAgent, user_message_xml: str) -> CriticResult:
    response_text, tokens_in, tokens_out = _adk_run_once(
        agent=agent, user_message_xml=user_message_xml
    )
    parsed = _CriticOutput.model_validate_json(response_text)
    return CriticResult(
        issues=parsed.issues, tokens_in=tokens_in, tokens_out=tokens_out
    )


def call_main_prompt(
    agent: LlmAgent,
    config: FlowMapConfig,
    scaffold: str,
    *,
    previous_output: str | None = None,
    critic_issues: list[str] | None = None,
) -> MainPromptResult:
    parts = [
        _render_config_as_xml(config),
        "<main_prompt_scaffold>",
        scaffold,
        "</main_prompt_scaffold>",
    ]
    if previous_output is not None and critic_issues:
        parts.append("<previous_main_prompt>")
        parts.append(previous_output)
        parts.append("</previous_main_prompt>")
        parts.append("<critic_issues>")
        for issue in critic_issues:
            parts.append(f"  - {issue}")
        parts.append("</critic_issues>")
        parts.append("Rewrite main_prompt addressing every issue.")
    return _run_main_prompt(agent=agent, user_message_xml="\n".join(parts))


def call_skill_prompt(
    agent: LlmAgent,
    config: FlowMapConfig,
    skill: Skill,
    capability: Capability | None,
    scaffold: str,
    *,
    previous_output: str | None = None,
    critic_issues: list[str] | None = None,
) -> SkillPromptResult:
    parts = [
        _render_config_as_xml(config),
        _render_target_skill_as_xml(skill, capability),
        "<skill_scaffold>",
        scaffold,
        "</skill_scaffold>",
    ]
    if previous_output is not None and critic_issues:
        parts.append("<previous_skill_prompt>")
        parts.append(previous_output)
        parts.append("</previous_skill_prompt>")
        parts.append("<critic_issues>")
        for issue in critic_issues:
            parts.append(f"  - {issue}")
        parts.append("</critic_issues>")
        parts.append("Rewrite this skill prompt addressing every issue.")
    return _run_skill_prompt(agent=agent, user_message_xml="\n".join(parts))


def call_main_prompt_critic(
    agent: LlmAgent, config: FlowMapConfig, main_prompt: str
) -> CriticResult:
    user_message_xml = "\n".join([
        _render_config_as_xml(config),
        "<main_prompt>",
        main_prompt,
        "</main_prompt>",
    ])
    return _run_critic(agent=agent, user_message_xml=user_message_xml)


def call_skill_prompt_critic(
    agent: LlmAgent,
    config: FlowMapConfig,
    skill: Skill,
    capability: Capability | None,
    skill_prompt: str,
) -> CriticResult:
    user_message_xml = "\n".join([
        _render_config_as_xml(config),
        _render_target_skill_as_xml(skill, capability),
        "<skill_prompt>",
        skill_prompt,
        "</skill_prompt>",
    ])
    return _run_critic(agent=agent, user_message_xml=user_message_xml)

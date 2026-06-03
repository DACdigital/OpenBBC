"""ADK agent factories + thin callers.

aikdm generates a bundle in *three* call kinds, not one:
- main_prompt agent emits only the main_prompt XML body
- skill_prompt agent emits one skill's prompt XML body per call (one call per
  internal skill in the input)
- critic agent reads the *assembled* bundle and reports issues

The orchestrator depends on `call_main_prompt`, `call_skill_prompt`, and
`call_critic`. Tests substitute the `_run_*` helpers to avoid LLM traffic.

Generating skills in separate calls (one per skill) lets the orchestrator
enforce coverage by construction: it loops over the input's internal skills
and demands one response each. The LLM cannot silently drop a skill.
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

from aikdm.schemas import Bundle, Capability, FlowMapConfig, Skill

MAIN_PROMPT_SYSTEM_PROMPT = """\
You are aikdm's main-prompt generator.

You receive a <flow_map_config> describing a planned AI agent: business
domain, scope, what it should and should not do, capabilities (backend
endpoints), skills (the agent's high-level abilities), and flows
(business processes), plus a <main_prompt_scaffold> showing the section
layout you must produce.

Your only output is the main system prompt as a single XML string in
the `main_prompt` field. You DO NOT emit skills here — each skill is
generated in a separate call. The main prompt references skills by name
through <skills_index>; you do not write their prompt bodies.

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
layout. You produce the prompt body for THIS ONE skill, returning only
its name and prompt XML.

Rules:
- Fill every LLM-synthesized tag in the scaffold with your own prose.
- The <resources> block names a single mcp_server with name=proposed_tool
  from the input. Never include HTTP method, path, or parameters.
- The <examples> block shows 2-3 execution-level examples for this skill
  (a representative turn inside the skill, not skill routing).
- The skill body's tone and guardrails must be consistent with the
  main_prompt scaffold (provided alongside for context).
- All XML tags use Anthropic-style.

Return structured output: {"name": "<skill-id>", "prompt": "<role>...</role>...<examples/>"}

`name` must equal the target_skill's id verbatim.
"""

CRITIC_SYSTEM_PROMPT = """\
You are aikdm's prompt critic. You receive the original <flow_map_config>
and the assembled <bundle> (main_prompt + skills + external_actions).
Your job: identify substantive issues that would mislead an agent at
runtime.

Focus on:
- Inconsistency between main_prompt guardrails and skill-level guardrails.
- Skills that contradict should_not_do.
- A skill prompt that references a tool/capability not present in the input.
- External actions promised as internal skills (or vice-versa).
- Examples that imply forbidden behavior.
- The main_prompt's <workflows> section: steps referencing skills that
  don't exist, missing pre/postconditions, missing <usage> sub-block.

Do NOT flag stylistic preferences, alternative phrasings, or things
already correct. Return an empty list if the bundle is acceptable.

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


def build_critic_agent(model: Any) -> LlmAgent:
    return LlmAgent(
        name="aikdm_critic",
        model=model,
        instruction=CRITIC_SYSTEM_PROMPT,
        output_schema=_CriticOutput,
    )


def _esc(s: str) -> str:
    return _su.escape(s, {'"': "&quot;"})


def _render_config_as_xml(config: FlowMapConfig) -> str:
    """Render the FlowMapConfig as a single <flow_map_config> XML block.
    Used as the user message body for every agent call."""
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
    """Invoke the ADK InMemoryRunner synchronously for a single user turn.

    Returns (response_text, tokens_in, tokens_out).
    """
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
    main_prompt_for_context: str,
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
        "<main_prompt_for_context>",
        main_prompt_for_context,
        "</main_prompt_for_context>",
    ]
    if previous_output is not None and critic_issues:
        parts.append("<previous_skill_prompt>")
        parts.append(previous_output)
        parts.append("</previous_skill_prompt>")
        parts.append("<critic_issues>")
        for issue in critic_issues:
            parts.append(f"  - {issue}")
        parts.append("</critic_issues>")
        parts.append("Rewrite this skill prompt addressing every issue relevant to it.")
    return _run_skill_prompt(agent=agent, user_message_xml="\n".join(parts))


def call_critic(agent: LlmAgent, config: FlowMapConfig, bundle: Bundle) -> CriticResult:
    user_message_xml = "\n".join([
        _render_config_as_xml(config),
        "<bundle>",
        bundle.model_dump_json(indent=2),
        "</bundle>",
    ])
    return _run_critic(agent=agent, user_message_xml=user_message_xml)

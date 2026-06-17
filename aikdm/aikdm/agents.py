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

from aikdm.schemas import Endpoint, FlowMapConfig, Skill

MAIN_PROMPT_SYSTEM_PROMPT = """\
You are aikdm's main-prompt generator.

You receive a <flow_map_config> describing a planned AI agent and a
<main_prompt_scaffold> showing the section layout you must produce.

Your only output is the main system prompt as a single XML string.
You do NOT emit skills - each skill is generated separately. The main
prompt references skills by name through <skills_index>; you do not
write their prompt bodies.

Rules:
- Fill every LLM-synthesized tag with your own prose. Never copy the
  wizard fields (scope, business_domain, should_do, should_not_do) verbatim
  - refine them into polished, concrete instructions.
- The <workflows> section must begin with the <usage> sub-block copied
  verbatim from the scaffold, then synthesize each included flow
  (name, intent, preconditions, ordered steps referencing skill names,
  postconditions, side-effects). Exclude flows with included=false.
- The <examples> section shows 2-3 routing examples (user says X ->
  agent picks skill Y / declines / redirects). Distinct from per-skill
  examples.
- The <skills_index> lists every internal skill (name + one-line
  description) - keep it in sync with the input.
- The <external_actions> section names every external skill from input
  (skill id + external_note). Empty if there are none.
- The <tools> section lists ONLY general-purpose tools - endpoints whose
  used_by_skills in the input is empty. Tools that belong to one or more
  skills are described INSIDE that skill's prompt; never duplicate them
  at the agent level. For each general-purpose tool: name, description,
  HTTP detail (method + path), auth.
- Never use the words "capability" or "resource" in any prose section.
  The runtime agent only knows about tools. Discovery uses "endpoint"
  at its layer; in the bundle and runtime, the word is "tool".
- All XML tags use Anthropic-style.

Return structured output: {"main_prompt": "<role>...</role>...<tools/>"}
"""

SKILL_PROMPT_SYSTEM_PROMPT = """\
You are aikdm's skill-prompt generator.

You receive a <flow_map_config>, a single <target_skill> (the business-
domain skill you must write the prompt for), and a <skill_scaffold>
showing the section layout. You produce the prompt body for THIS ONE
skill.

Rules:
- Fill every LLM-synthesized tag in the scaffold with your own prose.
- The <tools> block names EVERY endpoint in target_skill.suggested_endpoints[].
  For each: a 1-2 sentence purpose and a "when to invoke" phrase mapping
  user intent to the tool. Never include HTTP method, path, or parameters
  in the prompt body - those are metadata on the endpoint, not prose.
- Never use the words "capability" or "resource" in the prompt body. The
  runtime agent only knows about tools. Discovery uses "endpoint" at its
  layer; in this skill's prompt the word is "tool".
- The <examples> block shows 2-3 execution-level examples for this skill
  (a representative turn inside the skill, not skill routing).
- Tone and guardrails must be consistent with the wider agent context
  (flow_map_config.scope, business_domain, should_not_do). You do not
  see the main_prompt body - rely on shared input for consistency.
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
- The <tools> block contains endpoints whose used_by_skills is non-empty
  in the input - these are skill-owned tools and must not appear at the
  agent level. The agent <tools> block should list only general-purpose
  tools (used_by_skills is empty).
- The <tools> block omits a general-purpose tool that should be listed
  (an endpoint whose used_by_skills is empty in the input).
- Use of the words "capability" or "resource" in any prose section.
  Capability is a v1 concept; do not use it. Discovery uses "endpoint";
  the runtime/main-prompt uses "tool".
- Examples that imply forbidden behavior (contradicting should_not_do).
- Internal contradictions between sections (e.g. should_do allowing a
  thing the guardrails forbid).

Do NOT critique skill prompt bodies - you don't see them. Do NOT flag
stylistic preferences. Return an empty list if the main_prompt is acceptable.

Return structured output: {"issues": [string, ...]}.
"""

SKILL_PROMPT_CRITIC_SYSTEM_PROMPT = """\
You are aikdm's skill-prompt critic. You receive the original
<flow_map_config>, a single <target_skill>, and a <skill_prompt> body.
Your job: identify substantive issues IN THIS SKILL PROMPT that would
mislead an agent.

Focus on:
- The <tools> block: every endpoint id in target_skill.suggested_endpoints[]
  must appear as a <tool name="..."> entry. Missing entries are a flag;
  extra entries (tools the skill should not call) are a flag.
- HTTP detail in prose body: any HTTP method (GET/POST/PUT/PATCH/DELETE)
  at the start of a line, any "fetch(", "axios.", or "/api/" path string
  is forbidden in the prompt body. HTTP detail lives on the endpoint
  metadata, not in prose.
- Use of the words "capability" or "resource" in the prompt body. Use
  "tool" instead. Capability is a v1 concept and must not appear.
- <examples> being routing examples rather than execution examples.
- Skill body contradicting the input scope or should_not_do.
- Missing or empty required sections.
- Tone/guardrails clearly inconsistent with the input's business_domain.

Do NOT critique the main_prompt - you don't see it. Do NOT flag
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
    lines.append("  <endpoints>")
    for e in config.endpoints:
        used_by = ", ".join(e.used_by_skills)
        lines.append(
            f'    <endpoint id="{_esc(e.id)}" method="{e.method}" path="{_esc(e.path)}"'
            f' auth="{_esc(e.auth)}" used_by_skills="{_esc(used_by)}">'
        )
        lines.append(f"      <prose>{_esc(e.prose_md)}</prose>")
        lines.append("    </endpoint>")
    lines.append("  </endpoints>")
    lines.append("  <skills>")
    for s in config.skills:
        lines.append(
            f'    <skill id="{_esc(s.id)}" external="{str(s.external).lower()}">'
        )
        lines.append(f"      <name>{_esc(s.name)}</name>")
        lines.append(f"      <domain>{_esc(s.domain)}</domain>")
        lines.append(f"      <description>{_esc(s.description)}</description>")
        lines.append(f"      <user_phrases>{_esc(', '.join(s.user_phrases))}</user_phrases>")
        lines.append("      <suggested_endpoints>")
        for ref in s.suggested_endpoints:
            lines.append(
                f'        <endpoint role="{ref.role}" when="{_esc(ref.when)}">{_esc(ref.endpoint)}</endpoint>'
            )
        lines.append("      </suggested_endpoints>")
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


def _render_target_skill_as_xml(skill: Skill, config: FlowMapConfig) -> str:
    endpoint_by_id = {e.id: e for e in config.endpoints}
    lines: list[str] = [f'<target_skill id="{_esc(skill.id)}">']
    lines.append(f"  <name>{_esc(skill.name)}</name>")
    lines.append(f"  <domain>{_esc(skill.domain)}</domain>")
    lines.append(f"  <description>{_esc(skill.description)}</description>")
    lines.append("  <suggested_endpoints>")
    for ref in skill.suggested_endpoints:
        lines.append(
            f'    <endpoint role="{ref.role}" when="{_esc(ref.when)}">{_esc(ref.endpoint)}</endpoint>'
        )
    lines.append("  </suggested_endpoints>")
    lines.append("  <linked_endpoints>")
    for ref in skill.suggested_endpoints:
        ep = endpoint_by_id.get(ref.endpoint)
        if ep is None:
            continue
        lines.append(
            f'    <linked_endpoint id="{_esc(ep.id)}" method="{ep.method}" path="{_esc(ep.path)}" auth="{_esc(ep.auth)}">'
        )
        lines.append(f"      <prose>{_esc(ep.prose_md)}</prose>")
        lines.append("    </linked_endpoint>")
    lines.append("  </linked_endpoints>")
    lines.append(f"  <prose>{_esc(skill.prose_md)}</prose>")
    lines.append("</target_skill>")
    return "\n".join(lines)


async def _adk_run_once(*, agent: LlmAgent, user_message_xml: str) -> tuple[str, int, int]:
    runner = InMemoryRunner(agent=agent)

    user_id = "aikdm"
    session_id = uuid.uuid4().hex
    await runner.session_service.create_session(
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

    async for event in runner.run_async(
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


async def _run_main_prompt(*, agent: LlmAgent, user_message_xml: str) -> MainPromptResult:
    response_text, tokens_in, tokens_out = await _adk_run_once(
        agent=agent, user_message_xml=user_message_xml
    )
    parsed = _MainPromptOutput.model_validate_json(response_text)
    return MainPromptResult(
        main_prompt=parsed.main_prompt, tokens_in=tokens_in, tokens_out=tokens_out
    )


async def _run_skill_prompt(*, agent: LlmAgent, user_message_xml: str) -> SkillPromptResult:
    response_text, tokens_in, tokens_out = await _adk_run_once(
        agent=agent, user_message_xml=user_message_xml
    )
    parsed = _SkillPromptOutput.model_validate_json(response_text)
    return SkillPromptResult(
        skill_name=parsed.name,
        prompt=parsed.prompt,
        tokens_in=tokens_in,
        tokens_out=tokens_out,
    )


async def _run_critic(*, agent: LlmAgent, user_message_xml: str) -> CriticResult:
    response_text, tokens_in, tokens_out = await _adk_run_once(
        agent=agent, user_message_xml=user_message_xml
    )
    parsed = _CriticOutput.model_validate_json(response_text)
    return CriticResult(
        issues=parsed.issues, tokens_in=tokens_in, tokens_out=tokens_out
    )


async def call_main_prompt(
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
    return await _run_main_prompt(agent=agent, user_message_xml="\n".join(parts))


async def call_skill_prompt(
    agent: LlmAgent,
    config: FlowMapConfig,
    skill: Skill,
    scaffold: str,
    *,
    previous_output: str | None = None,
    critic_issues: list[str] | None = None,
) -> SkillPromptResult:
    parts = [
        _render_config_as_xml(config),
        _render_target_skill_as_xml(skill, config),
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
    return await _run_skill_prompt(agent=agent, user_message_xml="\n".join(parts))


async def call_main_prompt_critic(
    agent: LlmAgent, config: FlowMapConfig, main_prompt: str
) -> CriticResult:
    user_message_xml = "\n".join([
        _render_config_as_xml(config),
        "<main_prompt>",
        main_prompt,
        "</main_prompt>",
    ])
    return await _run_critic(agent=agent, user_message_xml=user_message_xml)


async def call_skill_prompt_critic(
    agent: LlmAgent,
    config: FlowMapConfig,
    skill: Skill,
    skill_prompt: str,
) -> CriticResult:
    user_message_xml = "\n".join([
        _render_config_as_xml(config),
        _render_target_skill_as_xml(skill, config),
        "<skill_prompt>",
        skill_prompt,
        "</skill_prompt>",
    ])
    return await _run_critic(agent=agent, user_message_xml=user_message_xml)

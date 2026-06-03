"""ADK agent factories + thin callers.

The orchestrator depends on `call_generator` and `call_critic`. Tests for
the orchestrator monkey-patch `_run_generator` and `_run_critic` to avoid
any LLM calls. Real ADK invocation lives only in those two underscore
functions, making them the second mocking seam (in addition to
models.build_model).
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

from aikdm.schemas import Bundle, FlowMapConfig

GENERATOR_SYSTEM_PROMPT = """\
You are aikdm's prompt generator.

You receive a <flow_map_config> describing a planned AI agent: business
domain, scope, what it should and should not do, capabilities (backend
endpoints), skills (the agent's high-level abilities), and flows
(business processes).

Your job: emit a Bundle (main_prompt + per-skill prompts + external_actions)
following the prompt schema. Honor the user-provided scaffolds — fill in
LLM-synthesized sections, keep wizard-copied sections verbatim, never
remove tags from the scaffolds.

Constraints:
- One skill entry per internal (external=false) skill.
- Each skill entry has: name (matches the input skill id), description
  (one-line summary used by the dispatcher to decide when to load this
  skill — Anthropic skill style), and prompt (the XML body loaded when
  the dispatcher selects this skill).
- External (external=true) skills appear ONLY in main_prompt's
  <external_actions> and the bundle's external_actions list — never as
  a skill entry.
- Each skill's <resources> block in its prompt names a single mcp_server
  with name=proposed_tool. Never include HTTP method, path, or parameters.
- All XML tags inside main_prompt and skill prompt bodies use
  Anthropic-style XML.

Return structured output matching the Bundle schema.
"""

CRITIC_SYSTEM_PROMPT = """\
You are aikdm's prompt critic. You receive the original <flow_map_config>
and the generator's <bundle>. Your job: identify substantive issues that
would mislead an agent at runtime.

Focus on:
- Inconsistency between main_prompt guardrails and skill-level guardrails.
- Skills that contradict should_not_do.
- Missing skills for capabilities the user clearly wants used.
- External actions promised as internal skills.
- Examples that imply forbidden behavior.

Do NOT flag stylistic preferences, alternative phrasings, or things
already correct. Return an empty list if the bundle is acceptable.

Return structured output: {"issues": [string, ...]}.
"""


@dataclass(frozen=True)
class GeneratorResult:
    bundle: Bundle
    tokens_in: int
    tokens_out: int


@dataclass(frozen=True)
class CriticResult:
    issues: list[str]
    tokens_in: int
    tokens_out: int


class _CriticOutput(BaseModel):
    issues: list[str]


def build_generator_agent(model: Any) -> LlmAgent:
    return LlmAgent(
        name="aikdm_generator",
        model=model,
        instruction=GENERATOR_SYSTEM_PROMPT,
        output_schema=Bundle,
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
    Used as the user message body for both generator and critic.
    """
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
            f'intent="{_esc(f.intent)}">{_esc(f.description)}</flow>'
        )
    lines.append("  </flows>")
    lines.append("</flow_map_config>")
    return "\n".join(lines)


def _adk_run_once(*, agent: LlmAgent, user_message_xml: str) -> tuple[str, int, int]:
    """Invoke the ADK InMemoryRunner synchronously for a single user turn.

    Returns a tuple of (response_text, tokens_in, tokens_out) where
    response_text is the raw text of the final model response (JSON when
    output_schema is set on the agent).

    ADK's Runner.run() is already sync — it bridges to async internally via a
    background thread, so no asyncio.run() wrapping is needed here.

    Reference: https://google.github.io/adk-docs/runners/
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
        # Accumulate token usage from any event that carries it.
        if event.usage_metadata is not None:
            um = event.usage_metadata
            # TODO: capture token usage more precisely per provider
            tokens_in = um.prompt_token_count or 0
            tokens_out = um.candidates_token_count or 0

        # Capture the final model response text.
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


def _run_generator(
    *,
    agent: LlmAgent,
    user_message_xml: str,
) -> GeneratorResult:
    """Execute the ADK agent for generation.

    The generator agent has output_schema=Bundle, so ADK instructs the model
    to return structured JSON. We parse the final response text as a Bundle.

    Reference: https://google.github.io/adk-docs/agents/llm-agents/
    """
    response_text, tokens_in, tokens_out = _adk_run_once(
        agent=agent, user_message_xml=user_message_xml
    )
    bundle = Bundle.model_validate_json(response_text)
    return GeneratorResult(bundle=bundle, tokens_in=tokens_in, tokens_out=tokens_out)


def _run_critic(
    *,
    agent: LlmAgent,
    user_message_xml: str,
) -> CriticResult:
    """Execute the ADK agent for criticism.

    The critic agent has output_schema=_CriticOutput, so the model returns
    {"issues": [...]}. We parse and return a CriticResult.
    """
    response_text, tokens_in, tokens_out = _adk_run_once(
        agent=agent, user_message_xml=user_message_xml
    )
    critic_output = _CriticOutput.model_validate_json(response_text)
    return CriticResult(
        issues=critic_output.issues, tokens_in=tokens_in, tokens_out=tokens_out
    )


def call_generator(
    agent: LlmAgent,
    config: FlowMapConfig,
    scaffold_main: str,
    scaffold_skills: dict[str, str],
    previous_bundle: Bundle | None = None,
    previous_issues: list[str] | None = None,
) -> GeneratorResult:
    parts = [
        _render_config_as_xml(config),
        "<main_prompt_scaffold>",
        scaffold_main,
        "</main_prompt_scaffold>",
        "<skill_scaffolds>",
    ]
    for sid, scaff in scaffold_skills.items():
        parts.append(f'  <skill_scaffold id="{sid}">')
        parts.append(scaff)
        parts.append("  </skill_scaffold>")
    parts.append("</skill_scaffolds>")
    if previous_bundle is not None and previous_issues:
        parts.append("<previous_bundle>")
        parts.append(previous_bundle.model_dump_json(indent=2))
        parts.append("</previous_bundle>")
        parts.append("<critic_issues>")
        for issue in previous_issues:
            parts.append(f"  - {issue}")
        parts.append("</critic_issues>")
        parts.append("Rewrite the bundle addressing every issue.")
    user_message_xml = "\n".join(parts)
    return _run_generator(agent=agent, user_message_xml=user_message_xml)


def call_critic(agent: LlmAgent, config: FlowMapConfig, bundle: Bundle) -> CriticResult:
    user_message_xml = "\n".join([
        _render_config_as_xml(config),
        "<bundle>",
        bundle.model_dump_json(indent=2),
        "</bundle>",
    ])
    return _run_critic(agent=agent, user_message_xml=user_message_xml)

"""Teacher LLM. Reads the current bundle + last eval judgments + tried-patches
history and proposes 0-3 section-level patches. XML-block prompt structure
follows Anthropic's prompt-engineering guidance."""

from __future__ import annotations

import uuid
from dataclasses import dataclass
from typing import Any

from google.adk.agents import LlmAgent  # type: ignore[import-untyped]
from google.adk.runners import InMemoryRunner
from google.genai import types as _genai_types

from aikdm.train.patcher import section_ids
from aikdm.train.schemas import SectionPatch, TeacherOutput


TEACHER_SYSTEM_PROMPT = """\
<role>
You are an expert prompt engineer for Claude models, coaching a target
AI agent that is being evaluated against a fixed test dataset. Your job
is to rewrite specific sections of the target agent's prompt so it
performs better against the FAILING acceptance criteria while preserving
what already passes.
</role>

<task>
Given the target agent's current prompt bundle, its most recent
evaluation results (per-criterion pass/fail with reasons), and a history
of edits that were already tried and did not help, propose 0-3 targeted
section-level patches. Each patch fully replaces one section.
</task>

<prompt_engineering_playbook>
Apply Anthropic's published prompt-engineering techniques when rewriting.
In roughly descending order of leverage for agents like this one:

1. USE MULTISHOT EXAMPLES. This is one of the highest-impact techniques.
   When a criterion is about a specific behavior (a greeting, a
   formatting convention, a tool-call sequence, a refusal pattern),
   embed 2-3 concrete `<example>` blocks inside the section showing the
   desired user turn and agent response. Diverse examples beat one
   canonical example. Wrap each in `<example>...</example>` XML tags —
   Claude models are trained to attend to those.

2. USE XML TAGS TO STRUCTURE THE SECTION. Wrap logically distinct pieces
   (instructions, examples, guardrails, tool guidance) in named tags.
   The target agent's `main_prompt` already uses tags like `<role>`,
   `<scope>`, `<personality>`, `<should_do>`, `<should_not_do>`,
   `<workflows>`, `<examples>` — preserve them and add content INSIDE
   them rather than deleting them. Skill prompts follow a similar
   XML-block layout.

3. GIVE CLAUDE TIME TO THINK. For criteria that require multi-step
   reasoning (planning tool calls, disambiguating user intent), add
   an explicit chain-of-thought instruction. Preferred form: "Before
   answering, think inside <thinking>...</thinking> tags about ...".
   Only the final answer is shown to the user; the `<thinking>` block
   is stripped.

4. BE CLEAR AND DIRECT. Prefer positive imperatives ("Always greet the
   user by name") over negatives ("Don't be rude"). Be specific about
   exact wording, exact tool names, exact formats. Vague guidance
   ("be helpful") produces vague behavior.

5. ASSIGN A CLEAR ROLE. If `main_prompt.<role>` is thin, sharpen it
   with domain expertise ("You are a customer support specialist for
   an e-commerce store"). Roles anchor tone and knowledge.

6. PREFER ADDITIONS TO DELETIONS. If a passing behavior is grounded in
   the existing text, do not rip it out. New examples, additional
   `should_do` bullets, or a new `<thinking>` instruction are lower-risk
   than rewriting a section from scratch.
</prompt_engineering_playbook>

<example>
Below is a hypothetical illustration of a well-formed patch. In this
example the criterion "greets politely and by name when known" failed.
The teacher adds an `<examples>` sub-block with a multishot demo, and
strengthens the `<should_do>` instruction — nothing else is touched.

Input:
  <last_eval>
    <judgment criterion="greets politely and by name when known"
              passed="false" reason="agent went straight to 'what do you need?'"/>
  </last_eval>

Good patch:
  {"section_id":"main_prompt",
   "new":"<role>You are a customer support specialist for AcmeStore.</role>\\n<should_do>\\n- Always open with a friendly greeting. If the user's name is known from context, address them by name.\\n- ...(rest preserved)...\\n</should_do>\\n<examples>\\n<example>\\nUser: hi\\nAgent: Hi Jamie — happy to help. What can I do for you today?\\n</example>\\n<example>\\nUser: hey\\nAgent: Hey there! What are you looking to sort out?\\n</example>\\n</examples>\\n...(rest of sections preserved verbatim)...",
   "rationale":"Failing criterion is greeting-shaped; adds two diverse example greetings and a positive should_do bullet without removing existing structure."}
</example>

<constraints>
- Emit 0-3 patches per epoch. Never more.
- Each patch fully replaces ONE section — output the entire new section
  text, including XML sub-tags. Do not output diffs or partial fragments.
- Only edit sections whose content plausibly caused a failing judgment.
  If a criterion failed because a tool call was missing, edit the
  section that discusses tool routing, not the personality section.
- Do NOT re-attempt a patch whose `section_id` and `rationale` match
  something in <tried_patches>.
- If no promising edit exists, emit patches=[] and explain in
  `focus_notes` what evidence you'd need to try again.
- Valid section_id values are listed under <available_sections>.
  Anything else will be rejected by the patcher.
- Preserve every XML tag that was present in the section. Add tags if
  it helps, but never strip existing structure.
</constraints>

Return exactly one JSON object matching:
{"patches":[{"section_id":"...","new":"...","rationale":"..."}, ...],
 "focus_notes":"..."}
"""


@dataclass
class TeacherTurn:
    output: TeacherOutput
    tokens_in: int
    tokens_out: int


def build_teacher_agent(model) -> LlmAgent:
    return LlmAgent(
        name="aikdm_teacher",
        model=model,
        instruction=TEACHER_SYSTEM_PROMPT,
        output_schema=TeacherOutput,
    )


async def _adk_run_once(*, agent, user_message_xml: str) -> tuple[str, int, int]:
    runner = InMemoryRunner(agent=agent)
    user_id = "aikdm"
    session_id = uuid.uuid4().hex
    await runner.session_service.create_session(
        app_name=runner.app_name, user_id=user_id, session_id=session_id,
    )
    new_message = _genai_types.Content(
        parts=[_genai_types.Part.from_text(text=user_message_xml)], role="user",
    )
    response_text = ""
    tokens_in = tokens_out = 0
    async for event in runner.run_async(
        user_id=user_id, session_id=session_id, new_message=new_message,
    ):
        if event.usage_metadata is not None:
            tokens_in = event.usage_metadata.prompt_token_count or 0
            tokens_out = event.usage_metadata.candidates_token_count or 0
        if event.is_final_response() and event.content and event.content.parts:
            response_text = "".join(
                p.text for p in event.content.parts
                if p.text and not getattr(p, "thought", False)
            )
    if not response_text:
        raise RuntimeError("teacher produced no final response")
    return response_text, tokens_in, tokens_out


async def propose_patches(
    *,
    agent,
    bundle: dict[str, Any],
    eval_result: dict[str, Any],       # decoded EvalResult (dict form)
    tried: list[SectionPatch],
) -> TeacherTurn:
    """Ask the teacher for the next patch set. Returns a TeacherTurn with
    parsed TeacherOutput + token counters."""
    xml = (
        "<available_sections>\n"
        + _render_sections(bundle)
        + "\n</available_sections>\n"
        "<last_eval>\n"
        + _render_last_eval(eval_result)
        + "\n</last_eval>\n"
        "<tried_patches>\n"
        + _render_tried(tried)
        + "\n</tried_patches>"
    )
    raw, t_in, t_out = await _adk_run_once(agent=agent, user_message_xml=xml)
    parsed = TeacherOutput.model_validate_json(raw)
    return TeacherTurn(output=parsed, tokens_in=t_in, tokens_out=t_out)


def _render_sections(bundle: dict[str, Any]) -> str:
    out: list[str] = []
    ids = section_ids(bundle)
    for sid in ids:
        if sid == "main_prompt":
            body = str(bundle.get("main_prompt", ""))
        else:
            # skill.<name>.prompt
            name = sid[len("skill.") : -len(".prompt")]
            body = ""
            for s in bundle.get("skills") or []:
                if s.get("name") == name:
                    body = str(s.get("prompt", ""))
                    break
        out.append(f'  <section id="{sid}">\n```\n{body}\n```\n  </section>')
    return "\n".join(out)


def _render_last_eval(eval_result: dict[str, Any]) -> str:
    lines: list[str] = [f'  <score>{eval_result.get("score", 0.0)}</score>',
                        '  <judgments>']
    for sess in eval_result.get("sessions", []) or []:
        for j in sess.get("judgments", []) or []:
            lines.append(
                f'    <judgment criterion="{_esc(j.get("criterion",""))}" '
                f'passed="{str(bool(j.get("passed", False))).lower()}" '
                f'reason="{_esc(j.get("reason",""))}"/>'
            )
    lines.append("  </judgments>")
    return "\n".join(lines)


def _render_tried(tried: list[SectionPatch]) -> str:
    if not tried:
        return "  <!-- none -->"
    return "\n".join(
        f'  <attempt section_id="{p.section_id}" rationale="{_esc(p.rationale)}"/>'
        for p in tried
    )


def _esc(s: str) -> str:
    return s.replace("&", "&amp;").replace("<", "&lt;").replace('"', "&quot;")

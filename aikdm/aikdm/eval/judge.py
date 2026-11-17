"""LLM judge. One call per turn: rules on <turn_under_test> only, with earlier
conversation as <context>."""

from __future__ import annotations

import json
import uuid
from dataclasses import dataclass

from google.adk.agents import LlmAgent  # type: ignore[import-untyped]
from google.adk.runners import InMemoryRunner
from google.genai import types as _genai_types
from pydantic import BaseModel, ValidationError

from aikdm.eval.schemas import Judgment
from aikdm.eval.script import CriterionItem

JUDGE_SYSTEM_PROMPT = """\
You are aikdm's eval judge.

You receive:
- <context>: the conversation BEFORE the turn under test (JSON). Context only.
- <turn_under_test>: the user message and the agent's full reply span for
  this turn (JSON): every assistant message, its tool calls and results, and
  the final text.
- <criteria>: a numbered list of acceptance criteria for THIS turn.

For each criterion, decide whether the agent's behavior in <turn_under_test>
satisfies it. Tool calls in <turn_under_test> are part of the behavior being
judged. Do not judge <context>. Be lenient on wording, strict on substance.
Return exactly one entry per criterion, in the same order.

Return structured output:
{"judgments":[{"criterion":"<verbatim criterion text>",
              "passed":true|false,"reason":"<one sentence>"}]}
"""


class _JudgeOutput(BaseModel):
    judgments: list[dict]


@dataclass
class JudgeResult:
    judgments: list[Judgment]
    passed: int
    total: int
    tokens_in: int
    tokens_out: int


class JudgeError(Exception):
    """Judge call failed, didn't parse, or returned the wrong number of entries."""


def build_judge_agent(model) -> LlmAgent:
    return LlmAgent(
        name="aikdm_eval_judge",
        model=model,
        instruction=JUDGE_SYSTEM_PROMPT,
        output_schema=_JudgeOutput,
        generate_content_config=_genai_types.GenerateContentConfig(temperature=0),
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
                part.text for part in event.content.parts
                if part.text and not getattr(part, "thought", False)
            )
    if not response_text:
        raise RuntimeError("judge produced no final response")
    return response_text, tokens_in, tokens_out


async def judge_turn(
    *, agent, context: list[dict], turn_under_test: list[dict],
    criteria_items: list[CriterionItem],
) -> JudgeResult:
    criteria_text = "\n".join(f"{i+1}. {c.criterion}" for i, c in enumerate(criteria_items))
    user_message_xml = (
        "<context>\n" + json.dumps(context, indent=2) + "\n</context>\n"
        "<turn_under_test>\n" + json.dumps(turn_under_test, indent=2) + "\n</turn_under_test>\n"
        "<criteria>\n" + criteria_text + "\n</criteria>"
    )
    raw, t_in, t_out = await _adk_run_once(agent=agent, user_message_xml=user_message_xml)
    try:
        parsed = _JudgeOutput.model_validate_json(raw)
    except ValidationError as e:
        raise JudgeError(f"unparseable judge output: {e.errors()[0]['msg']}") from e
    if len(parsed.judgments) != len(criteria_items):
        raise JudgeError(
            f"expected {len(criteria_items)} judgments, got {len(parsed.judgments)}")
    for i, entry in enumerate(parsed.judgments):
        if not isinstance(entry.get("passed"), bool):
            raise JudgeError(f"judge entry {i} has no boolean 'passed'")
    judgments = [
        Judgment(message_id=item.message_id, criterion=item.criterion,
                 passed=entry["passed"],
                 reason=str(entry.get("reason", "")))
        for item, entry in zip(criteria_items, parsed.judgments, strict=True)
    ]
    passed = sum(1 for j in judgments if j.passed)
    return JudgeResult(judgments=judgments, passed=passed, total=len(judgments),
                       tokens_in=t_in, tokens_out=t_out)

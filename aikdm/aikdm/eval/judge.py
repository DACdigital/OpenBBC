"""LLM judge. Scores a completed simulated transcript against a bag of
criteria. Not tied to per-message positions — the judge sees the whole
transcript and rules on each criterion as a session-level rubric item."""

from __future__ import annotations

import json
import uuid
from dataclasses import dataclass

from google.adk.agents import LlmAgent  # type: ignore[import-untyped]
from google.adk.runners import InMemoryRunner
from google.genai import types as _genai_types
from pydantic import BaseModel

from aikdm.eval.schemas import Judgment

JUDGE_SYSTEM_PROMPT = """\
You are aikdm's eval judge.

You receive:
- A <transcript> block: a full conversation between a simulated user and
  the tested agent, in JSON.
- A <criteria> block: a numbered list of acceptance criteria to check
  against the whole transcript.

For each criterion, decide whether the tested agent's behavior in the
transcript satisfies it. You are lenient on wording differences and strict
on substance. Return one entry per criterion in the same order.

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


def build_judge_agent(model) -> LlmAgent:
    return LlmAgent(
        name="aikdm_eval_judge",
        model=model,
        instruction=JUDGE_SYSTEM_PROMPT,
        output_schema=_JudgeOutput,
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


async def judge_transcript(
    *, agent, transcript: list[dict], criteria_items: list[dict],
) -> JudgeResult:
    """criteria_items is a list of {message_id, criterion} dicts. The judge
    ignores message_id (session-level rubric) but we thread it through so we
    can attribute each Judgment back to the original feedback row."""
    criteria_text = "\n".join(f"{i+1}. {c['criterion']}" for i, c in enumerate(criteria_items))
    user_message_xml = (
        "<transcript>\n" + json.dumps(transcript, indent=2) + "\n</transcript>\n"
        "<criteria>\n" + criteria_text + "\n</criteria>"
    )
    raw, t_in, t_out = await _adk_run_once(agent=agent, user_message_xml=user_message_xml)
    parsed = _JudgeOutput.model_validate_json(raw)
    judgments: list[Judgment] = []
    for i, entry in enumerate(parsed.judgments):
        item = criteria_items[i] if i < len(criteria_items) else {"message_id": ""}
        judgments.append(Judgment(
            message_id=item.get("message_id", ""),
            criterion=str(entry.get("criterion", item.get("criterion", ""))),
            passed=bool(entry.get("passed", False)),
            reason=str(entry.get("reason", "")),
        ))
    passed = sum(1 for j in judgments if j.passed)
    return JudgeResult(
        judgments=judgments, passed=passed, total=len(judgments),
        tokens_in=t_in, tokens_out=t_out,
    )

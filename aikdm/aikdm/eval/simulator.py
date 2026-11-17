"""User simulator, used only in AIKDM_EVAL_USER_TURN_MODE=paraphrase. The
runner decides WHICH turn to send (reference user turn k, passed as
<intent>); the simulator only rewrites its wording. It never decides whether
the conversation continues."""

from __future__ import annotations

import json
import uuid
from dataclasses import dataclass

from google.adk.agents import LlmAgent  # type: ignore[import-untyped]
from google.adk.runners import InMemoryRunner
from google.genai import types as _genai_types
from pydantic import BaseModel

USER_SIMULATOR_SYSTEM_PROMPT = """\
You rewrite one user message for an evaluation of an AI agent.

You receive:
- <reference_transcript>: the original session, for tone and language.
- <so_far>: the simulated conversation so far with the agent under test.
- <intent>: the exact user message to send now.

Write <intent> as the next user message, adapted only in connective wording
so it reads naturally after the agent's last reply in <so_far>.

Rules:
- Always send the intent, even if the agent's last reply asked a question.
  Never answer the agent's question instead of sending the intent.
- Never add, merge, split, or skip messages. Exactly one message: the intent.
- Keep every id, number, date, name, and any pasted JSON or document
  verbatim. Fix obvious typos only in ordinary words.
- Same language and register as the reference user.
- Plain message text only: no quotes, no JSON wrapper, no commentary.

Return exactly one JSON object: {"content": "<the user message>"}
"""


class _SimulatorOutput(BaseModel):
    content: str


@dataclass
class SimulatorTurn:
    content: str
    tokens_in: int
    tokens_out: int


def build_simulator_agent(model) -> LlmAgent:
    return LlmAgent(
        name="aikdm_user_simulator",
        model=model,
        instruction=USER_SIMULATOR_SYSTEM_PROMPT,
        output_schema=_SimulatorOutput,
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
        raise RuntimeError("user_simulator produced no final response")
    return response_text, tokens_in, tokens_out


async def next_user_turn(
    *, agent, reference_transcript: list[dict], simulated_so_far: list[dict], intent: str,
) -> SimulatorTurn:
    payload = (
        "<reference_transcript>\n" + json.dumps(reference_transcript, indent=2)
        + "\n</reference_transcript>\n"
        "<so_far>\n" + json.dumps(simulated_so_far, indent=2) + "\n</so_far>\n"
        "<intent>\n" + intent + "\n</intent>"
    )
    raw, t_in, t_out = await _adk_run_once(agent=agent, user_message_xml=payload)
    parsed = _SimulatorOutput.model_validate_json(raw)
    return SimulatorTurn(content=parsed.content, tokens_in=t_in, tokens_out=t_out)

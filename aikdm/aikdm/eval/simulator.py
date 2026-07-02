"""User simulator. Given the original session's transcript as a "recipe",
produces the next user turn against the tested agent. The prompt encourages
faithful replay of intent while allowing paraphrase (the tested agent's
responses will diverge, so exact replay of the original user turns can
lead the conversation off the rails)."""

from __future__ import annotations

import json
import uuid
from dataclasses import dataclass

from google.adk.agents import LlmAgent  # type: ignore[import-untyped]
from google.adk.runners import InMemoryRunner
from google.genai import types as _genai_types
from pydantic import BaseModel

USER_SIMULATOR_SYSTEM_PROMPT = """\
You simulate an end-user speaking to an AI agent under evaluation.

You receive:
- <reference_transcript>: the ORIGINAL session, which is your recipe.
- <so_far>: the CURRENT simulated conversation up to this point.

Your task: emit the NEXT user turn only. Follow the reference transcript's
intent step by step, but adapt phrasing so the flow makes sense given the
tested agent's (possibly different) responses. If the reference is exhausted
and the goal appears met, emit an empty content string and stop=true.

Return structured output: {"content":"<user message>","stop":false|true}
"""


class _SimulatorOutput(BaseModel):
    content: str
    stop: bool = False


@dataclass
class SimulatorTurn:
    content: str
    stop: bool
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
    *, agent, reference_transcript: list[dict], simulated_so_far: list[dict],
) -> SimulatorTurn:
    payload = (
        "<reference_transcript>\n" + json.dumps(reference_transcript, indent=2) + "\n</reference_transcript>\n"
        "<so_far>\n" + json.dumps(simulated_so_far, indent=2) + "\n</so_far>"
    )
    raw, t_in, t_out = await _adk_run_once(agent=agent, user_message_xml=payload)
    parsed = _SimulatorOutput.model_validate_json(raw)
    return SimulatorTurn(content=parsed.content, stop=parsed.stop, tokens_in=t_in, tokens_out=t_out)

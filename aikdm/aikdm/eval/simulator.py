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
- <reference_transcript>: the ORIGINAL session, which is your recipe
  showing what the human user wanted and how the conversation flowed.
- <so_far>: the CURRENT simulated conversation up to this point (with a
  potentially DIFFERENT tested agent that may respond differently).

Your job is to REPLAY the reference user turns, one at a time, adapted to
the tested agent's actual replies. You are NOT a proactive user — do not
invent new questions, new follow-ups, or new topics the reference user
never raised. The reference transcript is the script; you follow it.

Set the `content` field to the next user turn — a natural-language message
in the SAME language and register as the reference user turns. Write it as
if you were the human user. Do NOT wrap the message in JSON. Do NOT quote
it. Do NOT repeat prior turns.

How to pick the next turn:
1. Count how many user turns are already in <so_far> (call it k).
2. Read the (k+1)-th user turn from <reference_transcript>.
3. If it exists, paraphrase it to fit the tested agent's most recent
   reply — keep the same intent and any concrete values (ids, numbers,
   dates), and fix obvious typos (e.g. "prodycts" → "products"). Emit
   that as `content`, stop=false.
4. If it does NOT exist (i.e. every reference user turn is already
   covered in <so_far> and the tested agent has responded to the last
   one), set stop=true and leave `content` empty. This is the normal
   end. Do NOT invent an extra "thanks" / "one more thing" / follow-up
   turn just to keep talking.

Also stop when the tested agent has clearly stalled unrecoverably and
another user turn will not help. Never stop mid-flow just because a
reference step is hard — try to steer once, then stop.

You MUST return exactly one JSON object with two fields:
- content: string  (the plain user message; empty when stop=true)
- stop:    boolean (whether the conversation should end now)
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

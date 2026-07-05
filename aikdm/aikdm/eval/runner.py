"""Per-session eval runner. Ties the simulator, the target agent, the tool
mock, and the judge together."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from aikdm.eval import judge as judge_mod
from aikdm.eval import simulator as sim_mod
from aikdm.eval import target as target_mod
from aikdm.eval.schemas import InputSession, ResultSession


@dataclass
class SessionOutcome:
    session_id: str
    result: ResultSession
    tokens_in: int
    tokens_out: int
    error: str = ""


async def run_session(
    *,
    session: InputSession,
    bundle: dict[str, Any],
    simulator_agent,
    judge_agent,
    target_model: str,
    tool_caller,
) -> SessionOutcome:
    ref_transcript = [m.model_dump() for m in session.transcript]
    system_prompt = target_mod.build_system_prompt(bundle)
    tools_spec, _ = target_mod.build_tool_specs(bundle)

    simulated: list[dict[str, Any]] = []
    max_user_turns = _count_user_turns(ref_transcript) + 4
    tokens_in = tokens_out = 0

    for _ in range(max_user_turns):
        turn = await sim_mod.next_user_turn(
            agent=simulator_agent,
            reference_transcript=ref_transcript,
            simulated_so_far=simulated,
        )
        tokens_in += turn.tokens_in
        tokens_out += turn.tokens_out
        if turn.stop or not turn.content.strip():
            break
        simulated.append({"role": "user", "content": turn.content})
        t = await target_mod.target_turn(
            model=target_model,
            system_prompt=system_prompt,
            tools_spec=tools_spec,
            conversation=simulated,
            tool_caller=tool_caller,
        )
        tokens_in += t.tokens_in
        tokens_out += t.tokens_out
        simulated.append(t.assistant_message)

    criteria_items: list[dict[str, Any]] = []
    for crit in session.criteria:
        for item in crit.items:
            criteria_items.append({"message_id": crit.message_id, "criterion": item})

    if not criteria_items:
        return SessionOutcome(
            session_id=session.session_id,
            result=ResultSession(
                session_id=session.session_id,
                score=1.0, total_criteria=0, passed_criteria=0,
                transcript=simulated, judgments=[],
            ),
            tokens_in=tokens_in, tokens_out=tokens_out,
        )

    judge_result = await judge_mod.judge_transcript(
        agent=judge_agent, transcript=simulated, criteria_items=criteria_items,
    )
    tokens_in += judge_result.tokens_in
    tokens_out += judge_result.tokens_out
    score = judge_result.passed / max(1, judge_result.total)
    return SessionOutcome(
        session_id=session.session_id,
        result=ResultSession(
            session_id=session.session_id,
            score=score,
            total_criteria=judge_result.total,
            passed_criteria=judge_result.passed,
            transcript=simulated,
            judgments=judge_result.judgments,
        ),
        tokens_in=tokens_in, tokens_out=tokens_out,
    )


def _count_user_turns(transcript: list[dict[str, Any]]) -> int:
    return sum(1 for m in transcript if m.get("role") == "user")

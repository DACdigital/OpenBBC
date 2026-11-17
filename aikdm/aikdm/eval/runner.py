# aikdm/eval/runner.py
"""Per-session eval executor. Compiles the reference session into a step
script (script.py) and runs it: UserStep → AgentStep → JudgeStep per turn.
The step list is fixed at compile time; steps are only ever skipped, and only
after a target failure in simulated history mode."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from aikdm.eval import judge as judge_mod
from aikdm.eval import script as script_mod
from aikdm.eval import simulator as sim_mod
from aikdm.eval import target as target_mod
from aikdm.eval.history import reference_entries
from aikdm.eval.schemas import InputSession, Judgment, ResultSession

UNMAPPED_PREFIX = "[unmapped message_id {id}, judged against last turn] "


@dataclass
class SessionOutcome:
    session_id: str
    result: ResultSession
    tokens_in: int
    tokens_out: int
    error: str = ""


def _inconclusive(items, reason: str) -> list[Judgment]:
    return [Judgment(message_id=c.message_id, criterion=c.criterion, passed=False,
                     inconclusive=True, reason=_prefixed(c, reason)) for c in items]


def _prefixed(item: script_mod.CriterionItem, reason: str) -> str:
    return (UNMAPPED_PREFIX.format(id=item.message_id) + reason) if item.unmapped else reason


async def run_session(
    *,
    session: InputSession,
    bundle: dict[str, Any],
    simulator_agent,
    judge_agent,
    target_model: str,
    tool_caller,
    user_turn_mode: str = "verbatim",
    history_mode: str = "simulated",
) -> SessionOutcome:
    script = script_mod.compile_script(session)
    if script.turn_count == 0:
        js = _inconclusive(script.unrouted, "no user turns in reference transcript")
        return _outcome(session, [], js, 0, 0)

    system_prompt = target_mod.build_system_prompt(bundle)
    tools_spec, _ = target_mod.build_tool_specs(bundle)
    declared = {t["function"]["name"] for t in tools_spec}
    ref_transcript = [m.model_dump() for m in session.transcript]

    transcript: list[dict[str, Any]] = []       # result: user k + generated span k
    reference_history: list[dict[str, Any]] = []
    user_entry: dict[int, dict[str, Any]] = {}
    context: dict[int, list[dict[str, Any]]] = {}
    spans: dict[int, list[dict[str, Any]]] = {}
    target_error: dict[int, str] = {}
    stopped: tuple[int, str] | None = None      # (turn j, cause) — simulated mode only
    judgments: list[Judgment] = []
    tokens_in = tokens_out = 0

    for step in script.steps:
        k = step.turn
        if isinstance(step, script_mod.UserStep):
            if stopped:
                continue
            verbatim = script_mod.render_user_turn(step.ref_message)
            text = verbatim
            if user_turn_mode == "paraphrase":
                try:
                    t = await sim_mod.next_user_turn(
                        agent=simulator_agent, reference_transcript=ref_transcript,
                        simulated_so_far=transcript, intent=verbatim)
                    tokens_in += t.tokens_in
                    tokens_out += t.tokens_out
                    text = t.content.strip() or verbatim
                except Exception:  # noqa: BLE001 — simulator can never fail a session
                    text = verbatim
            user_entry[k] = {"role": "user", "content": text}

        elif isinstance(step, script_mod.AgentStep):
            ref_user = {"role": "user", "content": script_mod.render_user_turn(
                _ref_user(script, k))}
            if not stopped:
                before = list(reference_history if history_mode == "reference" else transcript)
                context[k] = before
                try:
                    tt = await target_mod.target_turn(
                        model=target_model, system_prompt=system_prompt, tools_spec=tools_spec,
                        history=before + [user_entry[k]], tool_caller=tool_caller)
                    tokens_in += tt.tokens_in
                    tokens_out += tt.tokens_out
                    spans[k] = tt.span
                    transcript.extend([user_entry[k], *tt.span])
                    if tt.budget_exceeded and history_mode == "simulated":
                        stopped = (k, "exceeded tool-call budget")
                except Exception as e:  # noqa: BLE001 — becomes an inconclusive verdict
                    target_error[k] = str(e)
                    transcript.append(user_entry[k])
                    if history_mode == "simulated":
                        stopped = (k, f"target error: {e}")
            reference_history.extend(
                [ref_user, *reference_entries(step.ref_span, declared_tools=declared)])

        else:  # JudgeStep
            if k in target_error:
                judgments += _inconclusive(step.criteria,
                                           f"turn {k} target error: {target_error[k]}")
                continue
            if k not in spans:
                j, cause = stopped  # type: ignore[misc]
                judgments += _inconclusive(step.criteria, f"turn {k} not reached: turn {j} {cause}")
                continue
            try:
                jr = await judge_mod.judge_turn(
                    agent=judge_agent, context=context[k],
                    turn_under_test=[user_entry[k], *spans[k]],
                    criteria_items=step.criteria)
                tokens_in += jr.tokens_in
                tokens_out += jr.tokens_out
                judgments += [j.model_copy(update={"reason": _prefixed(c, j.reason)})
                              for c, j in zip(step.criteria, jr.judgments, strict=True)]
            except Exception as e:  # noqa: BLE001 — judge failure is per-turn
                judgments += _inconclusive(step.criteria, f"judge error: {e}")

    return _outcome(session, transcript, judgments, tokens_in, tokens_out)


def _ref_user(script: script_mod.Script, k: int):
    return next(s.ref_message for s in script.steps
                if isinstance(s, script_mod.UserStep) and s.turn == k)


def _outcome(session, transcript, judgments, tokens_in, tokens_out) -> SessionOutcome:
    total = len(judgments)
    passed = sum(1 for j in judgments if j.passed)
    score = passed / total if total else 1.0
    return SessionOutcome(
        session_id=session.session_id,
        result=ResultSession(session_id=session.session_id, score=score,
                             total_criteria=total, passed_criteria=passed,
                             transcript=transcript, judgments=judgments),
        tokens_in=tokens_in, tokens_out=tokens_out,
    )

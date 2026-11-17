# aikdm/eval/runner.py
"""Per-session eval executor. Compiles the reference session into a step
script (script.py) and runs it: UserStep → AgentStep → JudgeStep per turn.
The step list is fixed at compile time; steps are only ever skipped, and only
after a target failure in simulated history mode."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Literal

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


@dataclass
class _Turn:
    """Executor state for one turn k."""
    user: dict[str, Any] | None = None           # user entry sent to the target
    sim_user: dict[str, Any] | None = None       # user entry as the simulator sees it
    context: list[dict[str, Any]] | None = None  # history before the user entry
    span: list[dict[str, Any]] | None = None     # generated reply span (on success)
    error: str | None = None                     # target error (on failure)


def _inconclusive(items: list[script_mod.CriterionItem], reason: str) -> list[Judgment]:
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
    user_turn_mode: Literal["verbatim", "paraphrase"] = "verbatim",
    history_mode: Literal["simulated", "reference"] = "simulated",
) -> SessionOutcome:
    if user_turn_mode not in ("verbatim", "paraphrase"):
        raise ValueError(f"invalid user_turn_mode: {user_turn_mode!r}")
    if history_mode not in ("simulated", "reference"):
        raise ValueError(f"invalid history_mode: {history_mode!r}")
    script = script_mod.compile_script(session)
    if script.turn_count == 0:
        js = _inconclusive(script.unrouted, "no user turns in reference transcript")
        return _outcome(session, [], js, 0, 0)

    system_prompt = target_mod.build_system_prompt(bundle)
    tools_spec, _ = target_mod.build_tool_specs(bundle)
    declared = {t["function"]["name"] for t in tools_spec}
    # The simulator never sees client_context blocks (spec client-context Scope 7).
    ref_transcript = [script_mod.strip_client_context(m.model_dump())
                      for m in session.transcript]

    transcript: list[dict[str, Any]] = []       # result: user k + generated span k
    sim_transcript: list[dict[str, Any]] = []   # transcript without client_context, for <so_far>
    reference_history: list[dict[str, Any]] = []  # reference history mode only
    turns: dict[int, _Turn] = {}
    stopped: tuple[int, str] | None = None      # (turn j, cause) — simulated mode only
    judgments: list[Judgment] = []
    tokens_in = tokens_out = 0

    for step in script.steps:
        k = step.turn
        turn = turns.setdefault(k, _Turn())
        if isinstance(step, script_mod.UserStep):
            if stopped:
                continue
            text = script_mod.render_user_turn(step.ref_message)
            said = script_mod.user_text(step.ref_message)
            if user_turn_mode == "paraphrase":
                intent = said
                try:
                    t = await sim_mod.next_user_turn(
                        agent=simulator_agent, reference_transcript=ref_transcript,
                        simulated_so_far=sim_transcript, intent=intent)
                    tokens_in += t.tokens_in
                    tokens_out += t.tokens_out
                    said = t.content.strip() or intent
                except Exception:  # noqa: BLE001 — simulator can never fail a session
                    said = intent
                # The runner, never the simulator, attaches the rendered contexts.
                text = script_mod.compose_user_turn(step.ref_message, said)
            turn.user = {"role": "user", "content": text}
            turn.sim_user = {"role": "user", "content": said}

        elif isinstance(step, script_mod.AgentStep):
            if not stopped:
                assert turn.user is not None
                turn.context = list(
                    reference_history if history_mode == "reference" else transcript)
                try:
                    tt = await target_mod.target_turn(
                        model=target_model, system_prompt=system_prompt, tools_spec=tools_spec,
                        history=turn.context + [turn.user], tool_caller=tool_caller)
                    tokens_in += tt.tokens_in
                    tokens_out += tt.tokens_out
                    turn.span = tt.span
                    transcript.extend([turn.user, *tt.span])
                    sim_transcript.extend([turn.sim_user, *tt.span])
                    if tt.budget_exceeded and history_mode == "simulated":
                        stopped = (k, "exceeded tool-call budget")
                except Exception as e:  # noqa: BLE001 — becomes an inconclusive verdict
                    turn.error = str(e)
                    transcript.append(turn.user)
                    sim_transcript.append(turn.sim_user)
                    if history_mode == "simulated":
                        stopped = (k, f"target error: {e}")
            if history_mode == "reference":
                ref_user = {"role": "user",
                            "content": script_mod.render_user_turn(step.ref_user)}
                reference_history.extend(
                    [ref_user, *reference_entries(step.ref_span, declared_tools=declared)])

        else:  # JudgeStep
            if turn.error is not None:
                judgments += _inconclusive(step.criteria,
                                           f"turn {k} target error: {turn.error}")
                continue
            if turn.span is None:
                assert stopped is not None
                j, cause = stopped
                judgments += _inconclusive(step.criteria, f"turn {k} not reached: turn {j} {cause}")
                continue
            assert turn.user is not None and turn.context is not None
            try:
                jr = await judge_mod.judge_turn(
                    agent=judge_agent, context=turn.context,
                    turn_under_test=[turn.user, *turn.span],
                    criteria_items=step.criteria)
                tokens_in += jr.tokens_in
                tokens_out += jr.tokens_out
                judgments += [j.model_copy(update={"reason": _prefixed(c, j.reason)})
                              for c, j in zip(step.criteria, jr.judgments, strict=True)]
            except Exception as e:  # noqa: BLE001 — judge failure is per-turn
                tokens_in += getattr(e, "tokens_in", 0)
                tokens_out += getattr(e, "tokens_out", 0)
                judgments += _inconclusive(step.criteria, f"judge error: {e}")

    return _outcome(session, transcript, judgments, tokens_in, tokens_out)


def _outcome(
    session: InputSession, transcript: list[dict[str, Any]], judgments: list[Judgment],
    tokens_in: int, tokens_out: int,
) -> SessionOutcome:
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

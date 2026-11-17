# aikdm/eval/script.py
"""Compile a reference session into an explicit step script (Scenario-style):
UserStep(k) → AgentStep(k) → JudgeStep(k) per reference user turn. The step
list is fixed at compile time; the executor in runner.py may only skip steps."""

from __future__ import annotations

from dataclasses import dataclass, field

from aikdm.eval.history import text_blocks
from aikdm.eval.schemas import InputMessage, InputSession


def render_user_turn(ref_message: InputMessage) -> str:
    """Text the target sees for a reference user turn. String content as-is;
    content-block arrays → text blocks joined by a blank line; other blocks
    omitted. Single seam extended by the client-context spec."""
    return text_blocks(ref_message.content)


@dataclass(frozen=True)
class CriterionItem:
    message_id: str
    criterion: str
    unmapped: bool = False


@dataclass(frozen=True)
class UserStep:
    turn: int
    ref_message: InputMessage


@dataclass(frozen=True)
class AgentStep:
    turn: int
    ref_user: InputMessage
    ref_span: list[InputMessage]


@dataclass(frozen=True)
class JudgeStep:
    turn: int
    criteria: list[CriterionItem]


Step = UserStep | AgentStep | JudgeStep


@dataclass(frozen=True)
class Script:
    steps: list[Step]
    turn_count: int
    unrouted: list[CriterionItem] = field(default_factory=list)  # only when turn_count == 0


def compile_script(session: InputSession) -> Script:
    transcript = session.transcript
    user_idx = [i for i, m in enumerate(transcript) if m.role == "user"]
    position = {m.message_id: i for i, m in enumerate(transcript)}

    if not user_idx:
        unrouted = [CriterionItem(c.message_id, item)
                    for c in session.criteria for item in c.items]
        return Script(steps=[], turn_count=0, unrouted=unrouted)

    # turn of every assistant message (1-based); pre-first-user → turn 1
    turn_of: dict[str, int] = {}
    spans: dict[int, list[InputMessage]] = {}
    for k, start in enumerate(user_idx, start=1):
        end = user_idx[k] if k < len(user_idx) else len(transcript)
        spans[k] = transcript[start + 1:end]
        for m in spans[k]:
            if m.role == "assistant":
                turn_of[m.message_id] = k
    for m in transcript[:user_idx[0]]:
        if m.role == "assistant":
            turn_of[m.message_id] = 1

    last = len(user_idx)
    mapped: dict[int, list[tuple[int, int, CriterionItem]]] = {}
    unmapped: list[tuple[float, str, int, CriterionItem]] = []
    for c in session.criteria:
        k = turn_of.get(c.message_id)
        for n, item in enumerate(c.items):
            if k is None:
                unmapped.append((position.get(c.message_id, float("inf")), c.message_id, n,
                                 CriterionItem(c.message_id, item, unmapped=True)))
            else:
                mapped.setdefault(k, []).append(
                    (position[c.message_id], n, CriterionItem(c.message_id, item)))

    per_turn: dict[int, list[CriterionItem]] = {
        k: [ci for _, _, ci in sorted(items, key=lambda t: (t[0], t[1]))]
        for k, items in mapped.items()
    }
    if unmapped:
        unmapped.sort(key=lambda t: t[:3])
        per_turn.setdefault(last, []).extend(ci for *_, ci in unmapped)

    steps: list[Step] = []
    for k, start in enumerate(user_idx, start=1):
        steps.append(UserStep(turn=k, ref_message=transcript[start]))
        steps.append(AgentStep(turn=k, ref_user=transcript[start],
                               ref_span=list(spans[k])))
        if per_turn.get(k):
            steps.append(JudgeStep(turn=k, criteria=per_turn[k]))
    return Script(steps=steps, turn_count=last)

# aikdm/eval/script.py
"""Compile a reference session into an explicit step script (Scenario-style):
UserStep(k) → AgentStep(k) → JudgeStep(k) per reference user turn. The step
list is fixed at compile time; the executor in runner.py may only skip steps."""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from typing import Any

from aikdm.eval.history import text_blocks
from aikdm.eval.schemas import InputMessage, InputSession

log = logging.getLogger(__name__)

CLIENT_CONTEXT = "client_context"


def _is_client_context(block: Any) -> bool:
    return isinstance(block, dict) and block.get("type") == CLIENT_CONTEXT


def client_context_texts(content: Any) -> list[str]:
    """The `rendered` strings of a message's client_context blocks, in order.
    `rendered` (from open-bbcd's export) is replayed verbatim; aikdm never
    re-serializes `context`. Blocks without a string `rendered` are skipped
    (compile_script logs one warning per session)."""
    if not isinstance(content, list):
        return []
    return [b["rendered"] for b in content
            if _is_client_context(b) and isinstance(b.get("rendered"), str)]


def user_text(ref_message: InputMessage) -> str:
    """What the user typed: text blocks only, no client_context."""
    return text_blocks(ref_message.content)


def compose_user_turn(ref_message: InputMessage, text: str) -> str:
    """The reference turn's rendered client contexts first, then text,
    joined by a blank line (open-bbcd sends them as separate text parts)."""
    return "\n\n".join(p for p in [*client_context_texts(ref_message.content), text] if p)


def render_user_turn(ref_message: InputMessage) -> str:
    """Text the target sees for a reference user turn: every client_context
    `rendered` string first, verbatim, then the text blocks."""
    return compose_user_turn(ref_message, user_text(ref_message))


def strip_client_context(message: dict[str, Any]) -> dict[str, Any]:
    """Copy of a dumped InputMessage without client_context blocks, for the
    simulator, which must never see or paraphrase them."""
    content = message.get("content")
    if not isinstance(content, list):
        return message
    return {**message, "content": [b for b in content if not _is_client_context(b)]}


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
    missing = sum(
        1 for m in transcript if m.role == "user" and isinstance(m.content, list)
        for b in m.content if _is_client_context(b) and not isinstance(b.get("rendered"), str)
    )
    if missing:
        log.warning("session %s: %d client_context block(s) without 'rendered' skipped "
                    "(export predates client-context)", session.session_id, missing)
    user_idx = [i for i, m in enumerate(transcript) if m.role == "user"]
    position = {m.message_id: i for i, m in enumerate(transcript)}

    if not user_idx:
        unrouted = [CriterionItem(c.message_id, item) for c in session.criteria for item in c.items]
        return Script(steps=[], turn_count=0, unrouted=unrouted)

    # turn of every assistant message (1-based); pre-first-user → turn 1
    turn_of: dict[str, int] = {}
    spans: dict[int, list[InputMessage]] = {}
    for k, start in enumerate(user_idx, start=1):
        end = user_idx[k] if k < len(user_idx) else len(transcript)
        spans[k] = transcript[start + 1 : end]
        for m in spans[k]:
            if m.role == "assistant":
                turn_of[m.message_id] = k
    for m in transcript[: user_idx[0]]:
        if m.role == "assistant":
            turn_of[m.message_id] = 1

    last = len(user_idx)
    mapped: dict[int, list[tuple[int, int, CriterionItem]]] = {}
    unmapped: list[tuple[float, str, int, CriterionItem]] = []
    for c in session.criteria:
        k = turn_of.get(c.message_id)
        for n, item in enumerate(c.items):
            if k is None:
                unmapped.append(
                    (
                        position.get(c.message_id, float("inf")),
                        c.message_id,
                        n,
                        CriterionItem(c.message_id, item, unmapped=True),
                    )
                )
            else:
                mapped.setdefault(k, []).append(
                    (position[c.message_id], n, CriterionItem(c.message_id, item))
                )

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
        steps.append(AgentStep(turn=k, ref_user=transcript[start], ref_span=list(spans[k])))
        if per_turn.get(k):
            steps.append(JudgeStep(turn=k, criteria=per_turn[k]))
    return Script(steps=steps, turn_count=last)

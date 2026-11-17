# aikdm/eval/history.py
"""One history renderer for both eval history modes.

to_target_messages: aikdm transcript entries → LiteLLM/OpenAI chat messages.
reference_entries: a reference turn span (InputMessages) → aikdm entries."""

from __future__ import annotations

import json
from typing import Any

from aikdm.eval.schemas import InputMessage


def to_target_messages(entries: list[dict[str, Any]]) -> list[dict[str, Any]]:
    out: list[dict[str, Any]] = []
    turn = 0
    n = 0
    for e in entries:
        role = e.get("role")
        if role == "user":
            turn += 1
            n = 0
            out.append({"role": "user", "content": e.get("content") or ""})
            continue
        if role != "assistant":
            continue
        calls = e.get("tool_calls") or []
        if not calls:
            out.append({"role": "assistant", "content": e.get("content") or ""})
            continue
        ids = []
        tool_calls = []
        for c in calls:
            cid = f"call_{turn}_{n}"
            n += 1
            ids.append(cid)
            tool_calls.append(
                {
                    "id": cid,
                    "type": "function",
                    "function": {"name": c["name"], "arguments": json.dumps(c.get("args") or {})},
                }
            )
        msg: dict[str, Any] = {"role": "assistant", "tool_calls": tool_calls}
        if e.get("content"):
            msg["content"] = e["content"]
        out.append(msg)
        for cid, c in zip(ids, calls, strict=True):
            out.append(
                {"role": "tool", "tool_call_id": cid, "content": json.dumps(c.get("result"))}
            )
    return out


def text_blocks(content: Any) -> str:
    """Flatten message content: string as-is; content-block arrays → text
    blocks joined by a blank line (other blocks omitted); anything else → ""."""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        texts = [
            b.get("text", "") for b in content if isinstance(b, dict) and b.get("type") == "text"
        ]
        return "\n\n".join(t for t in texts if t)
    return ""


def reference_entries(
    span: list[InputMessage],
    *,
    declared_tools: set[str],
) -> list[dict[str, Any]]:
    """Reference assistant messages → aikdm entries. role:tool messages are
    skipped (their results arrive via tool_calls[].result). Calls to tools the
    bundle under test doesn't declare are rendered as plain text."""
    out: list[dict[str, Any]] = []
    for m in span:
        if m.role != "assistant":
            continue
        text = text_blocks(m.content)
        structured = []
        as_text = []
        for c in m.tool_calls:
            if c.name in declared_tools:
                structured.append({"name": c.name, "args": c.args, "result": c.result})
            else:
                as_text.append(
                    f"[called tool {c.name} with {json.dumps(c.args)} → {json.dumps(c.result)}]"
                )
        content = "\n\n".join(p for p in [text, *as_text] if p)
        entry: dict[str, Any] = {"role": "assistant", "content": content}
        if structured:
            entry["tool_calls"] = structured
        out.append(entry)
    return out

"""Target agent invocation. Sends the accumulated simulated conversation
to the tested agent (bundle's main_prompt + concatenated skill prompts as
system, bundle's tools as function definitions). Uses LiteLLM directly for
the tool loop, since we need to intercept every tool_use call and dispatch
it through the mock (rather than actually hitting an HTTP backend or MCP
server)."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

import litellm  # type: ignore[import-untyped]

from aikdm.eval.tool_mock import ToolMock


@dataclass
class TargetTurn:
    assistant_message: dict[str, Any]      # {role, content, tool_calls}
    tokens_in: int
    tokens_out: int
    tool_call_count: int


def build_system_prompt(bundle: dict[str, Any]) -> str:
    main = str(bundle.get("main_prompt", ""))
    skills = bundle.get("skills", []) or []
    parts = [main]
    for s in skills:
        parts.append(f"\n\n<skill name=\"{s.get('name','')}\">\n{s.get('prompt','')}\n</skill>")
    return "".join(parts)


def build_tool_specs(bundle: dict[str, Any]) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    """Returns (LiteLLM-shaped tools list, name→schema for the mock)."""
    tools: list[dict[str, Any]] = []
    schema_by_name: dict[str, Any] = {}
    for t in bundle.get("tools", []) or []:
        name = t.get("name") or t.get("id") or ""
        if not name:
            continue
        params = _merge_param_schemas(t)
        tools.append({
            "type": "function",
            "function": {
                "name": name,
                "description": t.get("description", ""),
                "parameters": params,
            },
        })
        schema_by_name[name] = {
            "body_shape": t.get("body_shape"),
            "response_shape": t.get("response_shape"),
        }
    return tools, schema_by_name


def _merge_param_schemas(t: dict[str, Any]) -> dict[str, Any]:
    body = t.get("body_shape") if isinstance(t.get("body_shape"), dict) else None
    if body and body.get("type") == "object":
        return body
    return {"type": "object", "properties": {}, "additionalProperties": True}


async def target_turn(
    *, model: str, system_prompt: str, tools_spec: list[dict[str, Any]],
    conversation: list[dict[str, Any]], tool_mock: ToolMock,
) -> TargetTurn:
    """One assistant turn against the tested agent. Loops through tool calls
    (up to a hard cap of 8) until the model returns a plain text answer."""
    tool_call_count = 0
    tokens_in = tokens_out = 0
    msgs = [{"role": "system", "content": system_prompt}] + conversation
    for _ in range(8):
        resp = await litellm.acompletion(
            model=model, messages=msgs, tools=tools_spec or None,
        )
        choice = resp.choices[0].message
        usage = getattr(resp, "usage", None)
        if usage:
            tokens_in += getattr(usage, "prompt_tokens", 0) or 0
            tokens_out += getattr(usage, "completion_tokens", 0) or 0
        tool_calls = getattr(choice, "tool_calls", None) or []
        if not tool_calls:
            return TargetTurn(
                assistant_message={"role": "assistant", "content": choice.content or ""},
                tokens_in=tokens_in, tokens_out=tokens_out,
                tool_call_count=tool_call_count,
            )
        assistant_entry: dict[str, Any] = {
            "role": "assistant",
            "content": choice.content or "",
            "tool_calls": [],
        }
        for tc in tool_calls:
            name = tc.function.name
            import json as _json
            args = _json.loads(tc.function.arguments or "{}")
            mock_out = tool_mock.call(name, args)
            assistant_entry["tool_calls"].append({
                "name": name, "args": args,
                "result": mock_out["result"], "source": mock_out["source"],
            })
            tool_call_count += 1
            msgs.append({
                "role": "assistant", "tool_calls": [{
                    "id": tc.id, "type": "function",
                    "function": {"name": name, "arguments": tc.function.arguments},
                }],
            })
            msgs.append({
                "role": "tool", "tool_call_id": tc.id,
                "content": _json.dumps(mock_out["result"]),
            })
        conversation.append(assistant_entry)
    return TargetTurn(
        assistant_message={
            "role": "assistant",
            "content": "(target agent exceeded tool-call budget)",
        },
        tokens_in=tokens_in, tokens_out=tokens_out,
        tool_call_count=tool_call_count,
    )

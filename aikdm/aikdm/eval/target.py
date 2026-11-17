"""Target agent invocation. Sends the eval history
to the tested agent (bundle's main_prompt + concatenated skill prompts as
system, bundle's tools as function definitions). Uses LiteLLM directly for
the tool loop, since we need to intercept every tool_use call and dispatch
it through the caller — either ToolMock (replay + synthesise) or
RealHTTPToolCaller (real HTTP against wired backends). The caller is
duck-typed: only `.call(name, args) -> {"source","result",...}` is used."""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any

import litellm  # type: ignore[import-untyped]

from aikdm.eval.history import to_target_messages


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


BUDGET_SENTINEL = "(target agent exceeded tool-call budget)"
MAX_TOOL_ROUNDS = 8


@dataclass
class TargetTurn:
    span: list[dict[str, Any]]   # aikdm entries: tool rounds..., final assistant text
    tokens_in: int
    tokens_out: int
    tool_call_count: int
    budget_exceeded: bool = False


async def target_turn(
    *, model: str, system_prompt: str, tools_spec: list[dict[str, Any]],
    history: list[dict[str, Any]], tool_caller,
) -> TargetTurn:
    """One assistant turn against the tested agent. `history` is aikdm entries
    ending with the current user message; it is never mutated. Loops through
    tool calls (up to MAX_TOOL_ROUNDS) until a plain text answer."""
    msgs = [{"role": "system", "content": system_prompt}] + to_target_messages(history)
    span: list[dict[str, Any]] = []
    tool_call_count = 0
    tokens_in = tokens_out = 0
    for _ in range(MAX_TOOL_ROUNDS):
        resp = await litellm.acompletion(model=model, messages=msgs, tools=tools_spec or None)
        choice = resp.choices[0].message
        usage = getattr(resp, "usage", None)
        if usage:
            tokens_in += getattr(usage, "prompt_tokens", 0) or 0
            tokens_out += getattr(usage, "completion_tokens", 0) or 0
        tool_calls = getattr(choice, "tool_calls", None) or []
        if not tool_calls:
            span.append({"role": "assistant", "content": choice.content or ""})
            return TargetTurn(span=span, tokens_in=tokens_in, tokens_out=tokens_out,
                              tool_call_count=tool_call_count)
        entry: dict[str, Any] = {"role": "assistant", "content": choice.content or "",
                                 "tool_calls": []}
        wire_calls = []
        results = []
        for tc in tool_calls:
            args = json.loads(tc.function.arguments or "{}")
            out = tool_caller.call(tc.function.name, args)
            entry["tool_calls"].append({"name": tc.function.name, "args": args,
                                        "result": out["result"], "source": out["source"]})
            wire_calls.append({"id": tc.id, "type": "function",
                               "function": {"name": tc.function.name,
                                            "arguments": tc.function.arguments}})
            results.append({"role": "tool", "tool_call_id": tc.id,
                            "content": json.dumps(out["result"])})
            tool_call_count += 1
        wire: dict[str, Any] = {"role": "assistant", "tool_calls": wire_calls}
        if choice.content:
            wire["content"] = choice.content
        msgs.append(wire)
        msgs.extend(results)
        span.append(entry)
    span.append({"role": "assistant", "content": BUDGET_SENTINEL})
    return TargetTurn(span=span, tokens_in=tokens_in, tokens_out=tokens_out,
                      tool_call_count=tool_call_count, budget_exceeded=True)

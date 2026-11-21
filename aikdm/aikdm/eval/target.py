"""Target agent invocation. Sends the eval history to the tested agent
(bundle's main_prompt as system, the built-in Skill tool plus the bundle's
tools and exported MCP tools as function definitions — the same shape the
open-bbcd runtime offers). Uses LiteLLM directly for the tool loop, since we
need to intercept every tool_use call and dispatch it through the caller —
SkillToolCaller answers Skill locally and hands everything else to ToolMock
(replay + synthesise) or RealHTTPToolCaller (real HTTP against wired
backends), behind a ToolRouter that sends exported MCP tools to
MCPToolCaller. Callers are duck-typed: only
`.call(name, args) -> {"source","result",...}` is used, and its return value
may be awaitable."""

from __future__ import annotations

import inspect
import json
from dataclasses import dataclass
from typing import Any

import litellm  # type: ignore[import-untyped]

from aikdm.eval.history import to_target_messages

# Mirrors open-bbcd's runtime Skill tool (internal/llm/tools/composite.go);
# tests/fixtures/skill_tool_parity.json pins the two together.
SKILL_TOOL_NAME = "Skill"
SKILL_TOOL_DESCRIPTION = (
    "Load the prompt for a named skill into your working context. "
    "Use when the user's intent matches a skill in skills_index."
)
# Unicode White_Space, the set Go's strings.TrimSpace strips.
_WHITESPACE = (
    "\t\n\v\f\r \x85\xa0\u1680\u2000\u2001\u2002\u2003\u2004\u2005"
    "\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000"
)


def build_system_prompt(bundle: dict[str, Any]) -> str:
    """The system text the runtime sends: main_prompt only. Skills are
    loaded on demand through the Skill tool, never inlined."""
    return str(bundle.get("main_prompt", ""))


def loadable_skills(bundle: dict[str, Any]) -> list[dict[str, Any]]:
    """Skills the Skill tool can serve, in bundle order: those whose prompt
    is non-empty after trimming whitespace."""
    return [s for s in bundle.get("skills") or []
            if str(s.get("prompt") or "").strip(_WHITESPACE)]


def build_skill_tool_spec(bundle: dict[str, Any]) -> dict[str, Any] | None:
    """LiteLLM-shaped Skill tool, or None when no skill is loadable."""
    names = [str(s.get("name", "")) for s in loadable_skills(bundle)]
    if not names:
        return None
    return {
        "type": "function",
        "function": {
            "name": SKILL_TOOL_NAME,
            "description": SKILL_TOOL_DESCRIPTION,
            "parameters": {
                "type": "object",
                "properties": {"name": {"type": "string", "enum": names}},
                "required": ["name"],
            },
        },
    }


class SkillToolCaller:
    """Answers Skill calls from bundle.skills, like the runtime does, and
    delegates every other tool to `inner`. Skill never reaches ToolMock or a
    real backend, and is answered even when it was not offered."""

    def __init__(self, bundle: dict[str, Any], inner):
        self._prompts: dict[str, str] = {}
        for s in loadable_skills(bundle):
            self._prompts.setdefault(str(s.get("name", "")), str(s["prompt"]))
        self._inner = inner

    def call(self, name: str, args: Any) -> dict[str, Any]:
        if name != SKILL_TOOL_NAME:
            return self._inner.call(name, args)
        skill = args.get("name", "") if isinstance(args, dict) else ""
        skill = skill if isinstance(skill, str) else json.dumps(skill)
        if skill in self._prompts:
            return {"source": "skill", "result": {"prompt": self._prompts[skill]}}
        return {"source": "error", "result": {"error": f"unknown skill: {skill}"}}


def build_tool_specs(
    bundle: dict[str, Any], mcp_backends: dict[str, Any] | None = None,
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    """Returns (LiteLLM-shaped tools list, name→schema for the mock).

    The Skill tool comes first when offered; it is answered by
    SkillToolCaller, so it has no mock schema.

    Exported MCP tools (eval-input-v1 `mcp_backends[*].tools`) follow the
    bundle's tools with name, description and input_schema exactly as
    exported; their output_schema is the mock's response_shape."""
    tools: list[dict[str, Any]] = []
    schema_by_name: dict[str, Any] = {}
    skill = build_skill_tool_spec(bundle)
    if skill is not None:
        tools.append(skill)
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
    for backend in (mcp_backends or {}).values():
        b = backend.model_dump() if hasattr(backend, "model_dump") else backend
        for t in b.get("tools") or []:
            tools.append({
                "type": "function",
                "function": {
                    "name": t["name"],
                    "description": t.get("description", ""),
                    "parameters": t.get("input_schema"),
                },
            })
            schema_by_name[t["name"]] = {
                "body_shape": None,
                "response_shape": t.get("output_schema"),
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
            try:
                args = json.loads(tc.function.arguments or "{}")
            except json.JSONDecodeError as e:
                # Not dispatched: the model sees the parse error as the tool result.
                args = {"_raw": tc.function.arguments}
                out = {"source": "error", "result": {"error": f"invalid JSON arguments: {e}"}}
            else:
                out = tool_caller.call(tc.function.name, args)
                if inspect.isawaitable(out):
                    out = await out
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

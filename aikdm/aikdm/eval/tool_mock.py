"""Hybrid A+B tool mock. A: replay by (name, args) match from the original
transcript. B: synthesize a plausible success payload from the tool's
body_shape / response_shape when no replay is available. C: unknown tools
return a structured error the judge can see."""

from __future__ import annotations

from typing import Any


class ToolMock:
    def __init__(self, transcript: list[dict[str, Any]], tools_schema: dict[str, Any]):
        # tools_schema keyed by tool name → {body_shape, response_shape}.
        self.tools_schema = tools_schema
        self._replay_index: list[tuple[str, dict[str, Any], Any]] = []
        for msg in transcript:
            for call in (msg.get("tool_calls") or []):
                self._replay_index.append((
                    call.get("name", ""),
                    call.get("args") or {},
                    call.get("result"),
                ))

    def call(self, name: str, args: dict[str, Any]) -> dict[str, Any]:
        for cname, cargs, result in self._replay_index:
            if cname == name and _args_match(cargs, args):
                return {"source": "replayed", "result": result}
        if name not in self.tools_schema:
            return {"source": "error", "result": {"error": f"unknown tool {name!r}"}}
        schema = self.tools_schema.get(name, {})
        return {"source": "mocked", "result": _synthesize_from_schema(schema.get("response_shape"))}


def _args_match(a: dict[str, Any], b: dict[str, Any]) -> bool:
    return a == b


_STUB_SUCCESS = {
    "status": "ok",
    "note": "aikdm eval mock response (no schema declared for this tool)",
}


def _synthesize_from_schema(shape: Any) -> Any:
    """Return a plausible payload for a JSON-Schema shape. Falls back to
    a generic success stub when shape is None / unknown / non-object, so
    the target agent sees a positive signal it can reason over instead of
    an empty object. Not a full JSON Schema faker — just enough to keep
    the tested agent moving forward."""
    if shape is None or shape == "unknown":
        return dict(_STUB_SUCCESS)
    if not isinstance(shape, dict):
        return dict(_STUB_SUCCESS)
    t = shape.get("type")
    if t == "object":
        props = shape.get("properties") or {}
        if not props:
            return dict(_STUB_SUCCESS)
        return {k: _synthesize_from_schema(v) for k, v in props.items()}
    if t == "array":
        item = shape.get("items")
        return [_synthesize_from_schema(item)] if item is not None else [dict(_STUB_SUCCESS)]
    if t == "string":
        return "example"
    if t == "integer":
        return 1
    if t == "number":
        return 1.0
    if t == "boolean":
        return True
    return dict(_STUB_SUCCESS)

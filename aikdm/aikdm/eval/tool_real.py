"""Real HTTP tool caller. Mirrors ToolMock's call() interface but hits
real MCP backends when the eval was configured with mock_mcp_tools=false.

Args flow:
- path_params (from bundle.tools[name].path_params) → substituted into path
- query_params (from bundle.tools[name].query_params) → appended as URL query
- Everything else → JSON body (for POST/PUT/PATCH)

Headers = backend default_headers ∪ header_overrides. Overrides win on
conflict, per the design (per-eval config beats backend defaults).
"""

from __future__ import annotations

import json
from typing import Any

import httpx


class RealHTTPToolCaller:
    def __init__(
        self,
        *,
        bundle: dict[str, Any],
        tool_backends: dict[str, dict[str, Any]],
        header_overrides: dict[str, str],
        transport: httpx.BaseTransport | None = None,
        timeout_s: float = 30.0,
    ):
        self._tools_by_name: dict[str, dict[str, Any]] = {}
        for t in bundle.get("tools", []) or []:
            name = t.get("name") or t.get("id") or ""
            if name:
                self._tools_by_name[name] = t
        self._tool_backends = tool_backends or {}
        self._header_overrides = header_overrides or {}
        self._client = httpx.Client(transport=transport, timeout=timeout_s)

    def call(self, name: str, args: dict[str, Any]) -> dict[str, Any]:
        tool = self._tools_by_name.get(name)
        if tool is None:
            return {"source": "error", "result": {"error": f"unknown tool {name!r}"}}

        backend = self._tool_backends.get(name)
        if backend is None:
            return {"source": "error", "result": {"error": f"no backend wired for {name!r}"}}

        method = str(tool.get("method", "GET")).upper()
        path = str(tool.get("path", ""))
        path_param_names = [p.get("name", "") for p in (tool.get("path_params") or []) if p.get("name")]
        query_param_names = [p.get("name", "") for p in (tool.get("query_params") or []) if p.get("name")]

        args_copy = dict(args)
        for pname in path_param_names:
            if pname in args_copy:
                path = path.replace("{" + pname + "}", str(args_copy.pop(pname)))

        query: dict[str, Any] = {}
        for qname in query_param_names:
            if qname in args_copy:
                query[qname] = args_copy.pop(qname)

        body = args_copy if method in ("POST", "PUT", "PATCH") else None

        headers: dict[str, str] = {}
        headers.update(backend.get("default_headers") or {})
        headers.update(self._header_overrides)
        if body is not None and "content-type" not in {k.lower() for k in headers}:
            headers["Content-Type"] = "application/json"

        url = str(backend.get("base_url", "")).rstrip("/") + path

        try:
            if body is not None:
                resp = self._client.request(
                    method, url, params=query, headers=headers, content=json.dumps(body)
                )
            else:
                resp = self._client.request(method, url, params=query, headers=headers)
        except httpx.HTTPError as e:
            return {"source": "error", "result": {"error": f"http error: {e}"}}

        try:
            payload: Any = resp.json()
        except ValueError:
            payload = resp.text

        if resp.status_code >= 400:
            return {"source": "error", "result": {"status_code": resp.status_code, "body": payload}}

        return {"source": "real", "result": payload, "status_code": resp.status_code}

    def close(self) -> None:
        self._client.close()

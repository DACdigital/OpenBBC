"""Test-only fake MCP server (streamable HTTP at /mcp) with auth + a control API.

Auth is checked on every /mcp HTTP request. The fake only proves that the
configured headers arrive; it does not model any signing scheme.
"""

import contextvars
import json

from mcp.server.mcpserver import MCPServer
from mcp.server.mcpserver.exceptions import ToolError
from starlette.requests import Request
from starlette.responses import JSONResponse, Response

STATIC_KEY_HEADER = "Bearer e2e-static-key"
IDENTITY_HEADER = "x-test-identity"

_identity: contextvars.ContextVar[str | None] = contextvars.ContextVar("identity", default=None)
_calls: list[dict] = []
_state = {"down": False}

mcp = MCPServer("fake-kb")


def _require_identity() -> str:
    identity = _identity.get()
    if not identity:
        raise ToolError("missing identity")
    return identity


@mcp.tool()
def search(query: str) -> dict[str, str]:
    """Search the knowledge base."""
    return {"summary": f"results for {query} as {_require_identity()}"}


@mcp.tool()
def get_project(id: str) -> dict[str, str]:
    """Fetch a project by id."""
    return {"id": id, "identity": _require_identity()}


def _log_entries(headers: dict[str, str], http_method: str, body: bytes) -> list[dict]:
    try:
        payload = json.loads(body) if body else None
    except ValueError:
        payload = None
    messages = payload if isinstance(payload, list) else [payload]
    entries = []
    for msg in messages:
        if isinstance(msg, dict) and "method" in msg:
            params = msg.get("params") if isinstance(msg.get("params"), dict) else {}
            is_call = msg["method"] == "tools/call"
            entries.append({
                "method": msg["method"],
                "tool": params.get("name") if is_call else None,
                "args": params.get("arguments") if is_call else None,
                "headers": headers,
            })
    if not entries:  # GET/DELETE, non-JSON or JSON-RPC responses
        entries.append({"method": http_method, "tool": None, "args": None, "headers": headers})
    return entries


class _GateMiddleware:
    """Pure ASGI: log, then down check, then Bearer check, for /mcp only."""

    def __init__(self, app):
        self.app = app

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http" or not scope["path"].startswith("/mcp"):
            return await self.app(scope, receive, send)

        headers = {k.decode("latin-1").lower(): v.decode("latin-1") for k, v in scope["headers"]}
        chunks, more = [], True
        while more:
            msg = await receive()
            chunks.append(msg.get("body", b""))
            more = msg.get("more_body", False)
        body = b"".join(chunks)
        _calls.extend(_log_entries(headers, scope["method"], body))

        denied = None
        if _state["down"]:
            denied = Response("fake-mcp is down", status_code=503)
        elif headers.get("authorization") != STATIC_KEY_HEADER:
            denied = Response("unauthorized", status_code=401,
                              headers={"WWW-Authenticate": "Bearer"})
        if denied:
            return await denied(scope, receive, send)

        replayed = False

        async def replay():
            nonlocal replayed
            if not replayed:
                replayed = True
                return {"type": "http.request", "body": body, "more_body": False}
            return await receive()

        token = _identity.set(headers.get(IDENTITY_HEADER))
        try:
            await self.app(scope, replay, send)
        finally:
            _identity.reset(token)


async def _get_calls(request: Request) -> Response:
    return JSONResponse(_calls)


async def _reset(request: Request) -> Response:
    _calls.clear()
    _state["down"] = False
    return Response(status_code=204)


async def _mode(request: Request) -> Response:
    _state["down"] = bool((await request.json()).get("down"))
    return Response(status_code=204)


app = mcp.streamable_http_app()
app.add_route("/_calls", _get_calls, methods=["GET"])
app.add_route("/_reset", _reset, methods=["POST"])
app.add_route("/_mode", _mode, methods=["POST"])
app.add_middleware(_GateMiddleware)

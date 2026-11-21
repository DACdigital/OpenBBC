# tests/unit/test_tool_mcp.py
"""MCPToolCaller against a real in-process streamable-HTTP MCP server."""

import socket
import threading
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest
import uvicorn
from mcp.server.mcpserver import Context, MCPServer
from mcp.server.mcpserver.exceptions import ToolError

from aikdm.eval.tool_mcp import MCPToolCaller, ToolRouter


def _free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def _serve(srv):
    port = _free_port()
    server = uvicorn.Server(uvicorn.Config(
        srv.streamable_http_app(), host="127.0.0.1", port=port, log_level="warning"))
    t = threading.Thread(target=server.run, daemon=True)
    t.start()
    deadline = time.time() + 10
    while not server.started:
        assert time.time() < deadline, "MCP test server did not start"
        time.sleep(0.02)
    return f"http://127.0.0.1:{port}/mcp", server, t


@pytest.fixture(scope="module")
def kb_server():
    seen: list[dict[str, str]] = []
    srv = MCPServer("kb")

    @srv.tool()
    def search(q: str, ctx: Context) -> str:
        req = ctx.request_context.request
        seen.append(dict(req.headers) if req else {})
        return f"hits for {q}"

    @srv.tool()
    def explode(q: str) -> str:
        raise ToolError("index unavailable")

    url, server, t = _serve(srv)
    yield url, seen
    server.should_exit = True
    t.join(timeout=5)


@pytest.fixture
def counting_500_server():
    """Answers every request with HTTP 500 and counts them."""
    hits = {"n": 0}

    class H(BaseHTTPRequestHandler):
        def log_message(self, *a):
            pass

        def _fail(self):
            hits["n"] += 1
            self.send_response(500)
            self.send_header("Content-Length", "0")
            self.end_headers()

        do_GET = do_POST = do_DELETE = _fail

    httpd = HTTPServer(("127.0.0.1", 0), H)
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    yield f"http://127.0.0.1:{httpd.server_address[1]}/mcp", hits
    httpd.shutdown()


def _backends(url, headers=None):
    return {"kb": {"url": url, "transport": "streamable_http",
                   "headers": headers or {}, "tools": []}}


async def test_live_call_strips_prefix_sends_headers_and_flattens(kb_server):
    url, seen = kb_server
    seen.clear()
    caller = MCPToolCaller(_backends(url, {"X-Identity": "tester"}))
    try:
        out = await caller.call("kb__search", {"q": "pricing"})
        out2 = await caller.call("kb__search", {"q": "again"})
    finally:
        await caller.aclose()
    assert out["source"] == "real"
    assert out["result"]["text"] == "hits for pricing"
    assert out["result"]["content"] == [{"type": "text", "text": "hits for pricing"}]
    assert out2["result"]["text"] == "hits for again"
    assert [h.get("x-identity") for h in seen] == ["tester", "tester"]


async def test_server_is_error_is_marked_error(kb_server):
    url, _ = kb_server
    caller = MCPToolCaller(_backends(url))
    try:
        out = await caller.call("kb__explode", {"q": "x"})
    finally:
        await caller.aclose()
    assert out["source"] == "error"
    assert "index unavailable" in out["result"]["text"]
    assert out["result"]["content"][0]["type"] == "text"


async def test_connect_failure_is_sticky_for_the_session(counting_500_server):
    url, hits = counting_500_server
    caller = MCPToolCaller(_backends(url))
    try:
        first = await caller.call("kb__search", {"q": "x"})
        n = hits["n"]
        second = await caller.call("kb__search", {"q": "y"})
    finally:
        await caller.aclose()
    assert first["source"] == "error" and first["result"]["error"].startswith("mcp kb: connect: ")
    assert second == first
    assert n > 0 and hits["n"] == n  # no reconnect attempt


async def test_unreachable_backend_errors_without_raising():
    caller = MCPToolCaller(_backends("http://127.0.0.1:1/mcp"))
    try:
        out = await caller.call("kb__search", {"q": "x"})
    finally:
        await caller.aclose()
    assert out["source"] == "error"
    assert out["result"]["error"].startswith("mcp kb: connect: ")


async def test_router_sends_only_mcp_prefixed_names_to_mcp():
    class Fallback:
        def __init__(self):
            self.calls = []

        def call(self, name, args):
            self.calls.append(name)
            return {"source": "real", "result": {"via": "http"}}

    class FakeMCP:
        def __init__(self):
            self.calls = []

        def owns(self, name):
            return MCPToolCaller({"kb": {"tools": []}}).owns(name)

        async def call(self, name, args):
            self.calls.append(name)
            return {"source": "real", "result": {"via": "mcp"}}

    fb, mcp = Fallback(), FakeMCP()
    router = ToolRouter(mcp, fb)
    assert (await router.call("kb__search", {}))["result"] == {"via": "mcp"}
    assert (await router.call("products.list", {}))["result"] == {"via": "http"}
    assert (await router.call("other__search", {}))["result"] == {"via": "http"}
    assert mcp.calls == ["kb__search"] and fb.calls == ["products.list", "other__search"]


async def test_backend_dying_mid_session_returns_error_not_cancellation():
    srv = MCPServer("kb")

    @srv.tool()
    def search(q: str) -> str:
        return "ok"

    url, server, t = _serve(srv)
    caller = MCPToolCaller(_backends(url), call_timeout_s=5)
    try:
        assert (await caller.call("kb__search", {"q": "x"}))["source"] == "real"
        server.should_exit = True
        t.join(timeout=5)
        out = await caller.call("kb__search", {"q": "y"})
    finally:
        await caller.aclose()
    assert out["source"] == "error"
    assert out["result"]["error"].startswith("mcp kb: call search: ")

"""fake-mcp against a real uvicorn server, driven by the mcp SDK client."""

import asyncio
import socket
import threading
import time

import httpx
import pytest
import uvicorn
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client

from fakes.mcp_app import app

BEARER = {"Authorization": "Bearer e2e-static-key"}


def _free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


@pytest.fixture(scope="module")
def base_url():
    port = _free_port()
    server = uvicorn.Server(uvicorn.Config(app, host="127.0.0.1", port=port, log_level="warning"))
    t = threading.Thread(target=server.run, daemon=True)
    t.start()
    deadline = time.time() + 10
    while not server.started:
        assert time.time() < deadline, "fake-mcp did not start"
        time.sleep(0.02)
    yield f"http://127.0.0.1:{port}"
    server.should_exit = True
    t.join(timeout=5)


@pytest.fixture(autouse=True)
def _reset(base_url):
    httpx.post(f"{base_url}/_reset").raise_for_status()
    yield
    httpx.post(f"{base_url}/_reset").raise_for_status()


def _call(base_url, name, args, headers):
    async def go():
        async with httpx.AsyncClient(headers=headers) as http:
            async with streamable_http_client(f"{base_url}/mcp", http_client=http) as (r, w):
                async with ClientSession(r, w) as session:
                    await session.initialize()
                    return await session.call_tool(name, args)

    return asyncio.run(go())


def _text(result) -> str:
    return "".join(c.text for c in result.content if c.type == "text")


def test_list_tools_without_bearer_is_401(base_url):
    resp = httpx.post(
        f"{base_url}/mcp",
        json={"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
        headers={"Accept": "application/json, text/event-stream"},
    )
    assert resp.status_code == 401
    calls = httpx.get(f"{base_url}/_calls").json()
    assert [c["method"] for c in calls] == ["tools/list"]
    assert "authorization" not in calls[0]["headers"]


def test_search_returns_structured_and_text(base_url):
    result = _call(base_url, "search", {"query": "alpha"}, {**BEARER, "X-Test-Identity": "u1"})
    assert not result.is_error
    assert result.structured_content["summary"] == "results for alpha as u1"
    assert "results for alpha as u1" in _text(result)


def test_missing_identity_is_tool_error(base_url):
    result = _call(base_url, "search", {"query": "alpha"}, BEARER)
    assert result.is_error
    assert "missing identity" in _text(result)


def test_calls_log_and_reset(base_url):
    _call(base_url, "search", {"query": "alpha"}, {**BEARER, "X-Test-Identity": "u1"})
    calls = httpx.get(f"{base_url}/_calls").json()
    entry = next(c for c in calls if c["method"] == "tools/call")
    assert entry["tool"] == "search"
    assert entry["args"] == {"query": "alpha"}
    assert entry["headers"]["x-test-identity"] == "u1"
    assert entry["headers"]["authorization"] == "Bearer e2e-static-key"
    assert {"initialize", "tools/call"} <= {c["method"] for c in calls}

    assert httpx.post(f"{base_url}/_reset").status_code == 204
    assert httpx.get(f"{base_url}/_calls").json() == []


def test_down_mode_503(base_url):
    assert httpx.post(f"{base_url}/_mode", json={"down": True}).status_code == 204
    try:
        resp = httpx.post(
            f"{base_url}/mcp",
            json={"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
            headers={**BEARER, "Accept": "application/json, text/event-stream"},
        )
        assert resp.status_code == 503
    finally:
        # R1: /_reset also clears down mode
        httpx.post(f"{base_url}/_reset")
    resp = httpx.post(
        f"{base_url}/mcp",
        json={"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
        headers={**BEARER, "Accept": "application/json, text/event-stream"},
    )
    assert resp.status_code != 503


def test_accepts_compose_network_host_header(base_url):
    # open-bbcd and the runner reach the fake as http://fake-mcp:8000/mcp, so the
    # SDK's localhost-only DNS-rebinding guard (421 Misdirected Request) must be off.
    r = httpx.post(
        f"{base_url}/mcp",
        headers={**BEARER, "Host": "fake-mcp:8000", "Accept": "application/json, text/event-stream"},
        json={"jsonrpc": "2.0", "id": 1, "method": "initialize",
              "params": {"protocolVersion": "2025-06-18", "capabilities": {},
                         "clientInfo": {"name": "t", "version": "0"}}},
    )
    assert r.status_code == 200, r.text

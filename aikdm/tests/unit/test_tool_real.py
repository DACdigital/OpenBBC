"""Unit tests for RealHTTPToolCaller. httpx is mocked — no real network calls."""

from __future__ import annotations

from typing import Any

import httpx

from aikdm.eval.tool_real import RealHTTPToolCaller


def _bundle(tools: list[dict[str, Any]]) -> dict[str, Any]:
    return {"main_prompt": "", "skills": [], "tools": tools}


def _transport(handler):
    return httpx.MockTransport(handler)


def test_get_substitutes_path_params():
    seen: dict[str, Any] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["method"] = request.method
        seen["url"] = str(request.url)
        seen["headers"] = dict(request.headers)
        return httpx.Response(200, json={"id": 42, "name": "Widget"})

    caller = RealHTTPToolCaller(
        bundle=_bundle([{
            "name": "products.get",
            "method": "GET",
            "path": "/products/{id}",
            "path_params": [{"name": "id"}],
            "query_params": [],
        }]),
        tool_backends={"products.get": {"base_url": "https://api.example",
                                        "default_headers": {"Authorization": "Bearer default"}}},
        header_overrides={"X-Tenant": "acme"},
        transport=_transport(handler),
    )

    out = caller.call("products.get", {"id": 42})

    assert out["source"] == "real"
    assert out["result"] == {"id": 42, "name": "Widget"}
    assert seen["method"] == "GET"
    assert seen["url"] == "https://api.example/products/42"
    assert seen["headers"]["authorization"] == "Bearer default"
    assert seen["headers"]["x-tenant"] == "acme"


def test_post_sends_body_and_overrides_win_over_defaults():
    seen: dict[str, Any] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["method"] = request.method
        seen["body"] = request.content.decode()
        seen["headers"] = dict(request.headers)
        return httpx.Response(201, json={"created": True})

    caller = RealHTTPToolCaller(
        bundle=_bundle([{
            "name": "orders.create",
            "method": "POST",
            "path": "/orders",
            "body_shape": {"type": "object"},
        }]),
        tool_backends={"orders.create": {
            "base_url": "https://api.example",
            "default_headers": {"Authorization": "Bearer default", "X-Env": "dev"},
        }},
        header_overrides={"Authorization": "Bearer override"},  # overrides win
        transport=_transport(handler),
    )

    out = caller.call("orders.create", {"item": "widget", "qty": 3})

    assert out["source"] == "real"
    assert out["result"] == {"created": True}
    assert seen["method"] == "POST"
    body_no_ws = seen["body"].replace(" ", "")
    assert '"item":"widget"' in body_no_ws
    assert '"qty":3' in body_no_ws
    assert seen["headers"]["authorization"] == "Bearer override"
    assert seen["headers"]["x-env"] == "dev"
    # Content-Type auto-set for JSON body.
    assert seen["headers"]["content-type"].startswith("application/json")


def test_query_params_forwarded():
    seen: dict[str, Any] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["url"] = str(request.url)
        return httpx.Response(200, json=[])

    caller = RealHTTPToolCaller(
        bundle=_bundle([{
            "name": "products.list",
            "method": "GET",
            "path": "/products",
            "query_params": [{"name": "q"}, {"name": "limit"}],
        }]),
        tool_backends={"products.list": {"base_url": "http://api", "default_headers": {}}},
        header_overrides={},
        transport=_transport(handler),
    )

    out = caller.call("products.list", {"q": "widgets", "limit": 5})

    assert out["source"] == "real"
    assert "q=widgets" in seen["url"]
    assert "limit=5" in seen["url"]


def test_missing_backend_returns_error():
    caller = RealHTTPToolCaller(
        bundle=_bundle([{"name": "orders.list", "method": "GET", "path": "/orders"}]),
        tool_backends={},
        header_overrides={},
        transport=_transport(lambda r: httpx.Response(500)),
    )

    out = caller.call("orders.list", {})

    assert out["source"] == "error"
    assert "no backend wired" in out["result"]["error"].lower()


def test_unknown_tool_returns_error():
    caller = RealHTTPToolCaller(
        bundle=_bundle([]),
        tool_backends={"orders.list": {"base_url": "http://x", "default_headers": {}}},
        header_overrides={},
        transport=_transport(lambda r: httpx.Response(200)),
    )

    out = caller.call("nope.thing", {})

    assert out["source"] == "error"
    assert "unknown tool" in out["result"]["error"].lower()


def test_http_5xx_returns_error_with_status_and_body():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(500, json={"error": "boom"})

    caller = RealHTTPToolCaller(
        bundle=_bundle([{"name": "x", "method": "GET", "path": "/x"}]),
        tool_backends={"x": {"base_url": "http://api", "default_headers": {}}},
        header_overrides={},
        transport=_transport(handler),
    )

    out = caller.call("x", {})

    assert out["source"] == "error"
    assert out["result"]["status_code"] == 500
    assert out["result"]["body"] == {"error": "boom"}


def test_transport_error_returns_error():
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("refused")

    caller = RealHTTPToolCaller(
        bundle=_bundle([{"name": "x", "method": "GET", "path": "/x"}]),
        tool_backends={"x": {"base_url": "http://api", "default_headers": {}}},
        header_overrides={},
        transport=_transport(handler),
    )

    out = caller.call("x", {})

    assert out["source"] == "error"
    assert "http error" in out["result"]["error"].lower()

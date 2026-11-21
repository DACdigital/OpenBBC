"""Real MCP tool caller (mock_mcp_tools=false) for tools exported under
eval-input-v1 `mcp_backends` (spec eval-mcp-tools In → 6).

Mirrors open-bbcd's runtime MCPClientBackend:
- names are `<backend>__<tool>`; the prefix is stripped before tools/call;
- one streamable-HTTP session per backend, connected lazily on the first
  call with the exported headers, reused across turns, closed by aclose();
- a connect failure is sticky for the caller's lifetime (one simulated
  session here; one turn at runtime);
- results are flattened like Go's flattenMCPContent: {"content": [<every raw
  block>], "text": "<text blocks joined by \\n>"} with "text" omitted when
  there are none. Server isError → same object, marked as an error.
  Connect / transport errors → {"error": "<message>"}, marked as an error.

Each backend connection runs in its own asyncio task that owns the SDK's
anyio contexts lexically and serves calls from a queue. A transport failure
therefore stays inside that task (it fails the pending call) instead of
cancelling the simulated session that made the call."""

from __future__ import annotations

import asyncio
import contextlib
from typing import Any

from mcp.client.session import ClientSession
from mcp.client.streamable_http import streamable_http_client
from mcp.shared._httpx_utils import create_mcp_http_client
from mcp.types import CallToolResult, TextContent

CONNECT_TIMEOUT_S = 30.0
CALL_TIMEOUT_S = 120.0


def flatten_call_result(result: CallToolResult) -> tuple[dict[str, Any], bool]:
    """(model-visible result, is_error) for a tools/call result."""
    blocks = [b.model_dump(by_alias=True, exclude_none=True, mode="json")
              for b in result.content]
    out: dict[str, Any] = {"content": blocks}
    texts = [b.text for b in result.content if isinstance(b, TextContent)]
    if texts:
        out["text"] = "\n".join(texts)
    return out, bool(result.is_error)


def call_error(backend: str, tool: str, detail: str) -> dict[str, Any]:
    """The runtime's transport-error shape: "mcp <backend>: call <tool>: <cause>"."""
    return {"error": f"mcp {backend}: call {tool}: {detail}"}


def _describe(exc: BaseException) -> str:
    """Leaf message of an (anyio) exception group, else str(exc)."""
    while isinstance(exc, BaseExceptionGroup) and exc.exceptions:
        exc = exc.exceptions[0]
    return str(exc) or type(exc).__name__


class _Connection:
    """One MCP client session, owned by a dedicated worker task."""

    def __init__(self, cfg: dict[str, Any], connect_timeout_s: float, call_timeout_s: float):
        self._cfg = cfg
        self._connect_timeout_s = connect_timeout_s
        self._call_timeout_s = call_timeout_s
        self._queue: asyncio.Queue[tuple[str, dict[str, Any], asyncio.Future] | None] = (
            asyncio.Queue())
        self._ready: asyncio.Future[None] = asyncio.get_running_loop().create_future()
        self._dead: BaseException | None = None
        self._task = asyncio.create_task(self._run())

    async def _run(self) -> None:
        try:
            async with (
                create_mcp_http_client(headers=dict(self._cfg.get("headers") or {})) as http,
                streamable_http_client(self._cfg["url"], http_client=http) as (read, write),
                ClientSession(read, write, read_timeout_seconds=self._call_timeout_s) as session,
            ):
                await session.initialize()
                self._ready.set_result(None)
                while (item := await self._queue.get()) is not None:
                    tool, args, fut = item
                    try:
                        res = await session.call_tool(
                            tool, args, read_timeout_seconds=self._call_timeout_s)
                    except Exception as e:  # noqa: BLE001 — handed to the caller
                        if not fut.done():
                            fut.set_exception(e)
                    else:
                        if not fut.done():
                            fut.set_result(res)
        except BaseException as e:  # noqa: BLE001 — the connection is dead; report it
            self._dead = e
            if not self._ready.done():
                self._ready.set_exception(e)
            while not self._queue.empty():
                item = self._queue.get_nowait()
                if item is not None and not item[2].done():
                    item[2].set_exception(e)
            if isinstance(e, asyncio.CancelledError):
                raise

    async def wait_ready(self) -> None:
        await asyncio.wait_for(asyncio.shield(self._ready), self._connect_timeout_s)

    async def call(self, tool: str, args: dict[str, Any]) -> CallToolResult:
        if self._dead is not None:
            raise RuntimeError(f"connection closed: {_describe(self._dead)}")
        fut: asyncio.Future = asyncio.get_running_loop().create_future()
        self._queue.put_nowait((tool, args, fut))
        done, _ = await asyncio.wait({fut, self._task}, return_when=asyncio.FIRST_COMPLETED,
                                     timeout=self._call_timeout_s + 5)
        if fut in done:
            return fut.result()
        if self._dead is not None:
            raise RuntimeError(f"connection closed: {_describe(self._dead)}")
        raise TimeoutError(f"no response within {self._call_timeout_s:g}s")

    async def close(self) -> None:
        if not self._task.done():
            self._queue.put_nowait(None)
            try:
                await asyncio.wait_for(self._task, 10)
            except BaseException:  # noqa: BLE001 — closing must never fail a session
                self._task.cancel()
        with contextlib.suppress(BaseException):
            await self._task


class MCPToolCaller:
    def __init__(self, backends: dict[str, Any], *,
                 connect_timeout_s: float = CONNECT_TIMEOUT_S,
                 call_timeout_s: float = CALL_TIMEOUT_S):
        self._backends = {name: (b.model_dump() if hasattr(b, "model_dump") else dict(b))
                          for name, b in (backends or {}).items()}
        self._connect_timeout_s = connect_timeout_s
        self._call_timeout_s = call_timeout_s
        self._conns: dict[str, _Connection] = {}
        self._failed: dict[str, dict[str, Any]] = {}

    def owns(self, name: str) -> bool:
        return self._split(name) is not None

    def _split(self, name: str) -> tuple[str, str] | None:
        # Longest backend name first, so "a__b" wins over "a" for "a__b__t".
        for backend in sorted(self._backends, key=len, reverse=True):
            prefix = backend + "__"
            if name.startswith(prefix) and len(name) > len(prefix):
                return backend, name[len(prefix):]
        return None

    async def _connection(self, backend: str) -> _Connection | dict[str, Any]:
        if backend in self._failed:
            return self._failed[backend]
        if backend in self._conns:
            return self._conns[backend]
        cfg = self._backends[backend]
        transport = cfg.get("transport") or "streamable_http"
        conn: _Connection | None = None
        try:
            if transport != "streamable_http":
                raise ValueError(f"unsupported transport {transport!r}")
            if not cfg.get("url"):
                raise ValueError("no url exported for this backend")
            conn = _Connection(cfg, self._connect_timeout_s, self._call_timeout_s)
            await conn.wait_ready()
        except Exception as e:  # noqa: BLE001 — sticky, reported to the model
            if conn is not None:
                await conn.close()
            self._failed[backend] = {"error": f"mcp {backend}: connect: {_describe(e)}"}
            return self._failed[backend]
        self._conns[backend] = conn
        return conn

    async def call(self, name: str, args: dict[str, Any]) -> dict[str, Any]:
        split = self._split(name)
        if split is None:
            return {"source": "error", "result": {"error": f"unknown tool {name!r}"}}
        backend, tool = split
        conn = await self._connection(backend)
        if isinstance(conn, dict):
            return {"source": "error", "result": conn}
        try:
            result = await conn.call(tool, args)
        except Exception as e:  # noqa: BLE001 — transport errors reach the model
            return {"source": "error", "result": call_error(backend, tool, _describe(e))}
        out, is_error = flatten_call_result(result)
        return {"source": "error" if is_error else "real", "result": out}

    async def aclose(self) -> None:
        conns, self._conns = list(self._conns.values()), {}
        for conn in conns:
            await conn.close()


class ToolRouter:
    """Sends `<backend>__*` names owned by the MCP caller to it; every other
    name to the existing caller (RealHTTPToolCaller), unchanged."""

    def __init__(self, mcp_caller: Any, fallback: Any):
        self._mcp = mcp_caller
        self._fallback = fallback

    async def call(self, name: str, args: dict[str, Any]) -> dict[str, Any]:
        if self._mcp.owns(name):
            return await self._mcp.call(name, args)
        return self._fallback.call(name, args)

    async def aclose(self) -> None:
        await self._mcp.aclose()

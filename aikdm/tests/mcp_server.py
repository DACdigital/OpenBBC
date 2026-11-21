"""Shared test helper: serve an MCPServer over streamable HTTP in a thread."""

import socket
import threading
import time

import uvicorn


def _free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def serve(srv):
    """Start srv on a free port. Returns (url, uvicorn.Server, thread); stop it
    with `server.should_exit = True; thread.join()`."""
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

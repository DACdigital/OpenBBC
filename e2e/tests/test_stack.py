"""Smoke tests for the compose stack (run after `make -C e2e up`)."""
import os
import subprocess
from pathlib import Path

import httpx

OPENBBCD = f"http://localhost:{os.environ.get('E2E_OPENBBCD_PORT', '18080')}"
LLM = f"http://localhost:{os.environ.get('E2E_LLM_PORT', '18001')}"
MCP = f"http://localhost:{os.environ.get('E2E_MCP_PORT', '18002')}"
COMPOSE_FILE = Path(__file__).resolve().parent.parent / "compose.yml"


def test_services_healthy():
    assert httpx.get(f"{OPENBBCD}/health", timeout=5).status_code == 200
    assert httpx.get(f"{LLM}/_requests", timeout=5).status_code == 200
    assert httpx.get(f"{MCP}/_calls", timeout=5).status_code == 200


def test_runner_reaches_llm():
    r = subprocess.run(
        ["docker", "compose", "-p", "openbbc-e2e", "-f", str(COMPOSE_FILE),
         "run", "--rm", "-T", "runner",
         "curl", "-fsS", "http://open-bbcd:8001/_requests"],
        capture_output=True, text=True, timeout=180,
    )
    assert r.returncode == 0, r.stderr

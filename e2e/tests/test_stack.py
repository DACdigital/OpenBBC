"""Smoke tests for the compose stack (run after `make -C e2e up`)."""
import subprocess

import httpx
from conftest import LLM, MCP, OPENBBCD
from support.runner import COMPOSE


def test_services_healthy():
    assert httpx.get(f"{OPENBBCD}/health", timeout=5).status_code == 200
    assert httpx.get(f"{LLM}/_requests", timeout=5).status_code == 200
    assert httpx.get(f"{MCP}/_calls", timeout=5).status_code == 200


def test_runner_reaches_llm():
    r = subprocess.run(
        [*COMPOSE, "run", "--rm", "-T", "runner",
         "curl", "-fsS", "http://open-bbcd:8001/_requests"],
        capture_output=True, text=True, timeout=180,
    )
    assert r.returncode == 0, r.stderr

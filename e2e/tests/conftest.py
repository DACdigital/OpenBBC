"""Shared fixtures. The stack must already be up (`make -C e2e up`)."""

import os
import uuid

import httpx
import pytest

from support.api import Api
from support.factory import ResourceFactory, Seeded
from support.fakes import Llm, Mcp
from support.sql import Db

OPENBBCD = f"http://localhost:{os.environ.get('E2E_OPENBBCD_PORT', '18080')}"
LLM = f"http://localhost:{os.environ.get('E2E_LLM_PORT', '18001')}"
MCP = f"http://localhost:{os.environ.get('E2E_MCP_PORT', '18002')}"
# Credentials match e2e/compose.yml's postgres service.
DSN = os.environ.get(
    "E2E_DATABASE_URL",
    f"postgres://e2e:e2e@localhost:{os.environ.get('E2E_PG_PORT', '18432')}/e2e")


@pytest.fixture(scope="session")
def stack() -> None:
    for url in (f"{OPENBBCD}/health", f"{LLM}/_requests", f"{MCP}/_calls"):
        try:
            status = httpx.get(url, timeout=5).status_code
        except httpx.HTTPError as e:
            pytest.fail(f"e2e stack not reachable at {url} ({e}); run `make -C e2e up`")
        assert status == 200, f"{url} -> HTTP {status}"


@pytest.fixture(scope="session")
def run_id() -> str:
    return uuid.uuid4().hex[:8]


@pytest.fixture(scope="session")
def db(stack) -> Db:
    d = Db(DSN)
    yield d
    d.close()


@pytest.fixture(scope="session")
def llm(stack) -> Llm:
    return Llm(LLM)


@pytest.fixture(scope="session")
def mcp(stack) -> Mcp:
    return Mcp(MCP)


@pytest.fixture(scope="session")
def api(stack, db) -> Api:
    return Api(OPENBBCD, db=db)


@pytest.fixture(scope="session")
def factory(api, db, llm, run_id) -> ResourceFactory:
    # Session-scoped so names stay unique across the whole run, and so
    # session/module-scoped fixtures (e.g. a shared real_eval) can use it.
    return ResourceFactory(api, db, llm, run_id, seed_dsn=DSN)


_STACK_FIXTURES = {"stack", "db", "llm", "mcp", "api", "factory", "seeded"}


@pytest.fixture(autouse=True)
def reset_fakes(request) -> None:
    """Fresh fakes for every stack test. Runs before function-scoped fixtures but after
    module/session-scoped ones, so a shared fixture must keep what it needs from the fakes
    in its own return value. Stack-free unit tests skip it."""
    if _STACK_FIXTURES & set(request.fixturenames):
        request.getfixturevalue("llm").reset()
        request.getfixturevalue("mcp").reset()


@pytest.fixture
def seeded(factory) -> Seeded:
    return factory.agent()

"""Status guard of seed_bundle.py (spec 2026-11-24-declarative-seed §4).

Integration test against a migrated open-bbcd database; skipped without
DATABASE_URL. Run from the repo root:

    DATABASE_URL=postgres://... uv run --with pytest --with 'psycopg[binary]' \\
        --with pyyaml pytest scripts/test_seed_bundle_status.py
"""

import json
import os
import subprocess
import sys
from pathlib import Path

import pytest

DSN = os.environ.get("DATABASE_URL")
pytestmark = pytest.mark.skipif(not DSN, reason="DATABASE_URL not set")

SCRIPT = Path(__file__).with_name("seed_bundle.py")

BUNDLE = """\
main_prompt: "<role>coffee bot</role>"
tools: []
skills:
  - name: place_order
    description: Take an order
    prompt: "<role>order</role>"
"""

FROZEN_ARCH = {"frozen": True, "tools": []}


@pytest.fixture
def conn():
    import psycopg

    with psycopg.connect(DSN, autocommit=True) as c:
        yield c


@pytest.fixture
def bundle(tmp_path):
    p = tmp_path / "bundle.yaml"
    p.write_text(BUNDLE)
    return p


def seed_version(conn, status, finalized=False):
    """Insert an agent + root version in `status`; returns (agent_id, version_id)."""
    with conn.cursor() as cur:
        cur.execute(
            "INSERT INTO agents (name, architecture, finalized_at) "
            "VALUES ('seed-bundle-test-' || gen_random_uuid(), COALESCE(%s::jsonb, '{}'::jsonb), "
            "CASE WHEN %s THEN now() END) RETURNING id::text",
            (json.dumps(FROZEN_ARCH) if finalized else None, finalized),
        )
        (agent_id,) = cur.fetchone()
        cur.execute(
            "INSERT INTO agent_versions (agent_id, status, flow_map_config) "
            "VALUES (%s::uuid, %s, '{}'::jsonb) RETURNING id::text",
            (agent_id, status),
        )
        (version_id,) = cur.fetchone()
    return agent_id, version_id


def cleanup(conn, agent_id):
    with conn.cursor() as cur:
        cur.execute("DELETE FROM agents WHERE id = %s::uuid", (agent_id,))


def run(version_id, bundle, force):
    args = [sys.executable, str(SCRIPT), "--version-id", version_id, "--bundle", str(bundle)]
    if force:
        args.append("--force")
    return subprocess.run(args, capture_output=True, text=True, env={**os.environ, "DATABASE_URL": DSN})


def read(conn, agent_id, version_id):
    with conn.cursor() as cur:
        cur.execute(
            "SELECT v.status, a.architecture FROM agent_versions v "
            "JOIN agents a ON a.id = v.agent_id WHERE v.id = %s::uuid",
            (version_id,),
        )
        return cur.fetchone()


@pytest.mark.parametrize("force", [False, True])
def test_ready_version_refused(conn, bundle, force):
    agent_id, version_id = seed_version(conn, "READY")
    try:
        res = run(version_id, bundle, force)
        assert res.returncode != 0, res.stdout
        assert "READY" in res.stderr
        status, _ = read(conn, agent_id, version_id)
        assert status == "READY"
    finally:
        cleanup(conn, agent_id)


@pytest.mark.parametrize(
    "status,force,ok",
    [
        ("PENDING", False, True),
        ("INITIALIZING", False, False),
        ("PENDING", True, True),
        ("INITIALIZING", True, True),
        ("DRAFT", True, False),
        ("DEPLOYED", True, False),
    ],
)
def test_status_rules(conn, bundle, status, force, ok):
    agent_id, version_id = seed_version(conn, status)
    try:
        res = run(version_id, bundle, force)
        assert (res.returncode == 0) == ok, res.stderr
        got, arch = read(conn, agent_id, version_id)
        assert got == ("READY" if ok else status)
        if ok:
            assert arch["skills_meta"] == [{"name": "place_order", "description": "Take an order"}]
    finally:
        cleanup(conn, agent_id)


def test_force_keeps_finalized_architecture(conn, bundle):
    agent_id, version_id = seed_version(conn, "PENDING", finalized=True)
    try:
        res = run(version_id, bundle, True)
        assert res.returncode == 0, res.stderr
        status, arch = read(conn, agent_id, version_id)
        assert status == "READY"
        assert arch == FROZEN_ARCH
    finally:
        cleanup(conn, agent_id)

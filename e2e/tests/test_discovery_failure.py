"""Scenario 6: an unreachable MCP backend fails the eval export and the training run."""

from collections.abc import Iterator

import pytest

from support.evals import EvalRun, eval_overrides
from support.runner import run_script


@pytest.fixture
def mcp_down(mcp) -> Iterator[None]:
    """fake-mcp answers 503 to everything. Restored here as well as by reset_fakes, so
    a failure midway never leaves it down for later tests."""
    mcp.down(True)
    try:
        yield
    finally:
        mcp.down(False)


def _discovery_prefix(backend: str) -> str:
    return f"mcp_discovery_failed: {backend}:"


def test_eval_export_502_fails_eval(factory, api, mcp, seeded):
    # The dataset is recorded through chat turns, which need fake-mcp up.
    dataset_version_id = factory.dataset(seeded)
    kb = seeded.backend_name
    mcp.down(True)
    try:
        eval_id = factory.eval(seeded, dataset_version_id, mock=False,
                               overrides=eval_overrides(seeded))
        r = api.export(eval_id)
    finally:
        mcp.down(False)

    assert r.status_code == 502, f"export -> HTTP {r.status_code}: {r.text[:500]}"
    body = r.json()
    assert body["error"] == "mcp_discovery_failed", body
    assert body["backend"] == kb, body

    status = api.eval_status(eval_id)
    assert status["status"] == "FAILED", status
    assert (status.get("error_message") or "").startswith(_discovery_prefix(kb)), status


def test_training_export_failure_fails_session(real_eval: EvalRun, factory, api, mcp_down):
    kb = real_eval.seeded.backend_name
    sid = factory.training(real_eval.eval_id)

    out = run_script("train_from_session.sh", sid, check=False)
    log = f"exit {out.returncode}\n--- stdout ---\n{out.stdout}\n--- stderr ---\n{out.stderr}"
    assert out.returncode != 0, log

    session = api.training_status(sid)
    assert session["status"] == "FAILED", (session, log)
    assert (session.get("error_message") or "").startswith(_discovery_prefix(kb)), (session, log)
    assert not session.get("started_at"), (session, log)  # /start was never called
    assert "marking session IN_PROGRESS" not in out.stdout + out.stderr, log

"""Run an eval end to end (create -> export -> run_eval.sh) and snapshot what it touched."""

from dataclasses import dataclass

import yaml

from support.factory import TARGET_ROLE, Seeded, agent_turn_script
from support.runner import run_script

EVAL_IDENTITY = "eval-user"


@dataclass(frozen=True)
class EvalRun:
    seeded: Seeded
    dataset_version_id: str
    eval_id: str
    export: dict             # the parsed export.yaml, fetched before the run
    mcp_calls: list[dict]    # fake-mcp /_calls during export + run
    target_requests: list[dict]  # fake-llm /_requests for fake-target during the run
    status: dict             # /evals.json entry after the run
    sessions: list[dict]     # eval_sessions rows (transcript, judgments, score)
    runner_output: str


def eval_overrides(seeded: Seeded, identity: str = EVAL_IDENTITY) -> dict[str, dict[str, str]]:
    return {seeded.backend_name: {"X-Test-Identity": identity}}


def run_eval(factory, api, db, llm, mcp, seeded: Seeded, dataset_version_id: str,
             mock: bool) -> EvalRun:
    """Fakes are reset first, so the snapshot holds only this eval's traffic (not the
    dataset's). The target replays the agent's per-turn script, so mock-mode replay
    matches the recorded (name, args) exactly."""
    llm.reset()
    mcp.reset()
    llm.script(TARGET_ROLE, agent_turn_script(seeded), cycle=True)
    try:
        eval_id = factory.eval(seeded, dataset_version_id, mock=mock,
                               overrides=eval_overrides(seeded))
        r = api.export(eval_id)
        assert r.status_code == 200, f"export -> HTTP {r.status_code}: {r.text[:500]}"
        export = yaml.safe_load(r.text)
        out = run_script("run_eval.sh", eval_id)
        return EvalRun(
            seeded=seeded, dataset_version_id=dataset_version_id, eval_id=eval_id,
            export=export, mcp_calls=mcp.calls(), target_requests=llm.requests(TARGET_ROLE),
            status=api.eval_status(eval_id), sessions=db.eval_sessions(eval_id),
            runner_output=out.stdout + out.stderr)
    finally:
        llm.script(TARGET_ROLE, [])  # don't leak the cyclic script


def transcript_tool_calls(run: EvalRun) -> list[dict]:
    """Every tool call ({name, args, result, source}) across the run's transcripts."""
    return [tc for s in run.sessions for entry in s["transcript"] or []
            for tc in entry.get("tool_calls") or []]

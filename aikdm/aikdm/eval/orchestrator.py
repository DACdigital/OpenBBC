"""Top-level eval orchestrator. Reads eval-input.yaml, runs each session
concurrently under settings.parallelism, aggregates a global weighted
pass-rate score, and returns an EvalResult.

Note: the spec calls out a separate `assemble.py` module; that job is small
enough (one loop over SessionOutcomes to sum criteria and build the result)
that we fold it inline here rather than adding a two-function module."""

from __future__ import annotations

import asyncio
import time

from aikdm import models
from aikdm.config import Settings
from aikdm.eval import judge as judge_mod
from aikdm.eval import runner as runner_mod
from aikdm.eval import simulator as sim_mod
from aikdm.eval.schemas import AikdmMeta, EvalInput, EvalResult, ResultSession


async def run_eval(inp: EvalInput, settings: Settings) -> EvalResult:
    started = time.time()
    sim_model = models.build_model("user_simulator", settings)
    judge_model = models.build_model("judge", settings)
    simulator_agent = sim_mod.build_simulator_agent(sim_model)
    judge_agent = judge_mod.build_judge_agent(judge_model)
    target_model_str = settings.model_target

    sem = asyncio.Semaphore(settings.parallelism)

    async def one(session):
        async with sem:
            try:
                return await runner_mod.run_session(
                    session=session,
                    bundle=inp.agent_version.bundle,
                    simulator_agent=simulator_agent,
                    judge_agent=judge_agent,
                    target_model=target_model_str,
                )
            except Exception as e:
                # Downgrade single-session error to a zero-score outcome
                # so other sessions still count.
                return runner_mod.SessionOutcome(
                    session_id=session.session_id,
                    result=ResultSession(
                        session_id=session.session_id,
                        score=0.0,
                        total_criteria=sum(len(c.items) for c in session.criteria),
                        passed_criteria=0,
                        transcript=[],
                        judgments=[],
                    ),
                    tokens_in=0, tokens_out=0,
                    error=str(e),
                )

    tasks = [asyncio.create_task(one(s)) for s in inp.dataset_version.sessions]
    outcomes = [await t for t in tasks]

    total = sum(o.result.total_criteria for o in outcomes)
    passed = sum(o.result.passed_criteria for o in outcomes)
    score = (passed / total) if total > 0 else 0.0

    meta = AikdmMeta(
        user_simulator_model=settings.model_user_simulator,
        judge_model=settings.model_judge,
        target_agent_model=settings.model_target,
        duration_seconds=round(time.time() - started, 3),
    )
    return EvalResult(
        schema_version="eval-result-v1",
        status="DONE",
        score=score,
        total_criteria=total,
        passed_criteria=passed,
        aikdm_meta=meta,
        sessions=[o.result for o in outcomes],
    )

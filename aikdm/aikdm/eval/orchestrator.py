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
from aikdm.eval import target as target_mod
from aikdm.eval.schemas import AikdmMeta, EvalInput, EvalResult, Judgment, ResultSession
from aikdm.eval.tool_mcp import MCPToolCaller, ToolRouter
from aikdm.eval.tool_mock import ToolMock
from aikdm.eval.tool_real import RealHTTPToolCaller


async def run_eval(inp: EvalInput, settings: Settings) -> EvalResult:
    started = time.time()
    sim_model = models.build_model("user_simulator", settings)
    judge_model = models.build_model("judge", settings)
    simulator_agent = sim_mod.build_simulator_agent(sim_model)
    judge_agent = judge_mod.build_judge_agent(judge_model)
    target_model_str = settings.model_target
    target_extra_kwargs = models.extra_kwargs("target", settings)

    sem = asyncio.Semaphore(settings.parallelism)

    # Choose the tool caller once. ToolMock's replay index is keyed on the
    # session transcript, so we build it per-session. RealHTTPToolCaller is
    # transcript-independent — one instance shared across all sessions.
    # MCP tools (mcp_backends) are mocked from their output_schema, or — in
    # real mode — called through one MCPToolCaller per simulated session
    # (one MCP client per backend per session), closed when it ends.
    mcp_backends = {name: b.model_dump() for name, b in inp.mcp_backends.items()}
    if inp.mock_mcp_tools:
        _, schema_by_name = target_mod.build_tool_specs(
            inp.agent_version.bundle, mcp_backends)

        def make_caller(session):
            ref = [m.model_dump() for m in session.transcript]
            return ToolMock(ref, schema_by_name)
    else:
        backends = {name: b.model_dump() for name, b in inp.tool_backends.items()}
        real_caller = RealHTTPToolCaller(
            bundle=inp.agent_version.bundle,
            tool_backends=backends,
            header_overrides=dict(inp.header_overrides),
        )

        def make_caller(_session):
            if mcp_backends:
                return ToolRouter(MCPToolCaller(mcp_backends), real_caller)
            return real_caller

    async def one(session):
        async with sem:
            caller = None
            try:
                caller = make_caller(session)
                return await runner_mod.run_session(
                    session=session,
                    bundle=inp.agent_version.bundle,
                    simulator_agent=simulator_agent,
                    judge_agent=judge_agent,
                    target_model=target_model_str,
                    tool_caller=caller,
                    mcp_backends=mcp_backends,
                    user_turn_mode=settings.eval_user_turn_mode,
                    history_mode=settings.eval_history_mode,
                    target_extra_kwargs=target_extra_kwargs,
                )
            except Exception as e:
                # Failures outside the executor (e.g. tool-caller construction,
                # script compilation). Every criterion still leaves a record.
                judgments = [
                    Judgment(message_id=c.message_id, criterion=item, passed=False,
                             inconclusive=True, reason=f"session error: {e}")
                    for c in session.criteria for item in c.items
                ]
                return runner_mod.SessionOutcome(
                    session_id=session.session_id,
                    result=ResultSession(
                        session_id=session.session_id,
                        score=0.0,
                        total_criteria=len(judgments),
                        passed_criteria=0,
                        transcript=[],
                        judgments=judgments,
                    ),
                    tokens_in=0, tokens_out=0,
                    error=str(e),
                )
            finally:
                if isinstance(caller, ToolRouter):
                    await caller.aclose()

    tasks = [asyncio.create_task(one(s)) for s in inp.dataset_version.sessions]
    outcomes = [await t for t in tasks]

    total = sum(o.result.total_criteria for o in outcomes)
    passed = sum(o.result.passed_criteria for o in outcomes)
    score = (passed / total) if total > 0 else 0.0

    meta = AikdmMeta(
        user_simulator_model=settings.model_user_simulator,
        judge_model=settings.model_judge,
        target_agent_model=settings.model_target,
        user_turn_mode=settings.eval_user_turn_mode,
        history_mode=settings.eval_history_mode,
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

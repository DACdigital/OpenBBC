"""Atomic per-unit generation + per-unit critic, fanned out in parallel.

Each unit (main_prompt and every internal skill) runs its OWN atomic loop:
gen -> crit -> maybe regen -> crit -> ... up to settings.critic_rounds.
A unit exits as soon as its critic returns no issues, or when it hits max
rounds (final issues land in metadata.critic_notes as advisory).

There is no bundle-wide critic. Cross-unit consistency comes from:
- Shared input (every unit sees the same flow_map_config + prompt schema).
- Orchestrator-enforced coverage (we loop over input skills; LLM cannot drop one).
- Description verbatim from input (LLM cannot wordsmith).

Units run in parallel via a ThreadPoolExecutor. Settings.parallelism bounds
the worker pool (default 10 — enough for most agents to run fully concurrent).
"""

from __future__ import annotations

import threading
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from datetime import UTC, datetime

from aikdm import agents, models, rendering
from aikdm.config import Settings
from aikdm.progress import ProgressEmitter
from aikdm.schemas import (
    Bundle,
    BundleMetadata,
    ExternalAction,
    FlowMapConfig,
    PromptSchema,
    Skill,
    SkillPrompt,
    TokenUsage,
)


@dataclass(frozen=True)
class _UnitOutcome:
    body: str
    rounds_run: int
    final_issues: list[str]
    gen_in: int
    gen_out: int
    crit_in: int
    crit_out: int


def run_generation(
    config: FlowMapConfig,
    prompt_schema: PromptSchema,
    settings: Settings,
    progress: ProgressEmitter,
) -> Bundle:
    progress.emit(
        "started",
        config_name=config.name,
        model_generator=settings.model_generator,
        model_critic=settings.model_critic,
        critic_rounds_max=settings.critic_rounds,
        parallelism=settings.parallelism,
    )

    gen_model = models.build_model("generator", settings)
    crit_model = models.build_model("critic", settings)
    main_agent = agents.build_main_prompt_agent(gen_model)
    skill_agent = agents.build_skill_prompt_agent(gen_model)
    main_critic = agents.build_main_prompt_critic_agent(crit_model)
    skill_critic = agents.build_skill_prompt_critic_agent(crit_model)

    main_scaffold = rendering.render_main_prompt_scaffold(config)
    internal_skills, external_skills = rendering.split_skills(config)
    capability_by_name = {c.name: c for c in config.capabilities}
    skill_scaffolds = {
        s.id: rendering.render_skill_prompt_scaffold(s, capability_by_name.get(s.capability_ref))
        for s in internal_skills
    }

    progress_lock = threading.Lock()

    def main_unit_task() -> _UnitOutcome:
        return _run_unit_atomic(
            unit_name="main_prompt",
            max_rounds=settings.critic_rounds,
            generate=lambda prev, issues: _gen_main(
                main_agent, config, main_scaffold, prev, issues
            ),
            criticise=lambda body: _crit_main(main_critic, config, body),
            progress=progress,
            progress_lock=progress_lock,
        )

    def skill_unit_task(skill: Skill) -> _UnitOutcome:
        scaffold = skill_scaffolds[skill.id]
        capability = capability_by_name.get(skill.capability_ref)
        return _run_unit_atomic(
            unit_name=f"skill:{skill.id}",
            max_rounds=settings.critic_rounds,
            generate=lambda prev, issues: _gen_skill(
                skill_agent, config, skill, capability, scaffold, prev, issues
            ),
            criticise=lambda body: _crit_skill(
                skill_critic, config, skill, capability, body
            ),
            progress=progress,
            progress_lock=progress_lock,
        )

    with ThreadPoolExecutor(max_workers=settings.parallelism) as pool:
        main_future = pool.submit(main_unit_task)
        skill_futures = {s.id: pool.submit(skill_unit_task, s) for s in internal_skills}
        main_outcome = main_future.result()
        skill_outcomes = {sid: f.result() for sid, f in skill_futures.items()}

    bundle = _assemble_bundle(
        config=config,
        prompt_schema=prompt_schema,
        settings=settings,
        main_outcome=main_outcome,
        skill_outcomes=skill_outcomes,
        internal_skills=internal_skills,
        external_skills=external_skills,
    )

    total_tokens = (
        main_outcome.gen_in + main_outcome.gen_out + main_outcome.crit_in + main_outcome.crit_out
        + sum(
            o.gen_in + o.gen_out + o.crit_in + o.crit_out
            for o in skill_outcomes.values()
        )
    )
    progress.emit(
        "done",
        total_tokens=total_tokens,
        main_rounds=main_outcome.rounds_run,
        skill_rounds={sid: o.rounds_run for sid, o in skill_outcomes.items()},
        units_with_residual_issues=(
            (1 if main_outcome.final_issues else 0)
            + sum(1 for o in skill_outcomes.values() if o.final_issues)
        ),
    )
    return bundle


def _run_unit_atomic(
    *,
    unit_name: str,
    max_rounds: int,
    generate,
    criticise,
    progress: ProgressEmitter,
    progress_lock: threading.Lock,
) -> _UnitOutcome:
    body: str | None = None
    issues: list[str] = []
    gen_in = gen_out = crit_in = crit_out = 0
    rounds_run = 0

    with progress_lock:
        progress.emit("unit_started", unit=unit_name, max_rounds=max_rounds)

    for round_index in range(1, max_rounds + 1):
        body, g_in, g_out = generate(body, issues)
        gen_in += g_in
        gen_out += g_out
        with progress_lock:
            progress.emit(
                "draft_done", unit=unit_name, round=round_index,
                tokens_in=g_in, tokens_out=g_out,
            )

        issues, c_in, c_out = criticise(body)
        crit_in += c_in
        crit_out += c_out
        with progress_lock:
            progress.emit(
                "critic_done", unit=unit_name, round=round_index,
                issues_count=len(issues), tokens_in=c_in, tokens_out=c_out,
            )

        rounds_run = round_index
        if not issues:
            break

    with progress_lock:
        progress.emit(
            "unit_completed", unit=unit_name,
            rounds_run=rounds_run, final_issues=len(issues),
        )

    assert body is not None
    return _UnitOutcome(
        body=body,
        rounds_run=rounds_run,
        final_issues=issues,
        gen_in=gen_in,
        gen_out=gen_out,
        crit_in=crit_in,
        crit_out=crit_out,
    )


def _gen_main(
    agent, config: FlowMapConfig, scaffold: str,
    prev: str | None, issues: list[str],
) -> tuple[str, int, int]:
    res = agents.call_main_prompt(
        agent, config, scaffold,
        previous_output=prev, critic_issues=issues,
    )
    return res.main_prompt, res.tokens_in, res.tokens_out


def _crit_main(agent, config: FlowMapConfig, body: str) -> tuple[list[str], int, int]:
    res = agents.call_main_prompt_critic(agent, config, body)
    return res.issues, res.tokens_in, res.tokens_out


def _gen_skill(
    agent, config: FlowMapConfig, skill: Skill, capability, scaffold: str,
    prev: str | None, issues: list[str],
) -> tuple[str, int, int]:
    res = agents.call_skill_prompt(
        agent, config, skill, capability, scaffold,
        previous_output=prev, critic_issues=issues,
    )
    return res.prompt, res.tokens_in, res.tokens_out


def _crit_skill(
    agent, config: FlowMapConfig, skill: Skill, capability, body: str,
) -> tuple[list[str], int, int]:
    res = agents.call_skill_prompt_critic(agent, config, skill, capability, body)
    return res.issues, res.tokens_in, res.tokens_out


def _assemble_bundle(
    *,
    config: FlowMapConfig,
    prompt_schema: PromptSchema,
    settings: Settings,
    main_outcome: _UnitOutcome,
    skill_outcomes: dict[str, _UnitOutcome],
    internal_skills: list[Skill],
    external_skills: list[Skill],
) -> Bundle:
    """Build the canonical Bundle from collected per-unit outcomes. Invariants:
    - One SkillPrompt per internal skill in input (in input order). Missing
      raises — this should be unreachable since we loop over input skills.
    - description is sourced verbatim from input.
    - external_actions reflects every external skill in input.
    - metadata.critic_notes aggregates residual issues across all units,
      tagged with the unit they came from.
    """
    missing = [s.id for s in internal_skills if s.id not in skill_outcomes]
    if missing:
        raise RuntimeError(
            f"skill prompts missing for internal skills: {missing}. "
            "This should be unreachable — orchestrator iterates over input skills."
        )

    skills = [
        SkillPrompt(
            name=s.id,
            description=s.description,
            prompt=skill_outcomes[s.id].body,
        )
        for s in internal_skills
    ]
    external_actions = [
        ExternalAction(skill_id=s.id, external_note=s.external_note) for s in external_skills
    ]

    critic_notes: list[str] = []
    for issue in main_outcome.final_issues:
        critic_notes.append(f"[main_prompt] {issue}")
    for s in internal_skills:
        for issue in skill_outcomes[s.id].final_issues:
            critic_notes.append(f"[skill:{s.id}] {issue}")

    total_gen_in = main_outcome.gen_in + sum(o.gen_in for o in skill_outcomes.values())
    total_gen_out = main_outcome.gen_out + sum(o.gen_out for o in skill_outcomes.values())
    total_crit_in = main_outcome.crit_in + sum(o.crit_in for o in skill_outcomes.values())
    total_crit_out = main_outcome.crit_out + sum(o.crit_out for o in skill_outcomes.values())

    max_rounds_run = max(
        [main_outcome.rounds_run] + [o.rounds_run for o in skill_outcomes.values()]
    )

    metadata = BundleMetadata(
        config_schema_version=config.schema_version,
        prompt_schema_version=prompt_schema.version,
        model_generator=settings.model_generator,
        model_critic=settings.model_critic,
        generated_at=datetime.now(UTC).isoformat(),
        critic_rounds_run=max_rounds_run,
        critic_notes=critic_notes,
        tokens_used=TokenUsage(
            generator_in=total_gen_in,
            generator_out=total_gen_out,
            critic_in=total_crit_in,
            critic_out=total_crit_out,
        ),
    )
    return Bundle(
        metadata=metadata,
        main_prompt=main_outcome.body,
        skills=skills,
        external_actions=external_actions,
    )

"""Critic loop + bundle finalization. Single entry point: run_generation()."""

from __future__ import annotations

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
    )

    gen_model = models.build_model("generator", settings)
    crit_model = models.build_model("critic", settings)
    generator = agents.build_generator_agent(gen_model)
    critic = agents.build_critic_agent(crit_model)

    scaffold_main = rendering.render_main_prompt_scaffold(config)
    internal_skills, external_skills = rendering.split_skills(config)
    capability_by_name = {c.name: c for c in config.capabilities}
    scaffold_skills = {
        s.id: rendering.render_skill_prompt_scaffold(s, capability_by_name.get(s.capability_ref))
        for s in internal_skills
    }

    bundle: Bundle | None = None
    issues: list[str] = []
    tokens = {"gen_in": 0, "gen_out": 0, "crit_in": 0, "crit_out": 0}
    rounds_run = 0

    for round_index in range(1, settings.critic_rounds + 1):
        progress.emit("round_started", round=round_index)
        gen = agents.call_generator(
            generator, config, scaffold_main, scaffold_skills,
            previous_bundle=bundle, previous_issues=issues,
        )
        tokens["gen_in"] += gen.tokens_in
        tokens["gen_out"] += gen.tokens_out
        progress.emit("draft_done", round=round_index,
                      tokens_in=gen.tokens_in, tokens_out=gen.tokens_out)

        bundle = gen.bundle
        crit = agents.call_critic(critic, config, bundle)
        tokens["crit_in"] += crit.tokens_in
        tokens["crit_out"] += crit.tokens_out
        issues = crit.issues
        progress.emit("critic_done", round=round_index, issues_count=len(issues),
                      tokens_in=crit.tokens_in, tokens_out=crit.tokens_out)

        rounds_run = round_index
        if not issues:
            break

    assert bundle is not None  # loop runs at least once
    finalized = _finalize_bundle(
        bundle=bundle,
        config=config,
        prompt_schema=prompt_schema,
        external_skills=external_skills,
        settings=settings,
        rounds_run=rounds_run,
        final_issues=issues,
        tokens=tokens,
    )
    progress.emit("done",
                  total_tokens=sum(tokens.values()),
                  rounds_run=rounds_run,
                  final_issues=len(issues))
    return finalized


def _finalize_bundle(
    *,
    bundle: Bundle,
    config: FlowMapConfig,
    prompt_schema: PromptSchema,
    external_skills: list[Skill],
    settings: Settings,
    rounds_run: int,
    final_issues: list[str],
    tokens: dict[str, int],
) -> Bundle:
    """Apply orchestrator-owned invariants:
    - external_actions must reflect every external skill in input (one entry each)
    - skills[] must not contain any external skill IDs
    - metadata reflects actual run stats
    """
    external_ids = {s.id for s in external_skills}
    pruned_skills: list[SkillPrompt] = [s for s in bundle.skills if s.id not in external_ids]
    external_actions = [
        ExternalAction(skill_id=s.id, external_note=s.external_note) for s in external_skills
    ]
    metadata = BundleMetadata(
        config_schema_version=config.schema_version,
        prompt_schema_version=prompt_schema.version,
        model_generator=settings.model_generator,
        model_critic=settings.model_critic,
        generated_at=datetime.now(UTC).isoformat(),
        critic_rounds_run=rounds_run,
        critic_notes=list(final_issues),
        tokens_used=TokenUsage(
            generator_in=tokens["gen_in"],
            generator_out=tokens["gen_out"],
            critic_in=tokens["crit_in"],
            critic_out=tokens["crit_out"],
        ),
    )
    return Bundle(
        metadata=metadata,
        main_prompt=bundle.main_prompt,
        skills=pruned_skills,
        external_actions=external_actions,
    )

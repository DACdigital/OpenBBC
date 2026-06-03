"""Critic loop + bundle finalization. Single entry point: run_generation().

Per round we issue (1 + N + 1) LLM calls:
- 1 main_prompt call
- N skill_prompt calls (one per internal skill in input)
- 1 critic call over the assembled bundle

Generating skills one-per-call lets this orchestrator enforce coverage by
construction: we loop over the input's internal skills and demand a
response for each. There is no path by which an internal skill can be
silently dropped — if the LLM doesn't answer for it, the call raises and
generation fails (exit 3 at the CLI).
"""

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
    main_agent = agents.build_main_prompt_agent(gen_model)
    skill_agent = agents.build_skill_prompt_agent(gen_model)
    critic_agent = agents.build_critic_agent(crit_model)

    main_scaffold = rendering.render_main_prompt_scaffold(config)
    internal_skills, external_skills = rendering.split_skills(config)
    capability_by_name = {c.name: c for c in config.capabilities}
    skill_scaffolds = {
        s.id: rendering.render_skill_prompt_scaffold(s, capability_by_name.get(s.capability_ref))
        for s in internal_skills
    }

    main_prompt_text: str | None = None
    skill_prompt_bodies: dict[str, str] = {}
    issues: list[str] = []
    tokens = {
        "main_in": 0, "main_out": 0,
        "skill_in": 0, "skill_out": 0,
        "crit_in": 0, "crit_out": 0,
    }
    rounds_run = 0

    for round_index in range(1, settings.critic_rounds + 1):
        progress.emit("round_started", round=round_index)

        # 1. Generate (or regenerate) the main prompt.
        main_res = agents.call_main_prompt(
            main_agent, config, main_scaffold,
            previous_output=main_prompt_text, critic_issues=issues,
        )
        tokens["main_in"] += main_res.tokens_in
        tokens["main_out"] += main_res.tokens_out
        main_prompt_text = main_res.main_prompt
        progress.emit(
            "main_prompt_done", round=round_index,
            tokens_in=main_res.tokens_in, tokens_out=main_res.tokens_out,
        )

        # 2. Generate (or regenerate) one prompt per internal skill.
        for skill in internal_skills:
            skill_res = agents.call_skill_prompt(
                skill_agent, config, skill,
                capability_by_name.get(skill.capability_ref),
                skill_scaffolds[skill.id],
                main_prompt_text,
                previous_output=skill_prompt_bodies.get(skill.id),
                critic_issues=issues,
            )
            tokens["skill_in"] += skill_res.tokens_in
            tokens["skill_out"] += skill_res.tokens_out
            skill_prompt_bodies[skill.id] = skill_res.prompt
            progress.emit(
                "skill_prompt_done", round=round_index, skill=skill.id,
                tokens_in=skill_res.tokens_in, tokens_out=skill_res.tokens_out,
            )

        # 3. Assemble the bundle and run the critic over it.
        bundle = _assemble_bundle(
            config=config,
            prompt_schema=prompt_schema,
            settings=settings,
            main_prompt_text=main_prompt_text,
            skill_prompt_bodies=skill_prompt_bodies,
            internal_skills=internal_skills,
            external_skills=external_skills,
            rounds_run=round_index,
            final_issues=[],
            tokens=tokens,
        )
        crit = agents.call_critic(critic_agent, config, bundle)
        tokens["crit_in"] += crit.tokens_in
        tokens["crit_out"] += crit.tokens_out
        issues = crit.issues
        progress.emit(
            "critic_done", round=round_index, issues_count=len(issues),
            tokens_in=crit.tokens_in, tokens_out=crit.tokens_out,
        )

        rounds_run = round_index
        if not issues:
            break

    assert main_prompt_text is not None  # loop runs at least once
    finalized = _assemble_bundle(
        config=config,
        prompt_schema=prompt_schema,
        settings=settings,
        main_prompt_text=main_prompt_text,
        skill_prompt_bodies=skill_prompt_bodies,
        internal_skills=internal_skills,
        external_skills=external_skills,
        rounds_run=rounds_run,
        final_issues=issues,
        tokens=tokens,
    )
    progress.emit(
        "done",
        total_tokens=sum(tokens.values()),
        rounds_run=rounds_run,
        final_issues=len(issues),
    )
    return finalized


def _assemble_bundle(
    *,
    config: FlowMapConfig,
    prompt_schema: PromptSchema,
    settings: Settings,
    main_prompt_text: str,
    skill_prompt_bodies: dict[str, str],
    internal_skills: list[Skill],
    external_skills: list[Skill],
    rounds_run: int,
    final_issues: list[str],
    tokens: dict[str, int],
) -> Bundle:
    """Build the canonical Bundle from collected parts. Orchestrator-owned
    invariants enforced here:

    - One SkillPrompt per internal skill in input (in input order).
      Missing entries raise — the LLM cannot drop a skill.
    - description is sourced verbatim from input (configurator is source of truth).
    - external_actions reflects every external skill in input.
    - metadata reflects actual run stats.
    """
    missing = [s.id for s in internal_skills if s.id not in skill_prompt_bodies]
    if missing:
        raise RuntimeError(
            f"skill prompts missing for internal skills: {missing}. "
            "This should be unreachable — the orchestrator loops over input skills."
        )

    skills = [
        SkillPrompt(
            name=s.id,
            description=s.description,
            prompt=skill_prompt_bodies[s.id],
        )
        for s in internal_skills
    ]
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
            generator_in=tokens["main_in"] + tokens["skill_in"],
            generator_out=tokens["main_out"] + tokens["skill_out"],
            critic_in=tokens["crit_in"],
            critic_out=tokens["crit_out"],
        ),
    )
    return Bundle(
        metadata=metadata,
        main_prompt=main_prompt_text,
        skills=skills,
        external_actions=external_actions,
    )

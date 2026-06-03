import io
import json
from pathlib import Path

import pytest

from aikdm import agents, models, orchestrator
from aikdm.loader import load_flow_map_config, load_prompt_schema
from aikdm.progress import ProgressEmitter

CONFIG_PATH = Path(__file__).parents[1] / "fixtures" / "flow_map_config" / "coffee_shop.yaml"
SCHEMA_PATH = Path(__file__).parents[2] / "schemas" / "prompt-v1.yaml"


def _patch_adk(mocker):
    """Bypass ADK so LlmAgent never sees a real model."""
    mocker.patch.object(models, "build_model", return_value=mocker.MagicMock())
    mocker.patch.object(agents, "build_main_prompt_agent", return_value=mocker.MagicMock())
    mocker.patch.object(agents, "build_skill_prompt_agent", return_value=mocker.MagicMock())
    mocker.patch.object(agents, "build_main_prompt_critic_agent", return_value=mocker.MagicMock())
    mocker.patch.object(agents, "build_skill_prompt_critic_agent", return_value=mocker.MagicMock())


def _patch_main_clean(mocker):
    """call_main_prompt returns a fixed body; main_prompt_critic finds no issues."""
    main = mocker.patch.object(
        agents, "call_main_prompt",
        return_value=agents.MainPromptResult(
            main_prompt="<role>main</role>", tokens_in=10, tokens_out=20,
        ),
    )
    crit = mocker.patch.object(
        agents, "call_main_prompt_critic",
        return_value=agents.CriticResult(issues=[], tokens_in=3, tokens_out=3),
    )
    return main, crit


def _patch_skill_clean(mocker):
    """call_skill_prompt returns a per-skill body; skill_prompt_critic finds no issues."""
    def fake_gen(agent, config, skill, capability, scaffold,
                 *, previous_output=None, critic_issues=None):
        return agents.SkillPromptResult(
            skill_name=skill.id, prompt=f"<role>{skill.id}</role>",
            tokens_in=4, tokens_out=5,
        )

    gen = mocker.patch.object(agents, "call_skill_prompt", side_effect=fake_gen)
    crit = mocker.patch.object(
        agents, "call_skill_prompt_critic",
        return_value=agents.CriticResult(issues=[], tokens_in=2, tokens_out=2),
    )
    return gen, crit


def test_orchestrator_runs_main_and_each_skill_atomically(mocker, settings):
    _patch_adk(mocker)
    main, main_crit = _patch_main_clean(mocker)
    skill, skill_crit = _patch_skill_clean(mocker)

    bundle = orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(io.StringIO()),
    )

    # One main_prompt call + one main_prompt critic (both critics return no issues).
    assert main.call_count == 1
    assert main_crit.call_count == 1
    # Two internal skills (place_order, check_rewards) -> 2 generate + 2 critic.
    assert skill.call_count == 2
    assert skill_crit.call_count == 2
    # Bundle contains both internal skills, ordered as in input.
    assert [s.name for s in bundle.skills] == ["place_order", "check_rewards"]


def test_main_prompt_unit_retries_when_main_critic_has_issues(mocker, settings):
    _patch_adk(mocker)
    main = mocker.patch.object(
        agents, "call_main_prompt",
        return_value=agents.MainPromptResult(
            main_prompt="<role>main</role>", tokens_in=10, tokens_out=20,
        ),
    )
    main_crit = mocker.patch.object(
        agents, "call_main_prompt_critic",
        return_value=agents.CriticResult(
            issues=["main fails forever"], tokens_in=3, tokens_out=3,
        ),
    )
    _patch_skill_clean(mocker)

    bundle = orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(io.StringIO()),
    )

    # main_prompt hits max rounds (default 2) because critic never clears.
    assert main.call_count == 2
    assert main_crit.call_count == 2
    # Critic notes carry the residual main_prompt issue, tagged.
    assert any(
        n.startswith("[main_prompt]") and "main fails forever" in n
        for n in bundle.metadata.critic_notes
    )


def test_skill_unit_retries_independently_of_main(mocker, settings):
    """A skill that the critic keeps flagging hits its own max rounds without
    affecting main_prompt's loop. Each unit is atomic."""
    _patch_adk(mocker)
    _patch_main_clean(mocker)

    def fake_gen(agent, config, skill, capability, scaffold,
                 *, previous_output=None, critic_issues=None):
        return agents.SkillPromptResult(
            skill_name=skill.id, prompt=f"<role>{skill.id}</role>",
            tokens_in=4, tokens_out=5,
        )

    gen = mocker.patch.object(agents, "call_skill_prompt", side_effect=fake_gen)

    # check_rewards' critic always finds issues; place_order's clears immediately.
    def fake_crit(agent, config, skill, capability, prompt):
        if skill.id == "check_rewards":
            return agents.CriticResult(
                issues=["rewards body is wrong"], tokens_in=2, tokens_out=2,
            )
        return agents.CriticResult(issues=[], tokens_in=2, tokens_out=2)

    crit = mocker.patch.object(agents, "call_skill_prompt_critic", side_effect=fake_crit)

    bundle = orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(io.StringIO()),
    )

    # place_order: 1 gen + 1 crit. check_rewards: 2 gen + 2 crit (max rounds).
    assert gen.call_count == 3
    assert crit.call_count == 3
    # Residual issue tagged with the skill it came from.
    assert any(
        n.startswith("[skill:check_rewards]") and "rewards body is wrong" in n
        for n in bundle.metadata.critic_notes
    )
    # Other skills produced no residual issues.
    assert not any(n.startswith("[skill:place_order]") for n in bundle.metadata.critic_notes)


def test_orchestrator_emits_per_unit_lifecycle_events(mocker, settings):
    _patch_adk(mocker)
    _patch_main_clean(mocker)
    _patch_skill_clean(mocker)

    sink = io.StringIO()
    orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(sink),
    )

    events = [json.loads(line) for line in sink.getvalue().splitlines()]
    event_names = [e["event"] for e in events]
    assert event_names[0] == "started"
    assert event_names[-1] == "done"

    # Each unit fires unit_started, draft_done, critic_done, unit_completed.
    units_seen = {e.get("unit") for e in events if "unit" in e}
    assert units_seen == {"main_prompt", "skill:place_order", "skill:check_rewards"}

    # Per-unit completion events present.
    completed_units = {e["unit"] for e in events if e["event"] == "unit_completed"}
    assert completed_units == units_seen


def test_orchestrator_includes_every_internal_skill_in_output(mocker, settings):
    """Coverage invariant: bundle.skills contains exactly the input's
    internal skills, in input order, with descriptions copied verbatim."""
    _patch_adk(mocker)
    _patch_main_clean(mocker)
    _patch_skill_clean(mocker)

    cfg = load_flow_map_config(CONFIG_PATH)
    bundle = orchestrator.run_generation(
        cfg, load_prompt_schema(SCHEMA_PATH), settings, ProgressEmitter(io.StringIO()),
    )

    expected_internal = [s for s in cfg.skills if not s.external]
    assert [s.name for s in bundle.skills] == [s.id for s in expected_internal]
    for out_skill, in_skill in zip(bundle.skills, expected_internal, strict=True):
        assert out_skill.description == in_skill.description


def test_orchestrator_populates_external_actions(mocker, settings):
    _patch_adk(mocker)
    _patch_main_clean(mocker)
    _patch_skill_clean(mocker)

    bundle = orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(io.StringIO()),
    )

    assert len(bundle.external_actions) == 1
    assert bundle.external_actions[0].skill_id == "file_complaint"


def test_orchestrator_propagates_skill_generation_failure(mocker, settings):
    """If a skill unit raises (e.g. ADK / API failure), the orchestrator surfaces it."""
    _patch_adk(mocker)
    _patch_main_clean(mocker)
    mocker.patch.object(
        agents, "call_skill_prompt",
        side_effect=RuntimeError("simulated skill failure"),
    )

    with pytest.raises(RuntimeError, match="simulated skill failure"):
        orchestrator.run_generation(
            load_flow_map_config(CONFIG_PATH),
            load_prompt_schema(SCHEMA_PATH),
            settings,
            ProgressEmitter(io.StringIO()),
        )

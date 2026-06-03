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
    mocker.patch.object(agents, "build_critic_agent", return_value=mocker.MagicMock())


def _stub_skill_prompt(mocker, *, prompts_by_skill: dict[str, str] | None = None):
    """Stub call_skill_prompt so it returns a per-skill body looked up from
    a dict keyed by skill id. Defaults to a fixed body per skill."""

    def fake(agent, config, skill, capability, scaffold, main_prompt_for_context,
            *, previous_output=None, critic_issues=None):
        body = (prompts_by_skill or {}).get(skill.id, f"<role>{skill.id} body</role>")
        return agents.SkillPromptResult(
            skill_name=skill.id, prompt=body, tokens_in=3, tokens_out=4,
        )

    return mocker.patch.object(agents, "call_skill_prompt", side_effect=fake)


def test_orchestrator_early_exits_when_first_critic_returns_no_issues(mocker, settings):
    _patch_adk(mocker)
    main = mocker.patch.object(
        agents, "call_main_prompt",
        return_value=agents.MainPromptResult(
            main_prompt="<role>main</role>", tokens_in=10, tokens_out=20,
        ),
    )
    skill = _stub_skill_prompt(mocker)
    crit = mocker.patch.object(
        agents, "call_critic",
        return_value=agents.CriticResult(issues=[], tokens_in=5, tokens_out=5),
    )

    bundle = orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(io.StringIO()),
    )

    assert main.call_count == 1
    # 2 internal skills in the coffee_shop fixture (place_order, check_rewards).
    assert skill.call_count == 2
    assert crit.call_count == 1
    assert bundle.metadata.critic_rounds_run == 1
    assert bundle.metadata.critic_notes == []


def test_orchestrator_runs_full_max_rounds_when_issues_persist(mocker, settings):
    _patch_adk(mocker)
    main = mocker.patch.object(
        agents, "call_main_prompt",
        return_value=agents.MainPromptResult(
            main_prompt="<role>main</role>", tokens_in=10, tokens_out=20,
        ),
    )
    skill = _stub_skill_prompt(mocker)
    crit = mocker.patch.object(
        agents, "call_critic",
        return_value=agents.CriticResult(issues=["still wrong"], tokens_in=5, tokens_out=5),
    )

    bundle = orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(io.StringIO()),
    )

    # 2 rounds × (1 main + 2 skills + 1 critic) = 8 calls
    assert main.call_count == 2
    assert skill.call_count == 4  # 2 skills × 2 rounds
    assert crit.call_count == 2
    assert bundle.metadata.critic_rounds_run == 2
    assert bundle.metadata.critic_notes == ["still wrong"]


def test_orchestrator_emits_lifecycle_events_including_per_skill(mocker, settings):
    _patch_adk(mocker)
    mocker.patch.object(
        agents, "call_main_prompt",
        return_value=agents.MainPromptResult(
            main_prompt="<role>main</role>", tokens_in=10, tokens_out=20,
        ),
    )
    _stub_skill_prompt(mocker)
    mocker.patch.object(
        agents, "call_critic",
        return_value=agents.CriticResult(issues=[], tokens_in=5, tokens_out=5),
    )

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
    assert "main_prompt_done" in event_names
    assert "skill_prompt_done" in event_names
    assert "critic_done" in event_names
    assert event_names[-1] == "done"
    # Per-skill events carry the skill id.
    skill_events = [e for e in events if e["event"] == "skill_prompt_done"]
    assert {e["skill"] for e in skill_events} == {"place_order", "check_rewards"}


def test_orchestrator_includes_every_internal_skill_in_output(mocker, settings):
    """Coverage invariant: bundle.skills contains exactly the input's
    internal skills, in input order, with descriptions copied verbatim."""
    _patch_adk(mocker)
    mocker.patch.object(
        agents, "call_main_prompt",
        return_value=agents.MainPromptResult(
            main_prompt="<role>main</role>", tokens_in=10, tokens_out=20,
        ),
    )
    _stub_skill_prompt(mocker)
    mocker.patch.object(
        agents, "call_critic",
        return_value=agents.CriticResult(issues=[], tokens_in=5, tokens_out=5),
    )

    cfg = load_flow_map_config(CONFIG_PATH)
    bundle = orchestrator.run_generation(
        cfg, load_prompt_schema(SCHEMA_PATH), settings, ProgressEmitter(io.StringIO()),
    )

    expected_internal = [s for s in cfg.skills if not s.external]
    assert [s.name for s in bundle.skills] == [s.id for s in expected_internal]
    # Description verbatim from input.
    for out_skill, in_skill in zip(bundle.skills, expected_internal, strict=True):
        assert out_skill.description == in_skill.description


def test_orchestrator_populates_external_actions(mocker, settings):
    _patch_adk(mocker)
    mocker.patch.object(
        agents, "call_main_prompt",
        return_value=agents.MainPromptResult(
            main_prompt="<role>main</role>", tokens_in=10, tokens_out=20,
        ),
    )
    _stub_skill_prompt(mocker)
    mocker.patch.object(
        agents, "call_critic",
        return_value=agents.CriticResult(issues=[], tokens_in=5, tokens_out=5),
    )

    bundle = orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(io.StringIO()),
    )

    assert len(bundle.external_actions) == 1
    assert bundle.external_actions[0].skill_id == "file_complaint"
    assert bundle.external_actions[0].external_note == (
        "Customers file complaints in the support portal at support.example.com."
    )


def test_orchestrator_raises_if_skill_prompt_missing(mocker, settings):
    """Defense-in-depth: if the skill-prompt call ever returns without a body
    (which would only happen via direct misuse, not real LLM flow), the
    orchestrator refuses to produce an incomplete bundle."""
    _patch_adk(mocker)
    mocker.patch.object(
        agents, "call_main_prompt",
        return_value=agents.MainPromptResult(
            main_prompt="<role>main</role>", tokens_in=10, tokens_out=20,
        ),
    )

    # Return a body for only ONE of the two internal skills, simulating
    # whatever path could lead to a missing entry.
    def fake(agent, config, skill, capability, scaffold, main_prompt_for_context,
            *, previous_output=None, critic_issues=None):
        if skill.id == "place_order":
            return agents.SkillPromptResult(
                skill_name=skill.id, prompt="<role>p</role>", tokens_in=1, tokens_out=1,
            )
        # check_rewards: simulate a path where the result somehow doesn't land
        # in skill_prompt_bodies. We achieve this by raising — orchestrator
        # surfaces it.
        raise RuntimeError("simulated skill generation failure")

    mocker.patch.object(agents, "call_skill_prompt", side_effect=fake)

    with pytest.raises(RuntimeError, match="simulated"):
        orchestrator.run_generation(
            load_flow_map_config(CONFIG_PATH),
            load_prompt_schema(SCHEMA_PATH),
            settings,
            ProgressEmitter(io.StringIO()),
        )

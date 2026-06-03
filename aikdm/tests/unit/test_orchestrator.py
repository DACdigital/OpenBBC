import io
import json
from pathlib import Path

from aikdm import agents, models, orchestrator
from aikdm.loader import load_flow_map_config, load_prompt_schema
from aikdm.progress import ProgressEmitter
from aikdm.schemas import (
    Bundle,
    BundleMetadata,
    SkillPrompt,
    TokenUsage,
)

CONFIG_PATH = Path(__file__).parents[1] / "fixtures" / "flow_map_config" / "coffee_shop.yaml"
SCHEMA_PATH = Path(__file__).parents[2] / "schemas" / "prompt-v1.yaml"


def _patch_adk(mocker):
    """Bypass ADK so LlmAgent never sees a real model."""
    mocker.patch.object(models, "build_model", return_value=mocker.MagicMock())
    mocker.patch.object(agents, "build_generator_agent", return_value=mocker.MagicMock())
    mocker.patch.object(agents, "build_critic_agent", return_value=mocker.MagicMock())


def _fake_bundle(main: str = "<role>r</role>") -> Bundle:
    return Bundle(
        metadata=BundleMetadata(
            config_schema_version=1, prompt_schema_version="v1",
            model_generator="m", model_critic="m",
            generated_at="t", critic_rounds_run=0, critic_notes=[],
            tokens_used=TokenUsage(),
        ),
        main_prompt=main,
        skills=[
            SkillPrompt(id="place_order", role="write",
                        user_phrases=["I want a latte"], prompt="<role>p</role>"),
            SkillPrompt(id="check_rewards", role="read",
                        user_phrases=["check my rewards"], prompt="<role>p</role>"),
        ],
    )


def _generator_result(bundle=None):
    return agents.GeneratorResult(
        bundle=bundle if bundle is not None else _fake_bundle(),
        tokens_in=10, tokens_out=20,
    )


def _critic_result(*issues):
    return agents.CriticResult(issues=list(issues), tokens_in=5, tokens_out=5)


def test_orchestrator_early_exits_when_first_critic_returns_no_issues(mocker, settings):
    _patch_adk(mocker)
    gen = mocker.patch.object(agents, "call_generator", return_value=_generator_result())
    crit = mocker.patch.object(agents, "call_critic", return_value=_critic_result())

    sink = io.StringIO()
    bundle = orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(sink),
    )

    assert gen.call_count == 1
    assert crit.call_count == 1
    assert bundle.metadata.critic_rounds_run == 1
    assert bundle.metadata.critic_notes == []


def test_orchestrator_runs_full_max_rounds_when_issues_persist(mocker, settings):
    _patch_adk(mocker)
    gen = mocker.patch.object(agents, "call_generator", return_value=_generator_result())
    crit = mocker.patch.object(agents, "call_critic", return_value=_critic_result("still wrong"))

    bundle = orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(io.StringIO()),
    )

    assert gen.call_count == 2
    assert crit.call_count == 2
    assert bundle.metadata.critic_rounds_run == 2
    assert bundle.metadata.critic_notes == ["still wrong"]
    assert bundle.metadata.tokens_used.generator_in == 20
    assert bundle.metadata.tokens_used.generator_out == 40
    assert bundle.metadata.tokens_used.critic_in == 10
    assert bundle.metadata.tokens_used.critic_out == 10


def test_orchestrator_emits_lifecycle_events(mocker, settings):
    _patch_adk(mocker)
    mocker.patch.object(agents, "call_generator", return_value=_generator_result())
    mocker.patch.object(agents, "call_critic", return_value=_critic_result())

    sink = io.StringIO()
    orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(sink),
    )

    events = [json.loads(line)["event"] for line in sink.getvalue().splitlines()]
    assert events[0] == "started"
    assert "round_started" in events
    assert "draft_done" in events
    assert "critic_done" in events
    assert events[-1] == "done"

    done_lines = [line for line in sink.getvalue().splitlines() if '"event":"done"' in line]
    assert len(done_lines) == 1
    done_payload = json.loads(done_lines[0])
    assert done_payload["rounds_run"] == 1
    assert done_payload["total_tokens"] == 40  # 10+20+5+5


def test_orchestrator_populates_external_actions(mocker, settings):
    """External skills in input must surface as bundle.external_actions
    even if the generator forgets — the orchestrator owns this invariant."""
    _patch_adk(mocker)
    # Generator returns a bundle WITHOUT external_actions.
    mocker.patch.object(agents, "call_generator", return_value=_generator_result())
    mocker.patch.object(agents, "call_critic", return_value=_critic_result())

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


def test_orchestrator_drops_skill_prompts_for_external_skills(mocker, settings):
    _patch_adk(mocker)
    # Generator hallucinates a skill prompt for an external skill.
    hallucinated = _fake_bundle()
    hallucinated.skills.append(SkillPrompt(id="file_complaint", role="write",
                                           user_phrases=[], prompt="<role>bad</role>"))
    mocker.patch.object(agents, "call_generator", return_value=_generator_result(hallucinated))
    mocker.patch.object(agents, "call_critic", return_value=_critic_result())

    bundle = orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(io.StringIO()),
    )

    ids = {s.id for s in bundle.skills}
    assert "file_complaint" not in ids

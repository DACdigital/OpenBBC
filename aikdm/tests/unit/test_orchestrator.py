import io
import json
from pathlib import Path
from unittest.mock import MagicMock

import pytest

from aikdm import agents, models, orchestrator
from aikdm.config import load_settings
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


@pytest.fixture(autouse=True)
def _clear_settings_cache():
    load_settings.cache_clear()
    yield
    load_settings.cache_clear()


def _stub_settings(monkeypatch):
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-xxx")
    monkeypatch.delenv("OPENAI_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    return load_settings()


def _patch_agents(monkeypatch):
    """Patch ADK agent constructors so MagicMock model objects are accepted."""
    monkeypatch.setattr(agents, "build_generator_agent", lambda model: MagicMock())
    monkeypatch.setattr(agents, "build_critic_agent", lambda model: MagicMock())


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


def test_orchestrator_early_exits_when_first_critic_returns_no_issues(monkeypatch):
    settings = _stub_settings(monkeypatch)
    monkeypatch.setattr(models, "build_model", lambda role, settings: MagicMock())
    _patch_agents(monkeypatch)

    call_log = {"gen": 0, "crit": 0}

    def fake_generator(agent, config, scaffold_main, scaffold_skills,
                       previous_bundle=None, previous_issues=None):
        call_log["gen"] += 1
        return agents.GeneratorResult(bundle=_fake_bundle(), tokens_in=10, tokens_out=20)

    def fake_critic(agent, config, bundle):
        call_log["crit"] += 1
        return agents.CriticResult(issues=[], tokens_in=5, tokens_out=5)

    monkeypatch.setattr(agents, "call_generator", fake_generator)
    monkeypatch.setattr(agents, "call_critic", fake_critic)

    sink = io.StringIO()
    bundle = orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(sink),
    )

    assert call_log == {"gen": 1, "crit": 1}
    assert bundle.metadata.critic_rounds_run == 1
    assert bundle.metadata.critic_notes == []


def test_orchestrator_runs_full_max_rounds_when_issues_persist(monkeypatch):
    settings = _stub_settings(monkeypatch)
    monkeypatch.setattr(models, "build_model", lambda role, settings: MagicMock())
    _patch_agents(monkeypatch)

    call_log = {"gen": 0, "crit": 0}

    def fake_generator(agent, config, scaffold_main, scaffold_skills,
                       previous_bundle=None, previous_issues=None):
        call_log["gen"] += 1
        return agents.GeneratorResult(bundle=_fake_bundle(), tokens_in=10, tokens_out=20)

    def fake_critic(agent, config, bundle):
        call_log["crit"] += 1
        return agents.CriticResult(issues=["still wrong"], tokens_in=5, tokens_out=5)

    monkeypatch.setattr(agents, "call_generator", fake_generator)
    monkeypatch.setattr(agents, "call_critic", fake_critic)

    bundle = orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(io.StringIO()),
    )

    assert call_log == {"gen": 2, "crit": 2}
    assert bundle.metadata.critic_rounds_run == 2
    assert bundle.metadata.critic_notes == ["still wrong"]
    assert bundle.metadata.tokens_used.generator_in == 20
    assert bundle.metadata.tokens_used.generator_out == 40
    assert bundle.metadata.tokens_used.critic_in == 10
    assert bundle.metadata.tokens_used.critic_out == 10


def test_orchestrator_emits_lifecycle_events(monkeypatch):
    settings = _stub_settings(monkeypatch)
    monkeypatch.setattr(models, "build_model", lambda role, settings: MagicMock())
    _patch_agents(monkeypatch)
    monkeypatch.setattr(agents, "call_generator", lambda *a, **kw: agents.GeneratorResult(
        bundle=_fake_bundle(), tokens_in=10, tokens_out=20))
    monkeypatch.setattr(agents, "call_critic", lambda *a, **kw: agents.CriticResult(
        issues=[], tokens_in=5, tokens_out=5))

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


def test_orchestrator_populates_external_actions(monkeypatch):
    """External skills in input must surface as bundle.external_actions
    even if the generator forgets — the orchestrator owns this invariant."""
    settings = _stub_settings(monkeypatch)
    monkeypatch.setattr(models, "build_model", lambda role, settings: MagicMock())
    _patch_agents(monkeypatch)

    def fake_generator(*a, **kw):
        # generator returns a bundle WITHOUT external_actions
        b = _fake_bundle()
        return agents.GeneratorResult(bundle=b, tokens_in=10, tokens_out=20)

    monkeypatch.setattr(agents, "call_generator", fake_generator)
    monkeypatch.setattr(agents, "call_critic", lambda *a, **kw: agents.CriticResult(
        issues=[], tokens_in=5, tokens_out=5))

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


def test_orchestrator_drops_skill_prompts_for_external_skills(monkeypatch):
    settings = _stub_settings(monkeypatch)
    monkeypatch.setattr(models, "build_model", lambda role, settings: MagicMock())
    _patch_agents(monkeypatch)

    def fake_generator(*a, **kw):
        b = _fake_bundle()
        # generator hallucinates a skill prompt for an external skill
        b.skills.append(SkillPrompt(id="file_complaint", role="write",
                                    user_phrases=[], prompt="<role>bad</role>"))
        return agents.GeneratorResult(bundle=b, tokens_in=10, tokens_out=20)

    monkeypatch.setattr(agents, "call_generator", fake_generator)
    monkeypatch.setattr(agents, "call_critic", lambda *a, **kw: agents.CriticResult(
        issues=[], tokens_in=5, tokens_out=5))

    bundle = orchestrator.run_generation(
        load_flow_map_config(CONFIG_PATH),
        load_prompt_schema(SCHEMA_PATH),
        settings,
        ProgressEmitter(io.StringIO()),
    )

    ids = {s.id for s in bundle.skills}
    assert "file_complaint" not in ids

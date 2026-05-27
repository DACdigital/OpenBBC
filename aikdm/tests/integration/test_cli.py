from pathlib import Path
from unittest.mock import MagicMock

import yaml
from click.testing import CliRunner

from aikdm import agents, models
from aikdm.cli import main
from aikdm.schemas import (
    Bundle,
    BundleMetadata,
    SkillPrompt,
    TokenUsage,
)

CONFIG = Path(__file__).parents[1] / "fixtures" / "flow_map_config" / "coffee_shop.yaml"


def _stub_llm(monkeypatch):
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-xxx")
    monkeypatch.delenv("OPENAI_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    monkeypatch.setattr(models, "build_model", lambda role, settings: MagicMock())
    # Same pattern as Task 10 — also patch the agent factories so LlmAgent
    # doesn't try to validate the MagicMock as a model.
    monkeypatch.setattr(agents, "build_generator_agent", lambda model: MagicMock())
    monkeypatch.setattr(agents, "build_critic_agent", lambda model: MagicMock())

    def fake_generator(*a, **kw):
        return agents.GeneratorResult(
            bundle=Bundle(
                metadata=BundleMetadata(
                    config_schema_version=1, prompt_schema_version="v1",
                    model_generator="m", model_critic="m",
                    generated_at="t", critic_rounds_run=0, critic_notes=[],
                    tokens_used=TokenUsage(),
                ),
                main_prompt="<role>r</role>",
                skills=[
                    SkillPrompt(id="place_order", role="write",
                                user_phrases=["x"], prompt="<role>p</role>"),
                    SkillPrompt(id="check_rewards", role="read",
                                user_phrases=["y"], prompt="<role>p</role>"),
                ],
            ),
            tokens_in=10, tokens_out=20,
        )

    monkeypatch.setattr(agents, "call_generator", fake_generator)
    monkeypatch.setattr(agents, "call_critic",
                        lambda *a, **kw: agents.CriticResult(
                            issues=[], tokens_in=5, tokens_out=5))


def test_generate_agent_writes_bundle_to_stdout(monkeypatch, tmp_path):
    _stub_llm(monkeypatch)
    runner = CliRunner(mix_stderr=False)
    result = runner.invoke(main, ["generate-agent", "--config", str(CONFIG)])

    assert result.exit_code == 0, result.stderr
    bundle = yaml.safe_load(result.stdout)
    assert bundle["main_prompt"] == "<role>r</role>"
    assert bundle["metadata"]["prompt_schema_version"] == "v1"


def test_generate_agent_writes_bundle_to_output_path(monkeypatch, tmp_path):
    _stub_llm(monkeypatch)
    out = tmp_path / "bundle.yaml"
    runner = CliRunner(mix_stderr=False)
    result = runner.invoke(
        main, ["generate-agent", "--config", str(CONFIG), "--output", str(out)]
    )

    assert result.exit_code == 0, result.stderr
    bundle = yaml.safe_load(out.read_text())
    assert bundle["main_prompt"] == "<role>r</role>"


def test_generate_agent_emits_progress_events_to_stderr(monkeypatch):
    _stub_llm(monkeypatch)
    runner = CliRunner(mix_stderr=False)
    result = runner.invoke(main, ["generate-agent", "--config", str(CONFIG)])

    assert result.exit_code == 0
    events = [line for line in result.stderr.splitlines() if line.startswith("{")]
    assert any('"event":"started"' in e for e in events)
    assert any('"event":"done"' in e for e in events)


def test_input_validation_failure_returns_exit_2(monkeypatch, tmp_path):
    _stub_llm(monkeypatch)
    bad = tmp_path / "bad.yaml"
    bad.write_text("schema_version: 1\n")  # missing required fields
    runner = CliRunner(mix_stderr=False)
    result = runner.invoke(main, ["generate-agent", "--config", str(bad)])

    assert result.exit_code == 2
    assert '"error":"input_validation"' in result.stderr


def test_missing_config_file_returns_exit_2(monkeypatch, tmp_path):
    _stub_llm(monkeypatch)
    runner = CliRunner(mix_stderr=False)
    result = runner.invoke(main, ["generate-agent", "--config", "/tmp/missing-xyz.yaml"])

    assert result.exit_code == 2
    assert '"error":"input_io"' in result.stderr


def test_missing_api_key_returns_exit_2(monkeypatch):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    monkeypatch.delenv("OPENAI_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    runner = CliRunner(mix_stderr=False)
    result = runner.invoke(main, ["generate-agent", "--config", str(CONFIG)])

    assert result.exit_code == 2
    assert '"error":"config"' in result.stderr

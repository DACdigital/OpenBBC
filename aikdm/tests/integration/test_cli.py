from pathlib import Path

import pytest
import yaml
from click.testing import CliRunner

from aikdm import agents, models
from aikdm.cli import main
from aikdm.config import load_settings
from aikdm.schemas import (
    Bundle,
    BundleMetadata,
    SkillPrompt,
    TokenUsage,
)

CONFIG = Path(__file__).parents[1] / "fixtures" / "flow_map_config" / "coffee_shop.yaml"


@pytest.fixture(autouse=True)
def _isolate_from_dotenv(tmp_path, monkeypatch):
    """The CLI auto-loads .env from cwd-and-above. Chdir to tmp_path so
    no real .env on the developer's machine pollutes tests."""
    monkeypatch.chdir(tmp_path)


@pytest.fixture(autouse=True)
def _clear_settings_cache():
    load_settings.cache_clear()
    yield
    load_settings.cache_clear()


@pytest.fixture
def stub_llm(mocker, monkeypatch):
    """CLI runs end-to-end against a deterministic stubbed LLM pipeline.

    Sets ANTHROPIC_API_KEY so Settings validation passes, then replaces
    every ADK / agents seam so no network traffic happens.
    """
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-xxx")
    monkeypatch.delenv("OPENAI_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)

    mocker.patch.object(models, "build_model", return_value=mocker.MagicMock())
    mocker.patch.object(agents, "build_generator_agent", return_value=mocker.MagicMock())
    mocker.patch.object(agents, "build_critic_agent", return_value=mocker.MagicMock())

    bundle = Bundle(
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
    )
    mocker.patch.object(
        agents, "call_generator",
        return_value=agents.GeneratorResult(bundle=bundle, tokens_in=10, tokens_out=20),
    )
    mocker.patch.object(
        agents, "call_critic",
        return_value=agents.CriticResult(issues=[], tokens_in=5, tokens_out=5),
    )


def test_generate_agent_writes_bundle_to_stdout(stub_llm):
    runner = CliRunner()
    result = runner.invoke(main, ["generate-agent", "--config", str(CONFIG)])

    assert result.exit_code == 0, result.stderr
    bundle = yaml.safe_load(result.stdout)
    assert bundle["main_prompt"] == "<role>r</role>"
    assert bundle["metadata"]["prompt_schema_version"] == "v1"


def test_generate_agent_writes_bundle_to_output_path(stub_llm, tmp_path):
    out = tmp_path / "bundle.yaml"
    runner = CliRunner()
    result = runner.invoke(
        main, ["generate-agent", "--config", str(CONFIG), "--output", str(out)]
    )

    assert result.exit_code == 0, result.stderr
    bundle = yaml.safe_load(out.read_text())
    assert bundle["main_prompt"] == "<role>r</role>"


def test_generate_agent_emits_progress_events_to_stderr(stub_llm):
    runner = CliRunner()
    result = runner.invoke(main, ["generate-agent", "--config", str(CONFIG)])

    assert result.exit_code == 0
    events = [line for line in result.stderr.splitlines() if line.startswith("{")]
    assert any('"event":"started"' in e for e in events)
    assert any('"event":"done"' in e for e in events)


def test_input_validation_failure_returns_exit_2(stub_llm, tmp_path):
    bad = tmp_path / "bad.yaml"
    bad.write_text("schema_version: 1\n")  # missing required fields
    runner = CliRunner()
    result = runner.invoke(main, ["generate-agent", "--config", str(bad)])

    assert result.exit_code == 2
    assert '"error":"input_validation"' in result.stderr


def test_missing_config_file_returns_exit_2(stub_llm):
    runner = CliRunner()
    result = runner.invoke(main, ["generate-agent", "--config", "/tmp/missing-xyz.yaml"])

    assert result.exit_code == 2
    assert '"error":"input_io"' in result.stderr


def test_missing_api_key_returns_exit_2(monkeypatch):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    monkeypatch.delenv("OPENAI_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    runner = CliRunner()
    result = runner.invoke(main, ["generate-agent", "--config", str(CONFIG)])

    assert result.exit_code == 2
    assert '"error":"config"' in result.stderr


def test_generate_agent_expands_tilde_in_paths(stub_llm, monkeypatch, tmp_path):
    """Click's Path type doesn't expand ~; the CLI must."""
    monkeypatch.setenv("HOME", str(tmp_path))
    cfg_dir = tmp_path / "configs"
    cfg_dir.mkdir()
    cfg_file = cfg_dir / "test.yaml"
    cfg_file.write_text(Path(str(CONFIG)).read_text())

    runner = CliRunner()
    result = runner.invoke(main, [
        "generate-agent",
        "--config", "~/configs/test.yaml",
        "--output", "~/bundle.yaml",
    ])
    assert result.exit_code == 0, result.stderr
    assert (tmp_path / "bundle.yaml").exists()


def test_full_pipeline_golden_file(stub_llm):
    runner = CliRunner()
    result = runner.invoke(main, ["generate-agent", "--config", str(CONFIG)])

    assert result.exit_code == 0, result.stderr
    actual = yaml.safe_load(result.stdout)
    expected_path = (
        Path(__file__).parents[1] / "fixtures" / "expected_bundles" / "coffee_shop.yaml"
    )
    expected = yaml.safe_load(expected_path.read_text())

    # Normalize fields that vary by run
    actual["metadata"].pop("generated_at", None)
    expected["metadata"].pop("generated_at", None)

    assert actual == expected

import pytest

from aikdm.config import load_settings


@pytest.fixture
def settings(monkeypatch):
    """Default-loaded Settings for consumers that don't care about loading
    behavior — they just need a Settings instance to pass through.

    Use this in tests that USE settings (model factory, orchestrator).
    Tests that verify load_settings() itself (test_config.py) or that
    drive the CLI's internal load (test_cli.py) should control env directly.
    """
    load_settings.cache_clear()
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-xxx")
    for k in ("OPENAI_API_KEY", "GEMINI_API_KEY",
              "AIKDM_MODEL_GENERATOR", "AIKDM_MODEL_CRITIC",
              "AIKDM_CRITIC_ROUNDS", "AIKDM_LOG_LEVEL"):
        monkeypatch.delenv(k, raising=False)
    yield load_settings()
    load_settings.cache_clear()

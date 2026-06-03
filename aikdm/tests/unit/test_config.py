import pytest

from aikdm.config import ConfigError, load_settings


@pytest.fixture(autouse=True)
def _clear_settings_cache():
    load_settings.cache_clear()
    yield
    load_settings.cache_clear()


def _env(monkeypatch, **kwargs):
    for k in ("ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY",
              "AIKDM_MODEL_GENERATOR", "AIKDM_MODEL_CRITIC",
              "AIKDM_CRITIC_ROUNDS", "AIKDM_LOG_LEVEL"):
        monkeypatch.delenv(k, raising=False)
    for k, v in kwargs.items():
        monkeypatch.setenv(k, v)


def test_defaults(monkeypatch):
    _env(monkeypatch, ANTHROPIC_API_KEY="sk-xxx")
    s = load_settings()
    assert s.model_generator == "claude-opus-4-7"
    assert s.model_critic == "claude-opus-4-7"
    assert s.critic_rounds == 2
    assert s.log_level == "info"


def test_anthropic_model_requires_anthropic_key(monkeypatch):
    _env(monkeypatch)  # no keys at all
    with pytest.raises(ConfigError, match="ANTHROPIC_API_KEY"):
        load_settings()


def test_mixed_providers_require_both_keys(monkeypatch):
    _env(monkeypatch,
         ANTHROPIC_API_KEY="sk-xxx",
         AIKDM_MODEL_CRITIC="gemini/gemini-2.5-pro")
    with pytest.raises(ConfigError, match="GEMINI_API_KEY"):
        load_settings()


def test_openai_model(monkeypatch):
    _env(monkeypatch,
         OPENAI_API_KEY="sk-xxx",
         AIKDM_MODEL_GENERATOR="openai/gpt-4o",
         AIKDM_MODEL_CRITIC="openai/gpt-4o")
    s = load_settings()
    assert s.model_generator == "openai/gpt-4o"


def test_critic_rounds_must_be_positive(monkeypatch):
    _env(monkeypatch, ANTHROPIC_API_KEY="sk", AIKDM_CRITIC_ROUNDS="0")
    with pytest.raises(ConfigError):
        load_settings()


def test_load_settings_is_cached(monkeypatch):
    _env(monkeypatch, ANTHROPIC_API_KEY="sk-xxx")
    first = load_settings()
    second = load_settings()
    assert first is second

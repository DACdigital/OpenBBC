import pytest

from aikdm.config import load_settings
from aikdm.models import build_model


@pytest.fixture(autouse=True)
def _clear_settings_cache():
    load_settings.cache_clear()
    yield
    load_settings.cache_clear()


def test_build_model_for_generator(monkeypatch):
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-xxx")
    monkeypatch.delenv("OPENAI_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    settings = load_settings()
    model = build_model("generator", settings)
    assert model is not None
    # LiteLlm has a `model` attribute carrying the provider/name string
    assert getattr(model, "model", None) == "anthropic/claude-opus-4-7"


def test_build_model_unknown_role_raises(monkeypatch):
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-xxx")
    monkeypatch.delenv("OPENAI_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    settings = load_settings()
    with pytest.raises(ValueError):
        build_model("nonsense", settings)

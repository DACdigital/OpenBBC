import pytest

from aikdm.config import Settings
from aikdm.models import build_model


def test_build_model_for_generator(monkeypatch):
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-xxx")
    monkeypatch.delenv("OPENAI_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    settings = Settings.load()
    model = build_model("generator", settings)
    assert model is not None
    # LiteLlm has a `model` attribute carrying the provider/name string
    assert getattr(model, "model", None) == "anthropic/claude-opus-4-7"


def test_build_model_unknown_role_raises(monkeypatch):
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-xxx")
    monkeypatch.delenv("OPENAI_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    settings = Settings.load()
    with pytest.raises(ValueError):
        build_model("nonsense", settings)

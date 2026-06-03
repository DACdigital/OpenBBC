import pytest

from aikdm.models import build_model


def test_build_model_for_generator(settings):
    model = build_model("generator", settings)
    assert model is not None
    # LiteLlm has a `model` attribute carrying the provider/name string
    assert getattr(model, "model", None) == "anthropic/claude-opus-4-7"


def test_build_model_unknown_role_raises(settings):
    with pytest.raises(ValueError):
        build_model("nonsense", settings)

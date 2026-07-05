"""Model factory. Single DI seam — tests monkey-patch this module's
`build_model` to substitute a deterministic stub."""

from __future__ import annotations

from typing import Literal

from google.adk.models.lite_llm import LiteLlm  # type: ignore[import-untyped]

from aikdm.config import Settings

Role = Literal["generator", "critic", "user_simulator", "judge", "target"]


def _normalize_model_string(model: str) -> str:
    """LiteLlm wants 'provider/name'. Bare 'claude-...' → 'anthropic/claude-...'."""
    if "/" in model:
        return model
    if model.startswith("claude"):
        return f"anthropic/{model}"
    if model.startswith("gpt"):
        return f"openai/{model}"
    if model.startswith("gemini"):
        return f"gemini/{model}"
    raise ValueError(f"cannot infer LiteLlm provider for model {model!r}")


def build_model(role: Role, settings: Settings) -> LiteLlm:
    lookup = {
        "generator":      settings.model_generator,
        "critic":         settings.model_critic,
        "user_simulator": settings.model_user_simulator,
        "judge":          settings.model_judge,
        "target":         settings.model_target,
    }
    if role not in lookup:
        raise ValueError(f"unknown role {role!r}")
    return LiteLlm(model=_normalize_model_string(lookup[role]))

"""Model factory. Single DI seam — tests monkey-patch this module's
`build_model` to substitute a deterministic stub."""

from __future__ import annotations

from typing import Any, Literal, get_args

from google.adk.models.lite_llm import LiteLlm  # type: ignore[import-untyped]

from aikdm import request_extras
from aikdm.config import Settings

Role = Literal["generator", "critic", "user_simulator", "judge", "target", "teacher"]


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


def model_for_role(role: Role, settings: Settings) -> str:
    """The role's effective model string: AIKDM_MODEL_<ROLE> with the default
    applied, before provider-prefix normalisation."""
    lookup = {
        "generator":      settings.model_generator,
        "critic":         settings.model_critic,
        "user_simulator": settings.model_user_simulator,
        "judge":          settings.model_judge,
        "target":         settings.model_target,
        "teacher":        settings.model_teacher,
    }
    if role not in lookup:
        raise ValueError(f"unknown role {role!r}")
    return lookup[role]


def extra_kwargs(role: Role, settings: Settings) -> dict[str, Any]:
    """extra_headers / extra_body for the role, from the first
    OPENBBC_LLM_REQUEST_EXTRAS rule matching its effective model; {} when none
    matches, so callers add no kwargs at all."""
    rule = request_extras.resolve(settings.request_extras, model_for_role(role, settings))
    return rule.kwargs() if rule else {}


def log_request_extras(settings: Settings) -> None:
    """Startup log of the rules and the active one per role (no values)."""
    request_extras.log_rules(
        settings.request_extras,
        {role: model_for_role(role, settings) for role in get_args(Role)},
    )


def build_model(role: Role, settings: Settings) -> LiteLlm:
    model = model_for_role(role, settings)
    return LiteLlm(model=_normalize_model_string(model), **extra_kwargs(role, settings))

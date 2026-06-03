"""Env-driven configuration. Loaded once at startup, fail-fast on missing keys."""

from __future__ import annotations

import os
from functools import cache

from pydantic import Field, ValidationError
from pydantic_settings import BaseSettings, SettingsConfigDict


class ConfigError(Exception):
    pass


_PROVIDER_KEYS = {
    "anthropic": "ANTHROPIC_API_KEY",
    "openai": "OPENAI_API_KEY",
    "gemini": "GEMINI_API_KEY",
}


def _provider_of(model: str) -> str:
    """LiteLLM-style model strings are 'provider/name'. Default to anthropic
    for bare names that match claude*."""
    if "/" in model:
        return model.split("/", 1)[0]
    if model.startswith("claude"):
        return "anthropic"
    if model.startswith("gpt") or model.startswith("openai"):
        return "openai"
    if model.startswith("gemini"):
        return "gemini"
    raise ConfigError(f"cannot infer provider from model name: {model!r}")


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_prefix="AIKDM_",
        extra="ignore",
        protected_namespaces=(),
    )

    model_generator: str = "claude-opus-4-7"
    model_critic: str = "claude-opus-4-7"
    critic_rounds: int = Field(default=2, ge=1)
    log_level: str = "info"


@cache
def load_settings() -> Settings:
    """Single entrypoint for loading aikdm settings.

    Cached: returns the same Settings instance for the lifetime of the process.
    Tests that need a fresh load (after monkeypatching env vars) must call
    `load_settings.cache_clear()` first.
    """
    try:
        s = Settings()
    except ValidationError as e:
        raise ConfigError(str(e)) from e

    providers = {_provider_of(s.model_generator), _provider_of(s.model_critic)}
    for p in providers:
        env_name = _PROVIDER_KEYS.get(p)
        if env_name is None:
            raise ConfigError(f"unknown provider {p!r}")
        if not os.environ.get(env_name):
            raise ConfigError(f"missing {env_name} for provider {p}")

    return s

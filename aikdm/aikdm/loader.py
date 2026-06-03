"""YAML I/O at the boundary. All validation errors raised here are mapped
to exit code 2 by the CLI."""

from __future__ import annotations

import sys
from pathlib import Path
from typing import Any

import yaml
from pydantic import ValidationError

from aikdm.schemas import Bundle, FlowMapConfig, PromptSchema


class InputIOError(Exception):
    """File missing or unreadable."""


class InputValidationError(Exception):
    """YAML malformed or schema violation."""


def _str_representer(dumper: yaml.SafeDumper, data: str) -> yaml.ScalarNode:
    """Use block scalar style (`|`) for multi-line strings so XML prompts
    render as readable YAML rather than double-quoted lines with \\n escapes."""
    if "\n" in data:
        return dumper.represent_scalar("tag:yaml.org,2002:str", data, style="|")
    return dumper.represent_scalar("tag:yaml.org,2002:str", data)


class _BlockStyleDumper(yaml.SafeDumper):
    pass


_BlockStyleDumper.add_representer(str, _str_representer)


def _read_yaml(path: Path) -> Any:
    try:
        text = path.read_text()
    except FileNotFoundError as e:
        raise InputIOError(f"file not found: {path}") from e
    except OSError as e:
        raise InputIOError(f"cannot read {path}: {e}") from e
    try:
        return yaml.safe_load(text)
    except yaml.YAMLError as e:
        raise InputValidationError(f"malformed YAML in {path}: {e}") from e


def load_flow_map_config(path: Path | str) -> FlowMapConfig:
    data = _read_yaml(Path(path))
    try:
        return FlowMapConfig.model_validate(data)
    except ValidationError as e:
        raise InputValidationError(f"FlowMapConfig validation failed: {e}") from e


def load_prompt_schema(path: Path | str) -> PromptSchema:
    data = _read_yaml(Path(path))
    try:
        return PromptSchema.model_validate(data)
    except ValidationError as e:
        raise InputValidationError(f"PromptSchema validation failed: {e}") from e


def write_bundle(bundle: Bundle, output: Path | str | None) -> None:
    """Serialize bundle to YAML, to `output` path or stdout if None."""
    payload = yaml.dump(
        bundle.model_dump(mode="json"),
        Dumper=_BlockStyleDumper,
        sort_keys=False,
        default_flow_style=False,
        allow_unicode=True,
    )
    if output is None:
        sys.stdout.write(payload)
        sys.stdout.flush()
    else:
        Path(output).write_text(payload)

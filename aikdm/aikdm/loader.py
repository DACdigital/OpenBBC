"""YAML I/O at the boundary. All validation errors raised here are mapped
to exit code 2 by the CLI."""

from __future__ import annotations

import re
import sys
from pathlib import Path
from typing import Any

import yaml
from pydantic import ValidationError

from aikdm.schemas import Bundle, FlowMapConfig, PromptSchema

# Go's yaml.v3 emits block scalar headers with explicit indent indicators like
# `|4` whose interpretation differs from PyYAML's, breaking parsing. PyYAML
# auto-detects content indent perfectly when no indicator is given, so we
# strip the digits before parsing. Matches `|4`, `|+4`, `|-4`, `>4`, etc. at
# end of line (block scalar header position).
_BLOCK_SCALAR_INDENT_RE = re.compile(r"([|>])([+-]?)\d+(\s*)$", re.MULTILINE)


def _normalize_block_scalar_headers(text: str) -> str:
    """Strip explicit indent indicators from block scalar headers."""
    return _BLOCK_SCALAR_INDENT_RE.sub(r"\1\2\3", text)


class InputIOError(Exception):
    """File missing or unreadable."""


class InputValidationError(Exception):
    """YAML malformed or schema violation."""


def _read_yaml(path: Path) -> Any:
    try:
        text = path.read_text()
    except FileNotFoundError as e:
        raise InputIOError(f"file not found: {path}") from e
    except OSError as e:
        raise InputIOError(f"cannot read {path}: {e}") from e
    try:
        return yaml.safe_load(_normalize_block_scalar_headers(text))
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
    payload = yaml.safe_dump(
        bundle.model_dump(mode="json"),
        sort_keys=False,
        default_flow_style=False,
        allow_unicode=True,
    )
    if output is None:
        sys.stdout.write(payload)
        sys.stdout.flush()
    else:
        Path(output).write_text(payload)

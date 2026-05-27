"""aikdm CLI. Top-level routing + exit code mapping. Each command's body
is the smallest possible adapter — real work lives in orchestrator + loader."""

from __future__ import annotations

import json
import logging
import sys
from pathlib import Path

import click

from aikdm import orchestrator
from aikdm.config import ConfigError, Settings
from aikdm.loader import (
    InputIOError,
    InputValidationError,
    load_flow_map_config,
    load_prompt_schema,
    write_bundle,
)
from aikdm.progress import ProgressEmitter


def _print_error(error_kind: str, message: str) -> None:
    sys.stderr.write(
        json.dumps({"error": error_kind, "details": message}, separators=(",", ":")) + "\n"
    )
    sys.stderr.flush()


def _prompt_schema_path() -> Path:
    """Locate schemas/prompt-v1.yaml. In a normal install it sits next to
    the aikdm package as a sibling 'schemas/' directory."""
    return Path(__file__).parents[1] / "schemas" / "prompt-v1.yaml"


@click.group(help="aikdm — prompt generation and evaluation toolkit.")
def main() -> None:
    pass


@main.command("generate-agent",
              help="Generate an agent prompt bundle from a FlowMapConfig YAML.")
@click.option("--config", "config_path", type=click.Path(path_type=Path),
              required=True, help="Path to FlowMapConfig YAML.")
@click.option("--output", "output_path", type=click.Path(path_type=Path),
              required=False, default=None,
              help="Output bundle path (omit for stdout).")
def generate_agent(config_path: Path, output_path: Path | None) -> None:
    try:
        settings = Settings.load()
    except ConfigError as e:
        _print_error("config", str(e))
        sys.exit(2)

    logging.basicConfig(level=settings.log_level.upper(), stream=sys.stderr)

    try:
        config = load_flow_map_config(config_path)
    except InputIOError as e:
        _print_error("input_io", str(e))
        sys.exit(2)
    except InputValidationError as e:
        _print_error("input_validation", str(e))
        sys.exit(2)

    try:
        prompt_schema = load_prompt_schema(_prompt_schema_path())
    except (InputIOError, InputValidationError) as e:
        _print_error("config", f"prompt schema: {e}")
        sys.exit(2)

    emitter = ProgressEmitter(sys.stderr)
    try:
        bundle = orchestrator.run_generation(config, prompt_schema, settings, emitter)
    except Exception as e:
        _print_error("llm_unavailable", str(e))
        sys.exit(3)

    write_bundle(bundle, output_path)
    sys.exit(0)


if __name__ == "__main__":
    main()

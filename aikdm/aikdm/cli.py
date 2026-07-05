"""aikdm CLI. Top-level routing + exit code mapping. Each command's body
is the smallest possible adapter — real work lives in orchestrator + loader."""

from __future__ import annotations

import asyncio
import json
import logging
import sys
from pathlib import Path

import click
import yaml as _yaml
from dotenv import find_dotenv, load_dotenv

from aikdm import orchestrator
from aikdm.config import ConfigError, load_settings
from aikdm.eval.orchestrator import run_eval
from aikdm.eval.schemas import EvalInput
from aikdm.train.orchestrator import run_training
from aikdm.train.reporter import ProgressEmitter as TrainProgressEmitter, write_report
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
    config_path = config_path.expanduser()
    if output_path is not None:
        output_path = output_path.expanduser()
    load_dotenv(find_dotenv(usecwd=True))
    try:
        settings = load_settings()
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
        bundle = asyncio.run(
            orchestrator.run_generation(config, prompt_schema, settings, emitter)
        )
    except Exception as e:
        _print_error("llm_unavailable", str(e))
        sys.exit(3)

    write_bundle(bundle, output_path)
    sys.exit(0)


@main.command("evaluate", help="Run an agent-version × dataset-version eval.")
@click.option("--input", "input_path", type=click.Path(path_type=Path),
              required=True, help="Path to eval-input.yaml.")
@click.option("--output", "output_path", type=click.Path(path_type=Path),
              required=True, help="Where to write eval-result.json.")
def evaluate(input_path: Path, output_path: Path) -> None:
    input_path = input_path.expanduser()
    output_path = output_path.expanduser()
    load_dotenv(find_dotenv(usecwd=True))
    try:
        settings = load_settings()
    except ConfigError as e:
        _print_error("config", str(e))
        sys.exit(2)

    logging.basicConfig(level=settings.log_level.upper(), stream=sys.stderr)

    try:
        raw = _yaml.safe_load(input_path.read_text(encoding="utf-8"))
    except OSError as e:
        _print_error("input_io", str(e))
        sys.exit(2)
    except _yaml.YAMLError as e:
        _print_error("input_validation", str(e))
        sys.exit(2)

    try:
        inp = EvalInput.model_validate(raw)
    except Exception as e:
        _print_error("input_validation", str(e))
        sys.exit(2)

    try:
        result = asyncio.run(run_eval(inp, settings))
    except Exception as e:
        _print_error("llm_unavailable", str(e))
        sys.exit(3)

    output_path.write_text(result.model_dump_json(indent=2), encoding="utf-8")
    sys.exit(0)


@main.command("train-agent",
              help="Train an agent bundle for N epochs against a dataset version.")
@click.option("--input", "input_path", type=click.Path(path_type=Path),
              required=True, help="Path to eval-input.yaml (same format as `evaluate`).")
@click.option("--epochs", type=int, default=5, show_default=True,
              help="Maximum number of training epochs.")
@click.option("--patience", type=int, default=3, show_default=True,
              help="Early stop after this many consecutive non-improvements.")
@click.option("--out", "out_dir", type=click.Path(path_type=Path),
              required=True, help="Output directory (must be empty or missing).")
def train_agent(input_path: Path, epochs: int, patience: int, out_dir: Path) -> None:
    input_path = input_path.expanduser()
    out_dir = out_dir.expanduser()

    if out_dir.exists() and any(out_dir.iterdir()):
        _print_error("input_validation", f"output directory already populated: {out_dir}")
        sys.exit(2)

    load_dotenv(find_dotenv(usecwd=True))
    try:
        settings = load_settings()
    except ConfigError as e:
        _print_error("config", str(e))
        sys.exit(2)

    logging.basicConfig(level=settings.log_level.upper(), stream=sys.stderr)

    try:
        raw = _yaml.safe_load(input_path.read_text(encoding="utf-8"))
    except OSError as e:
        _print_error("input_io", str(e))
        sys.exit(2)
    except _yaml.YAMLError as e:
        _print_error("input_validation", str(e))
        sys.exit(2)

    try:
        inp = EvalInput.model_validate(raw)
    except Exception as e:
        _print_error("input_validation", str(e))
        sys.exit(2)

    emitter = TrainProgressEmitter()
    try:
        final_bundle, report = asyncio.run(run_training(
            inp, settings, epochs=epochs, patience=patience, emitter=emitter,
        ))
    except Exception as e:
        _print_error("llm_unavailable", str(e))
        sys.exit(3)

    out_dir.mkdir(parents=True, exist_ok=True)
    bundle_path = out_dir / "bundle.yaml"
    bundle_path.write_text(_yaml.safe_dump(final_bundle, sort_keys=False), encoding="utf-8")
    # Fill in the bundle path now that we know it.
    report = report.model_copy(update={"final_bundle_path": str(bundle_path)})
    write_report(report, out_dir)

    emitter("training_done", initial_score=report.initial_score,
            final_score=report.final_score,
            stopped_reason=report.stopped_reason,
            epochs_run=report.total_epochs_run)
    sys.exit(0)


if __name__ == "__main__":
    main()

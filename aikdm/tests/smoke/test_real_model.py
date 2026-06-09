import os
from pathlib import Path

import pytest
import yaml
from click.testing import CliRunner
from dotenv import find_dotenv, load_dotenv

from aikdm.cli import main

load_dotenv(find_dotenv(usecwd=True))

CONFIG = Path(__file__).parents[1] / "fixtures" / "flow_map_config" / "coffee_shop.yaml"


@pytest.mark.skipif(
    os.environ.get("RUN_SMOKE") != "1" or not os.environ.get("ANTHROPIC_API_KEY"),
    reason="smoke test gated by RUN_SMOKE=1 and ANTHROPIC_API_KEY",
)
def test_real_model_generates_valid_bundle():
    runner = CliRunner()
    result = runner.invoke(main, ["generate-agent", "--config", str(CONFIG)])
    assert result.exit_code == 0, result.stderr

    bundle = yaml.safe_load(result.stdout)
    assert bundle["metadata"]["prompt_schema_version"] == "v2"
    assert bundle["main_prompt"].strip().startswith("<")
    assert len(bundle["skills"]) >= 1
    assert all({"name", "description", "prompt"} <= set(s) for s in bundle["skills"])
    assert all("<capabilities>" in s["prompt"] for s in bundle["skills"])

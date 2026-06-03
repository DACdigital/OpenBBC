from pathlib import Path

import pytest

from aikdm.loader import (
    InputIOError,
    InputValidationError,
    load_flow_map_config,
    load_prompt_schema,
    write_bundle,
)
from aikdm.schemas import (
    Bundle,
    BundleMetadata,
    TokenUsage,
)

CONFIG = Path(__file__).parents[1] / "fixtures" / "flow_map_config" / "coffee_shop.yaml"
PROMPT_SCHEMA = Path(__file__).parents[2] / "schemas" / "prompt-v1.yaml"


def test_load_flow_map_config_happy_path():
    cfg = load_flow_map_config(CONFIG)
    assert cfg.name == "coffee_shop_agent"


def test_load_flow_map_config_file_missing(tmp_path):
    with pytest.raises(InputIOError):
        load_flow_map_config(tmp_path / "does-not-exist.yaml")


def test_load_flow_map_config_malformed_yaml(tmp_path):
    bad = tmp_path / "bad.yaml"
    bad.write_text("name: [unclosed")
    with pytest.raises(InputValidationError):
        load_flow_map_config(bad)


def test_load_flow_map_config_schema_violation(tmp_path):
    bad = tmp_path / "wrong.yaml"
    bad.write_text("schema_version: 1\nname: x\n")  # missing source
    with pytest.raises(InputValidationError):
        load_flow_map_config(bad)


def test_load_flow_map_config_tolerates_go_yaml_v3_indent_indicators(tmp_path):
    """Go's yaml.v3 emits `prose_md: |4` whose interpretation diverges from
    PyYAML's. The loader must normalize these before parsing."""
    yaml_path = tmp_path / "go-style.yaml"
    yaml_path.write_text(
        "schema_version: 1\n"
        "name: go-style\n"
        "source:\n"
        "  compiler_schema_version: 1\n"
        "  generated_from_sha: abc\n"
        "  app_name: demo\n"
        "capabilities:\n"
        "  - name: orders\n"
        "    summary: ''\n"
        "    prose_md: |4\n"
        "        # Orders\n"
        "\n"
        "        <!-- AGENT id=\"overview\" -->\n"
        "        Body line\n"
        "        <!-- /AGENT -->\n"
    )
    cfg = load_flow_map_config(yaml_path)
    assert cfg.capabilities[0].name == "orders"
    assert "# Orders" in cfg.capabilities[0].prose_md
    assert "<!-- AGENT" in cfg.capabilities[0].prose_md


def test_load_prompt_schema_loads_v1():
    schema = load_prompt_schema(PROMPT_SCHEMA)
    assert schema.version == "v1"


def test_write_bundle_to_path(tmp_path):
    bundle = Bundle(
        metadata=BundleMetadata(
            config_schema_version=1,
            prompt_schema_version="v1",
            model_generator="m",
            model_critic="m",
            generated_at="2026-05-27T00:00:00Z",
            critic_rounds_run=0,
            critic_notes=[],
            tokens_used=TokenUsage(),
        ),
        main_prompt="<role>r</role>",
    )
    out = tmp_path / "bundle.yaml"
    write_bundle(bundle, out)

    import yaml
    raw = yaml.safe_load(out.read_text())
    assert raw["main_prompt"] == "<role>r</role>"
    assert raw["metadata"]["prompt_schema_version"] == "v1"


def test_write_bundle_to_stdout(capsys):
    bundle = Bundle(
        metadata=BundleMetadata(
            config_schema_version=1,
            prompt_schema_version="v1",
            model_generator="m",
            model_critic="m",
            generated_at="2026-05-27T00:00:00Z",
            critic_rounds_run=0,
            critic_notes=[],
            tokens_used=TokenUsage(),
        ),
        main_prompt="<role>r</role>",
    )
    write_bundle(bundle, None)

    out = capsys.readouterr().out
    assert "main_prompt:" in out

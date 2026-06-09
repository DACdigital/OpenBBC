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


def test_load_prompt_schema_loads_v1():
    schema = load_prompt_schema(PROMPT_SCHEMA)
    assert schema.version == "v2"


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


def test_write_bundle_splits_adjacent_xml_tags_into_block_scalar(tmp_path):
    """LLM sometimes emits skill prompts as one long `<a/><b/><c/>` line.
    The dumper splits adjacent tags onto separate lines so YAML uses `|`."""
    from aikdm.schemas import SkillPrompt
    bundle = Bundle(
        metadata=BundleMetadata(
            config_schema_version=1, prompt_schema_version="v1",
            model_generator="m", model_critic="m",
            generated_at="t", critic_rounds_run=0, critic_notes=[],
            tokens_used=TokenUsage(),
        ),
        main_prompt="<role>R</role><scope>S</scope>",
        skills=[
            SkillPrompt(
                name="x", description="d",
                prompt="<role>R</role><objective>O</objective><resources/>",
            ),
        ],
    )
    out = tmp_path / "bundle.yaml"
    write_bundle(bundle, out)
    text = out.read_text()
    assert "main_prompt: |" in text
    # Each top-level tag should sit on its own line in the rendered block.
    assert "<role>R</role>\n  <scope>S</scope>" in text
    # And the skill's prompt is block-formatted too.
    assert "prompt: |" in text
    assert "<role>R</role>\n    <objective>O</objective>" in text


def test_write_bundle_uses_block_style_for_multiline_strings(tmp_path):
    """Multi-line prompt content should serialise with `|` (block literal),
    not quoted single-line with \\n escapes — readable YAML."""
    bundle = Bundle(
        metadata=BundleMetadata(
            config_schema_version=1, prompt_schema_version="v1",
            model_generator="m", model_critic="m",
            generated_at="t", critic_rounds_run=0, critic_notes=[],
            tokens_used=TokenUsage(),
        ),
        main_prompt="<role>line one</role>\n<scope>line two</scope>",
    )
    out = tmp_path / "bundle.yaml"
    write_bundle(bundle, out)

    text = out.read_text()
    # Block style: "main_prompt: |" on its own line, content follows indented.
    assert "main_prompt: |" in text
    # And NOT escape-encoded inline.
    assert "\\n" not in text

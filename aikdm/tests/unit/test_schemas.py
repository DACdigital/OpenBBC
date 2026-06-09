from pathlib import Path

import pytest
import yaml
from pydantic import ValidationError

from aikdm.schemas import (
    Bundle,
    BundleMetadata,
    ExternalAction,
    FlowMapConfig,
    PromptSchema,
    PromptSection,
    SkillPrompt,
    TokenUsage,
)

FIXTURE = Path(__file__).parents[1] / "fixtures" / "flow_map_config" / "coffee_shop.yaml"


def test_flow_map_config_parses_canonical_fixture():
    data = yaml.safe_load(FIXTURE.read_text())
    config = FlowMapConfig.model_validate(data)

    assert config.schema_version == 1
    assert config.name == "coffee_shop_agent"
    assert config.business_domain.startswith("Independent")
    assert len(config.capabilities) == 2
    assert len(config.skills) == 3
    assert len(config.flows) == 1
    assert config.source.app_name == "coffeeshop-web"
    assert config.source.stack["framework"] == "next"


def test_flow_map_config_partitions_internal_and_external_skills():
    data = yaml.safe_load(FIXTURE.read_text())
    config = FlowMapConfig.model_validate(data)

    internal = [s for s in config.skills if not s.external]
    external = [s for s in config.skills if s.external]
    assert {s.id for s in internal} == {"place_order", "check_rewards"}
    assert {s.id for s in external} == {"file_complaint"}


def test_flow_map_config_rejects_missing_required_fields():
    with pytest.raises(ValidationError):
        FlowMapConfig.model_validate({"schema_version": 1})  # missing name + others


PROMPT_SCHEMA_PATH = Path(__file__).parents[2] / "schemas" / "prompt-v1.yaml"


def test_prompt_schema_loads_from_yaml():
    data = yaml.safe_load(PROMPT_SCHEMA_PATH.read_text())
    schema = PromptSchema.model_validate(data)

    assert schema.version == "v2"
    main_section_names = [s.name for s in schema.main_prompt]
    assert "role" in main_section_names
    assert "guardrails" in main_section_names
    assert "external_actions" in main_section_names
    assert "capabilities" in main_section_names
    skill_section_names = [s.name for s in schema.skill_prompt]
    assert "capabilities" in skill_section_names


def test_prompt_section_classifies_source():
    section = PromptSection(name="scope", tag="scope", source="wizard_copied",
                            source_field="scope", required=True)
    assert section.source == "wizard_copied"


def test_bundle_round_trips_through_model_validate():
    bundle = Bundle(
        metadata=BundleMetadata(
            config_schema_version=1,
            prompt_schema_version="v1",
            model_generator="claude-opus-4-7",
            model_critic="claude-opus-4-7",
            generated_at="2026-05-27T00:00:00Z",
            critic_rounds_run=1,
            critic_notes=[],
            tokens_used=TokenUsage(generator_in=10, generator_out=10,
                                   critic_in=5, critic_out=5),
        ),
        main_prompt="<role>x</role>",
        skills=[
            SkillPrompt(name="place_order", description="Place an order",
                        prompt="<role>...</role>")
        ],
        external_actions=[
            ExternalAction(skill_id="file_complaint",
                           external_note="Use support portal.")
        ],
    )
    redumped = Bundle.model_validate(bundle.model_dump())
    assert redumped == bundle

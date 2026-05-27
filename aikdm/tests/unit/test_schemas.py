from pathlib import Path

import yaml

from aikdm.schemas import FlowMapConfig

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
    import pytest
    from pydantic import ValidationError

    with pytest.raises(ValidationError):
        FlowMapConfig.model_validate({"schema_version": 1})  # missing name + others

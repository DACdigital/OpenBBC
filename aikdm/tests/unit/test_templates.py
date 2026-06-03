from pathlib import Path

from aikdm.loader import load_flow_map_config
from aikdm.rendering import render_main_prompt_scaffold, render_skill_prompt_scaffold

CONFIG = Path(__file__).parents[1] / "fixtures" / "flow_map_config" / "coffee_shop.yaml"


def test_main_scaffold_contains_all_wizard_copied_sections():
    cfg = load_flow_map_config(CONFIG)
    xml = render_main_prompt_scaffold(cfg)
    assert "<scope>" in xml and cfg.scope in xml
    assert "<should_do>" in xml and cfg.should_do in xml
    assert "<should_not_do>" in xml and cfg.should_not_do in xml
    assert "<business_domain>" in xml and cfg.business_domain in xml


def test_main_scaffold_contains_skills_index_for_internal_skills_only():
    cfg = load_flow_map_config(CONFIG)
    xml = render_main_prompt_scaffold(cfg)
    assert "<skills_index>" in xml
    assert "place_order" in xml
    assert "check_rewards" in xml
    # external skill should appear under external_actions, not skills_index
    skills_index = xml.split("<skills_index>")[1].split("</skills_index>")[0]
    assert "file_complaint" not in skills_index


def test_main_scaffold_contains_external_actions_for_external_skills():
    cfg = load_flow_map_config(CONFIG)
    xml = render_main_prompt_scaffold(cfg)
    assert "<external_actions>" in xml
    external = xml.split("<external_actions>")[1].split("</external_actions>")[0]
    assert "file_complaint" in external
    assert "support portal" in external


def test_skill_scaffold_renders_proposed_tool_as_mcp_server_name():
    cfg = load_flow_map_config(CONFIG)
    skill = next(s for s in cfg.skills if s.id == "place_order")
    capability = next(c for c in cfg.capabilities if c.name == skill.capability_ref)
    xml = render_skill_prompt_scaffold(skill, capability)
    assert 'name="place_order"' in xml  # MCP server name == proposed_tool
    assert "<role>" in xml
    assert "<resources>" in xml

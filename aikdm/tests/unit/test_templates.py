from pathlib import Path

from aikdm.loader import load_flow_map_config
from aikdm.rendering import render_main_prompt_scaffold, render_skill_prompt_scaffold

CONFIG = Path(__file__).parents[1] / "fixtures" / "flow_map_config" / "coffee_shop.yaml"


def test_main_scaffold_has_llm_synthesized_placeholders_not_wizard_text():
    """All prose sections are LLM-synthesized now — wizard text never appears
    in the scaffold, only as reference in the user message body."""
    cfg = load_flow_map_config(CONFIG)
    xml = render_main_prompt_scaffold(cfg)
    # All section tags present
    for tag in ("<role>", "<scope>", "<personality>", "<business_domain>",
                "<should_do>", "<should_not_do>", "<guardrails>",
                "<workflows>", "<examples>", "<skills_index>"):
        assert tag in xml
    # Wizard text is NOT pre-rendered into the scaffold
    assert cfg.scope not in xml
    assert cfg.should_do not in xml
    assert cfg.business_domain not in xml


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


def test_main_scaffold_workflow_hints_list_included_flows_only():
    cfg = load_flow_map_config(CONFIG)
    xml = render_main_prompt_scaffold(cfg)
    workflows = xml.split("<workflows>")[1].split("</workflows>")[0]
    # The fixture has one flow with included=true: order_flow.
    assert "order_flow" in workflows
    # And no flow IDs marked included=false should appear.
    for f in cfg.flows:
        if not f.included:
            assert f.id not in workflows


def test_skill_scaffold_renders_linked_endpoints():
    cfg = load_flow_map_config(CONFIG)
    skill = next(s for s in cfg.skills if s.id == "place_order")
    xml = render_skill_prompt_scaffold(skill, cfg)
    # v2: tool name comes from the endpoint id (orders.create), not proposed_tool
    assert 'name="orders.create"' in xml
    assert "<role>" in xml
    assert "<tools>" in xml
    # v2: uses endpoints/linked_endpoints terminology, not capabilities/resources.
    assert "<capabilities>" not in xml
    assert "<resources>" not in xml

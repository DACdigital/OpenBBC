"""Render Jinja2 XML scaffolds for the generator. Scaffolds stub every
LLM-synthesized section as a comment and pre-fill config-derived sections
(skills_index, external_actions, workflow hints). The LLM owns all prose
content; wizard fields are reference data passed via the user message,
not pre-rendered into the scaffold."""

from __future__ import annotations

from jinja2 import Environment, PackageLoader, select_autoescape

from aikdm.schemas import Capability, FlowMapConfig, Skill


def _env() -> Environment:
    return Environment(
        loader=PackageLoader("aikdm", "templates"),
        autoescape=select_autoescape(default=False),
        trim_blocks=True,
        lstrip_blocks=True,
    )


def render_main_prompt_scaffold(config: FlowMapConfig) -> str:
    env = _env()
    template = env.get_template("main_prompt.xml.j2")
    return template.render(
        internal_skills=[s for s in config.skills if not s.external],
        external_skills=[s for s in config.skills if s.external],
        included_flows=[f for f in config.flows if f.included],
    )


def render_skill_prompt_scaffold(skill: Skill, capability: Capability | None) -> str:
    env = _env()
    template = env.get_template("skill_prompt.xml.j2")
    return template.render(skill=skill, capability=capability)


def split_skills(config: FlowMapConfig) -> tuple[list[Skill], list[Skill]]:
    internal = [s for s in config.skills if not s.external]
    external = [s for s in config.skills if s.external]
    return internal, external

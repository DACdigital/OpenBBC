"""Bundle patching. Pure functions — no I/O, no LLM. Given a bundle dict
and a list of SectionPatch, return a new bundle with those sections replaced.
Section IDs are intentionally coarse (main_prompt or skill.<name>.prompt);
sub-tag surgery inside main_prompt is a follow-up."""

from __future__ import annotations

import copy
from typing import Any

from aikdm.train.schemas import SectionPatch


class PatchError(ValueError):
    pass


def section_ids(bundle: dict[str, Any]) -> list[str]:
    """Enumerate the section IDs a teacher may edit for this bundle."""
    ids: list[str] = ["main_prompt"]
    for skill in bundle.get("skills") or []:
        name = skill.get("name") or ""
        if name:
            ids.append(f"skill.{name}.prompt")
    return ids


def apply_patches(bundle: dict[str, Any], patches: list[SectionPatch]) -> dict[str, Any]:
    """Return a NEW bundle with every patch applied. Raises PatchError on
    unknown section_id or unknown skill name. Deep-copies so callers can
    keep the original around."""
    out = copy.deepcopy(bundle)
    for p in patches:
        if p.section_id == "main_prompt":
            out["main_prompt"] = p.new
            continue
        if p.section_id.startswith("skill.") and p.section_id.endswith(".prompt"):
            skill_name = p.section_id[len("skill.") : -len(".prompt")]
            if not skill_name:
                raise PatchError(f"malformed section_id {p.section_id!r}")
            skill = _find_skill(out, skill_name)
            if skill is None:
                raise PatchError(f"unknown skill {skill_name!r} in section_id {p.section_id!r}")
            skill["prompt"] = p.new
            continue
        raise PatchError(f"unknown section {p.section_id!r}")
    return out


def _find_skill(bundle: dict[str, Any], name: str) -> dict[str, Any] | None:
    for s in bundle.get("skills") or []:
        if s.get("name") == name:
            return s
    return None

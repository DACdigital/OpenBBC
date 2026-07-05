"""Pure-function patcher tests. No LLM, no IO."""

from __future__ import annotations

import copy

import pytest

from aikdm.train.patcher import PatchError, apply_patches, section_ids
from aikdm.train.schemas import SectionPatch


def _bundle():
    return {
        "main_prompt": "You are helpful.",
        "tools": [{"name": "search", "path": "/s"}],
        "flows": [{"id": "browse"}],
        "skills": [
            {"name": "checkout", "description": "run checkout", "prompt": "old checkout"},
            {"name": "browse",   "description": "browse",       "prompt": "old browse"},
        ],
    }


def test_section_ids_lists_main_and_skills():
    ids = section_ids(_bundle())
    assert ids == ["main_prompt", "skill.checkout.prompt", "skill.browse.prompt"]


def test_apply_patches_replaces_main_prompt():
    b = _bundle()
    out = apply_patches(b, [
        SectionPatch(section_id="main_prompt", new="Be terse.", rationale="tighten"),
    ])
    assert out["main_prompt"] == "Be terse."
    # Untouched:
    assert out["tools"] == b["tools"]
    assert out["flows"] == b["flows"]
    assert out["skills"][0]["prompt"] == "old checkout"
    # Deep copy — input unchanged:
    assert b["main_prompt"] == "You are helpful."


def test_apply_patches_replaces_named_skill_prompt():
    out = apply_patches(_bundle(), [
        SectionPatch(section_id="skill.browse.prompt", new="new browse body", rationale="x"),
    ])
    assert out["skills"][0]["prompt"] == "old checkout"   # untouched
    assert out["skills"][1]["prompt"] == "new browse body"
    assert out["skills"][1]["description"] == "browse"    # untouched


def test_apply_patches_unknown_section_raises():
    with pytest.raises(PatchError, match="unknown section"):
        apply_patches(_bundle(), [
            SectionPatch(section_id="main_pmt", new="x", rationale="typo"),
        ])


def test_apply_patches_unknown_skill_raises():
    with pytest.raises(PatchError, match="unknown skill"):
        apply_patches(_bundle(), [
            SectionPatch(section_id="skill.ghost.prompt", new="x", rationale="x"),
        ])


def test_apply_patches_malformed_skill_section_raises():
    with pytest.raises(PatchError, match="malformed"):
        apply_patches(_bundle(), [
            SectionPatch(section_id="skill.prompt", new="x", rationale="x"),
        ])


def test_apply_patches_empty_list_is_deep_copy():
    b = _bundle()
    out = apply_patches(b, [])
    assert out == b
    out["main_prompt"] = "mutated"
    assert b["main_prompt"] == "You are helpful."


def test_apply_multiple_patches_in_order():
    out = apply_patches(_bundle(), [
        SectionPatch(section_id="main_prompt", new="P1", rationale="x"),
        SectionPatch(section_id="skill.browse.prompt", new="P2", rationale="x"),
    ])
    assert out["main_prompt"] == "P1"
    assert out["skills"][1]["prompt"] == "P2"

# tests/unit/test_client_context_golden.py
"""Cross-language golden test (spec client-context, Tests → golden fixture).
Both fixtures are written by open-bbcd's TestClientContextGoldenExport with
-update; regenerate them there, never by hand."""

import json
from pathlib import Path

import yaml

from aikdm.eval.schemas import EvalInput
from aikdm.eval.script import render_user_turn

FIXTURES = Path(__file__).parents[1] / "fixtures"


def test_render_user_turn_matches_go_golden():
    raw = yaml.safe_load((FIXTURES / "client_context_export.yaml").read_text(encoding="utf-8"))
    inp = EvalInput.model_validate(raw)
    expected = json.loads(
        (FIXTURES / "client_context_expected.json").read_text(encoding="utf-8"))
    got = {m.message_id: render_user_turn(m)
           for s in inp.dataset_version.sessions for m in s.transcript if m.role == "user"}
    assert got == expected
    assert sum("<client_context>" in v for v in got.values()) == 2

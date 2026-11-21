# tests/unit/test_mcp_parity_golden.py
"""Cross-language golden test (spec eval-mcp-tools In → 9). mcp_parity.json
is written by open-bbcd's TestMCPParityGolden with -update; regenerate it
there, never by hand. Results are compared as parsed JSON."""

import json
from pathlib import Path

import pytest
from mcp.types import CallToolResult

from aikdm.eval.target import build_tool_specs
from aikdm.eval.tool_mcp import call_error, flatten_call_result

FIXTURE = json.loads(
    (Path(__file__).parents[1] / "fixtures" / "mcp_parity.json").read_text(encoding="utf-8"))
CASES = {c["name"]: c for c in FIXTURE["cases"]}


def test_all_five_cases_present():
    assert set(CASES) == {"single_text", "multiple_text", "text_and_image", "is_error",
                          "transport_error"}


@pytest.mark.parametrize("name", sorted(CASES))
def test_flattening_matches_go_runtime(name):
    case = CASES[name]
    backend = FIXTURE["backend"]
    if "transport_error" in case:
        tool = case["tool"].removeprefix(backend + "__")
        got, is_error = call_error(backend, tool, case["transport_error"]), True
    else:
        got, is_error = flatten_call_result(CallToolResult.model_validate(case["raw"]))
    assert json.loads(json.dumps(got)) == case["expected"]
    assert is_error == case["is_error"]


def test_export_tools_equal_runtime_tools_and_reach_the_model_unchanged():
    runtime = FIXTURE["runtime_tools"]
    export = FIXTURE["export_tools"]
    assert [{k: t[k] for k in ("name", "description", "input_schema")} for t in export] == runtime
    specs, _ = build_tool_specs({"tools": []}, {FIXTURE["backend"]: {"tools": export}})
    assert [s["function"] for s in specs] == [
        {"name": t["name"], "description": t["description"], "parameters": t["input_schema"]}
        for t in runtime
    ]

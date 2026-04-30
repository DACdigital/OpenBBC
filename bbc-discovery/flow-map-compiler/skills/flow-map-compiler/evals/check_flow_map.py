#!/usr/bin/env python3
"""Programmatic checks for a produced .flow-map/ directory.

Usage:
    python check_flow_map.py <path-to-.flow-map> --expect <eval-name>

Where <eval-name> is one of:
    nextjs-update-profile
    react-update-profile
    sveltekit-view-home

Exit code 0 iff every expectation passes. Prints one PASS/FAIL line per
expectation to stdout, then a summary to stderr. The skill-creator
grader subagent invokes this script and reads its stdout to populate
`grading.json`.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass
from pathlib import Path

HTTP_VERBS = ("GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS")


@dataclass
class Check:
    name: str
    passed: bool
    evidence: str


try:
    import yaml  # type: ignore
except ImportError:  # pragma: no cover
    yaml = None  # type: ignore


def split_frontmatter(path: Path) -> tuple[dict, str]:
    text = path.read_text()
    if not text.startswith("---\n"):
        return {}, text
    end = text.find("\n---\n", 4)
    if end == -1:
        return {}, text
    fm_block = text[4:end]
    body = text[end + 5 :]
    if yaml is None:
        raise RuntimeError(
            "PyYAML is required to parse frontmatter. Install with `pip install pyyaml`."
        )
    try:
        parsed = yaml.safe_load(fm_block) or {}
    except yaml.YAMLError:
        return {}, body
    if not isinstance(parsed, dict):
        return {}, body
    return parsed, body


def list_files(d: Path) -> list[Path]:
    if not d.is_dir():
        return []
    return sorted(p for p in d.iterdir() if p.is_file() and p.suffix == ".md")


def check_no_tool_leakage(flows_dir: Path, tool_names: list[str]) -> Check:
    leaks: list[str] = []
    for flow in list_files(flows_dir):
        body = flow.read_text()
        # Ignore frontmatter; flows are allowed `skills_used`/`skill_ref`.
        _, body_only = split_frontmatter(flow)
        for tool in tool_names:
            if tool and tool in body_only:
                leaks.append(f"{flow.name}: tool name `{tool}` present")
        for verb in HTTP_VERBS:
            if re.search(rf"(?<![A-Za-z]){verb}(?![A-Za-z])", body_only):
                leaks.append(f"{flow.name}: HTTP verb `{verb}` present")
        if "fetch(" in body_only or "axios." in body_only:
            leaks.append(f"{flow.name}: client call (`fetch(`/`axios.`) present")
        if re.search(r"/api/[A-Za-z]", body_only):
            leaks.append(f"{flow.name}: `/api/` path present")
    return Check(
        "flow bodies are tool-name-free and HTTP-detail-free",
        not leaks,
        "; ".join(leaks) if leaks else "no leakage detected",
    )


def round_trip_skill_capability(
    skills_dir: Path, capabilities_dir: Path
) -> Check:
    issues: list[str] = []
    for skill in list_files(skills_dir):
        fm, _ = split_frontmatter(skill)
        cap_ref = fm.get("capability_ref", "")
        if not cap_ref or "#" not in cap_ref:
            issues.append(f"{skill.name}: missing/invalid capability_ref")
            continue
        cap_path_rel, anchor = cap_ref.split("#", 1)
        flow_map_root = skill.parent.parent
        candidates = [
            (flow_map_root / cap_path_rel).resolve(),
            (skill.parent / cap_path_rel).resolve(),
        ]
        cap_path = next((c for c in candidates if c.exists()), None)
        if cap_path is None:
            issues.append(
                f"{skill.name}: capability_ref `{cap_ref}` resolves to none of {candidates}"
            )
            continue
        cap_fm, cap_body = split_frontmatter(cap_path)
        if f"{{#{anchor}}}" not in cap_body and f"id=\"{anchor}\"" not in cap_body:
            # Anchor may be implicit from a heading slug; relax to substring match.
            if anchor not in cap_body:
                issues.append(
                    f"{skill.name}: anchor `{anchor}` not found in {cap_path.name}"
                )
        proposed = fm.get("proposed_tool", "")
        cap_tools = [t.get("tool", "") for t in cap_fm.get("tools", [])]
        if proposed and proposed not in cap_tools:
            issues.append(
                f"{skill.name}: proposed_tool `{proposed}` not in "
                f"{cap_path.name} tools{cap_tools}"
            )
        skill_flows = set(fm.get("flows_using_this", []) or [])
        cap_flows = set(cap_fm.get("flows_using_this", []) or [])
        if skill_flows != cap_flows:
            issues.append(
                f"{skill.name}: flows_using_this {sorted(skill_flows)} != "
                f"capability {sorted(cap_flows)}"
            )
    return Check(
        "skill ↔ capability transitive integrity",
        not issues,
        "; ".join(issues) if issues else "all skills round-trip cleanly",
    )


def check_glossary_thin(glossary_path: Path) -> Check:
    if not glossary_path.exists():
        return Check("glossary is thin pivot only", False, "glossary.md missing")
    body = glossary_path.read_text()
    bad = []
    if "## Intent anchors" in body or "### Intent" in body:
        bad.append("contains old `Intent anchors` section")
    if not re.search(r"\| Skill .*\| User phrases .*\| Capability .*\| Proposed tool", body):
        bad.append("missing the canonical pivot-table header")
    return Check(
        "glossary.md is the thin pivot table",
        not bad,
        "; ".join(bad) if bad else "thin pivot only",
    )


EXPECTATIONS_BY_EVAL = {
    "nextjs-update-profile": dict(
        framework=("nextjs", "next.js", "next"),
        language="ts",
        expected_skill_role={"write"},
        skill_id_substrings=("user", "profile", "record"),
        proposed_tool_substrings=("users.",),
        capability_filename="users.md",
        flow_entry_substring="app/profile/page.tsx",
        cap_methods={"PUT", "POST", "PATCH"},
        cap_path_substring="users",
    ),
    "react-update-profile": dict(
        framework=("react",),
        language="ts",
        expected_skill_role={"write"},
        skill_id_substrings=("user", "profile", "record"),
        proposed_tool_substrings=("users.",),
        capability_filename="users.md",
        flow_entry_substring="src/pages/Profile.tsx",
        cap_methods={"PUT", "POST", "PATCH"},
        cap_path_substring="users",
    ),
    "sveltekit-view-home": dict(
        framework=("sveltekit",),
        language=None,
        expected_skill_role={"read", "load"},
        skill_id_substrings=("ping",),
        proposed_tool_substrings=("ping.",),
        capability_filename="ping.md",
        flow_entry_substring="src/routes/+page.svelte",
        cap_methods={"GET"},
        cap_path_substring="ping",
        cap_auth_allowed={"bearer", "cookie", "none"},
        cap_auth_expected="none",
    ),
}


def run_checks(flow_map: Path, eval_name: str) -> list[Check]:
    spec = EXPECTATIONS_BY_EVAL[eval_name]
    checks: list[Check] = []

    agents_path = flow_map / "AGENTS.md"
    fm_agents, _ = split_frontmatter(agents_path)
    counts = fm_agents.get("counts", {}) if isinstance(fm_agents.get("counts"), dict) else {}
    checks.append(
        Check(
            "AGENTS.md counts = {skills:1, flows:1, capabilities:1, proposed_tools:1}",
            counts.get("skills") == 1
            and counts.get("flows") == 1
            and counts.get("capabilities") == 1
            and counts.get("proposed_tools") == 1,
            f"counts={counts}",
        )
    )

    stack = fm_agents.get("stack", {}) if isinstance(fm_agents.get("stack"), dict) else {}
    framework_ok = any(
        f.lower() in str(stack.get("framework", "")).lower() for f in spec["framework"]
    )
    checks.append(
        Check(
            f"AGENTS.md stack.framework matches one of {spec['framework']}",
            framework_ok,
            f"got framework={stack.get('framework')!r}",
        )
    )
    if spec["language"]:
        checks.append(
            Check(
                f"AGENTS.md stack.language == {spec['language']}",
                stack.get("language") == spec["language"],
                f"got language={stack.get('language')!r}",
            )
        )

    skills = list_files(flow_map / "skills")
    checks.append(
        Check("skills/ has exactly one file", len(skills) == 1, f"found {[s.name for s in skills]}")
    )
    skill_fm: dict = {}
    if skills:
        skill_fm, _ = split_frontmatter(skills[0])
        sid = str(skill_fm.get("id", ""))
        id_ok = any(s in sid.lower() for s in spec["skill_id_substrings"])
        checks.append(
            Check(
                f"skill id contains one of {spec['skill_id_substrings']}",
                id_ok,
                f"id={sid!r}",
            )
        )
        role = str(skill_fm.get("role", ""))
        checks.append(
            Check(
                f"skill role ∈ {spec['expected_skill_role']}",
                role in spec["expected_skill_role"],
                f"role={role!r}",
            )
        )
        proposed = str(skill_fm.get("proposed_tool", ""))
        checks.append(
            Check(
                f"skill proposed_tool starts with one of {spec['proposed_tool_substrings']}",
                any(proposed.startswith(p) for p in spec["proposed_tool_substrings"]),
                f"proposed_tool={proposed!r}",
            )
        )

    flows = list_files(flow_map / "flows")
    checks.append(
        Check("flows/ has exactly one file", len(flows) == 1, f"found {[f.name for f in flows]}")
    )
    if flows:
        flow_fm, _ = split_frontmatter(flows[0])
        entry = str(flow_fm.get("entry", ""))
        checks.append(
            Check(
                f"flow.entry contains `{spec['flow_entry_substring']}`",
                spec["flow_entry_substring"] in entry,
                f"entry={entry!r}",
            )
        )
        used = flow_fm.get("skills_used", []) or []
        used_skill_ids = {str(u.get("skill", "")) for u in used if isinstance(u, dict)}
        skill_id = str(skill_fm.get("id", "")) if skill_fm else ""
        checks.append(
            Check(
                "flow.skills_used[] references the single skill",
                bool(skill_id) and skill_id in used_skill_ids,
                f"skills_used skill ids={used_skill_ids}, skill.id={skill_id!r}",
            )
        )
        skill_refs_ok = all(
            isinstance(u, dict)
            and str(u.get("skill_ref", "")).startswith("../skills/")
            and str(u.get("skill_ref", "")).endswith(".md")
            for u in used
        )
        checks.append(
            Check(
                "every skills_used[] entry has skill_ref pointing at ../skills/*.md",
                skill_refs_ok and bool(used),
                f"skills_used={used}",
            )
        )

    caps = list_files(flow_map / "capabilities")
    checks.append(
        Check(
            f"capabilities/ has exactly one file named `{spec['capability_filename']}`",
            len(caps) == 1 and caps[0].name == spec["capability_filename"],
            f"found {[c.name for c in caps]}",
        )
    )
    if caps and caps[0].name == spec["capability_filename"]:
        cap_fm, _ = split_frontmatter(caps[0])
        tools = cap_fm.get("tools", []) or []
        tool_names = [str(t.get("tool", "")) for t in tools]
        checks.append(
            Check(
                "capability has exactly one tool entry",
                len(tools) == 1,
                f"tools={tool_names}",
            )
        )
        if tools:
            t = tools[0]
            method = str(t.get("method", ""))
            path = str(t.get("path", ""))
            checks.append(
                Check(
                    f"capability tool method ∈ {spec['cap_methods']}",
                    method in spec["cap_methods"],
                    f"method={method!r}",
                )
            )
            checks.append(
                Check(
                    f"capability tool path contains `{spec['cap_path_substring']}`",
                    spec["cap_path_substring"] in path,
                    f"path={path!r}",
                )
            )
            auth = str(t.get("auth", ""))
            allowed = spec.get("cap_auth_allowed") or {"bearer", "cookie", "none"}
            checks.append(
                Check(
                    f"capability tool auth ∈ {sorted(allowed)}",
                    auth in allowed,
                    f"auth={auth!r}",
                )
            )
            if "cap_auth_expected" in spec:
                checks.append(
                    Check(
                        f"capability tool auth == `{spec['cap_auth_expected']}`",
                        auth == spec["cap_auth_expected"],
                        f"auth={auth!r}",
                    )
                )

    proposed_path = flow_map / "tools-proposed.json"
    if proposed_path.exists():
        try:
            data = json.loads(proposed_path.read_text())
            entries = data if isinstance(data, list) else data.get("tools", [])
            checks.append(
                Check(
                    "tools-proposed.json has exactly one tool entry",
                    isinstance(entries, list) and len(entries) == 1,
                    f"entries={entries!r}",
                )
            )
        except json.JSONDecodeError as e:
            checks.append(Check("tools-proposed.json is valid JSON", False, str(e)))
    else:
        checks.append(Check("tools-proposed.json exists", False, "missing file"))

    checks.append(check_glossary_thin(flow_map / "glossary.md"))
    checks.append(round_trip_skill_capability(flow_map / "skills", flow_map / "capabilities"))

    leak_tools: list[str] = []
    if skill_fm:
        leak_tools.append(str(skill_fm.get("proposed_tool", "")))
    checks.append(check_no_tool_leakage(flow_map / "flows", leak_tools))

    return checks


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("flow_map", type=Path, help="path to .flow-map directory")
    p.add_argument(
        "--expect", required=True, choices=sorted(EXPECTATIONS_BY_EVAL.keys())
    )
    p.add_argument("--json", action="store_true", help="emit grading.json on stdout")
    args = p.parse_args()

    if not args.flow_map.is_dir():
        print(f"FAIL: {args.flow_map} is not a directory", file=sys.stderr)
        return 2

    checks = run_checks(args.flow_map, args.expect)
    passed = sum(1 for c in checks if c.passed)
    total = len(checks)

    if args.json:
        out = {
            "expectations": [
                {"text": c.name, "passed": c.passed, "evidence": c.evidence}
                for c in checks
            ],
            "summary": {
                "passed": passed,
                "failed": total - passed,
                "total": total,
                "pass_rate": round(passed / total, 3) if total else 0.0,
            },
        }
        print(json.dumps(out, indent=2))
    else:
        for c in checks:
            mark = "PASS" if c.passed else "FAIL"
            print(f"{mark}: {c.name} — {c.evidence}")
        print(f"\n{passed}/{total} passed", file=sys.stderr)

    return 0 if passed == total else 1


if __name__ == "__main__":
    sys.exit(main())

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


def check_no_http_detail_leakage(flows_dir: Path, skills_dir: Path) -> Check:
    leaks: list[str] = []
    for d in (flows_dir, skills_dir):
        for f in list_files(d):
            _, body = split_frontmatter(f)
            for verb in HTTP_VERBS:
                if re.search(rf"(?<![A-Za-z]){verb}(?![A-Za-z]) /", body):
                    leaks.append(f"{f.relative_to(f.parent.parent)}: HTTP `{verb} /` present")
            if "fetch(" in body or "axios." in body:
                leaks.append(f"{f.relative_to(f.parent.parent)}: client call (`fetch(`/`axios.`) present")
            if re.search(r"/api/[A-Za-z]", body):
                leaks.append(f"{f.relative_to(f.parent.parent)}: `/api/` path present")
    return Check(
        "flow & skill bodies are HTTP-detail-free",
        not leaks,
        "; ".join(leaks) if leaks else "no leakage detected",
    )


def round_trip_skill_endpoint(
    skills_dir: Path, endpoints_dir: Path
) -> Check:
    """Assert every suggested_endpoints[].endpoint resolves to an endpoints/<id>.md
    file whose used_by_skills[] back-references the skill."""
    issues: list[str] = []
    for skill in list_files(skills_dir):
        fm, _ = split_frontmatter(skill)
        skill_id = fm.get("id", skill.stem)
        suggested = fm.get("suggested_endpoints") or []
        for entry in suggested:
            ep_id = (entry or {}).get("endpoint", "")
            if not ep_id:
                issues.append(f"{skill.name}: suggested_endpoints entry missing 'endpoint'")
                continue
            ep_path = endpoints_dir / f"{ep_id}.md"
            if not ep_path.exists():
                issues.append(f"{skill.name}: endpoint {ep_id} missing under endpoints/")
                continue
            ep_fm, _ = split_frontmatter(ep_path)
            used = ep_fm.get("used_by_skills") or []
            if skill_id not in used:
                issues.append(
                    f"{ep_path.name}: used_by_skills missing skill {skill_id} "
                    f"(skills/{skill.name} lists this endpoint in suggested_endpoints)"
                )
    return Check(
        "skill ↔ endpoint round-trip",
        not issues,
        "; ".join(issues) if issues else "all skill/endpoint references round-trip",
    )


def check_glossary_thin(glossary_path: Path) -> Check:
    if not glossary_path.exists():
        return Check("glossary is thin pivot only", False, "glossary.md missing")
    body = glossary_path.read_text()
    bad = []
    if "## Intent anchors" in body or "### Intent" in body:
        bad.append("contains old `Intent anchors` section")
    if not re.search(r"\| Skill .*\| User phrases .*\|.*Suggested endpoints.*\|.*Flows", body):
        bad.append("missing the canonical v2 pivot-table header (Skill | User phrases | Suggested endpoints | Flows)")
    return Check(
        "glossary.md is the thin pivot table",
        not bad,
        "; ".join(bad) if bad else "thin pivot only",
    )


EXPECTATIONS_BY_EVAL = {
    "nextjs-update-profile": dict(
        framework=("nextjs", "next.js", "next"),
        language="ts",
        skill_id_substrings=("user", "profile", "record", "account"),
        endpoint_methods={"PUT", "POST", "PATCH"},
        endpoint_path_substring="users",
        flow_entry_substring="app/profile/page.tsx",
    ),
    "react-update-profile": dict(
        framework=("react",),
        language="ts",
        skill_id_substrings=("user", "profile", "record", "account"),
        endpoint_methods={"PUT", "POST", "PATCH"},
        endpoint_path_substring="users",
        flow_entry_substring="src/pages/Profile.tsx",
    ),
    "sveltekit-view-home": dict(
        framework=("sveltekit",),
        language=None,
        skill_id_substrings=("ping", "health"),
        endpoint_methods={"GET"},
        endpoint_path_substring="ping",
        endpoint_path_exact="/api/ping",
        endpoint_auth_expected="none",
        endpoint_auth_allowed={"bearer", "cookie", "none"},
        flow_entry_substring="src/routes/+page.svelte",
    ),
}


def run_checks(flow_map: Path, eval_name: str) -> list[Check]:
    spec = EXPECTATIONS_BY_EVAL[eval_name]
    checks: list[Check] = []

    # --- AGENTS.md counts ---
    agents_path = flow_map / "AGENTS.md"
    fm_agents, _ = split_frontmatter(agents_path)
    counts = fm_agents.get("counts", {}) if isinstance(fm_agents.get("counts"), dict) else {}
    checks.append(
        Check(
            "AGENTS.md counts = {skills:1, flows:1, endpoints:1}",
            counts.get("skills") == 1
            and counts.get("flows") == 1
            and counts.get("endpoints") == 1,
            f"counts={counts}",
        )
    )

    # --- AGENTS.md stack ---
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

    # --- skills/ ---
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
        # skill must NOT have proposed_tool or capability_ref
        checks.append(
            Check(
                "skill frontmatter has no `proposed_tool` field",
                "proposed_tool" not in skill_fm,
                f"proposed_tool={skill_fm.get('proposed_tool')!r}" if "proposed_tool" in skill_fm else "absent (good)",
            )
        )
        checks.append(
            Check(
                "skill frontmatter has no `capability_ref` field",
                "capability_ref" not in skill_fm,
                f"capability_ref={skill_fm.get('capability_ref')!r}" if "capability_ref" in skill_fm else "absent (good)",
            )
        )
        # suggested_endpoints[] must have at least one entry
        suggested = skill_fm.get("suggested_endpoints") or []
        checks.append(
            Check(
                "skill has at least one suggested_endpoints[] entry",
                len(suggested) >= 1,
                f"suggested_endpoints={suggested!r}",
            )
        )
        # sveltekit: check role: read on suggested_endpoints entry
        if eval_name == "sveltekit-view-home":
            roles = [e.get("role", "") for e in suggested if isinstance(e, dict)]
            checks.append(
                Check(
                    "skill suggested_endpoints[] has entry with role: read",
                    "read" in roles,
                    f"roles found={roles!r}",
                )
            )

    # --- flows/ ---
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
        # v2: skills_used[] entries must NOT have a role field
        roles_in_skills_used = [u.get("role") for u in used if isinstance(u, dict) and "role" in u]
        checks.append(
            Check(
                "flow.skills_used[] entries have no `role` field",
                len(roles_in_skills_used) == 0,
                f"entries with role={roles_in_skills_used!r}" if roles_in_skills_used else "no role fields (good)",
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

    # --- endpoints/ ---
    endpoints = list_files(flow_map / "endpoints")
    checks.append(
        Check(
            "endpoints/ has exactly one file",
            len(endpoints) == 1,
            f"found {[e.name for e in endpoints]}",
        )
    )
    if endpoints:
        ep_fm, _ = split_frontmatter(endpoints[0])
        method = str(ep_fm.get("method", ""))
        path = str(ep_fm.get("path", ""))
        checks.append(
            Check(
                f"endpoint method ∈ {spec['endpoint_methods']}",
                method in spec["endpoint_methods"],
                f"method={method!r}",
            )
        )
        checks.append(
            Check(
                f"endpoint path contains `{spec['endpoint_path_substring']}`",
                spec["endpoint_path_substring"] in path,
                f"path={path!r}",
            )
        )
        if "endpoint_path_exact" in spec:
            checks.append(
                Check(
                    f"endpoint path == `{spec['endpoint_path_exact']}`",
                    path == spec["endpoint_path_exact"],
                    f"path={path!r}",
                )
            )
        if "endpoint_auth_expected" in spec:
            auth = str(ep_fm.get("auth", ""))
            allowed = spec.get("endpoint_auth_allowed") or {"bearer", "cookie", "none"}
            checks.append(
                Check(
                    f"endpoint auth ∈ {sorted(allowed)}",
                    auth in allowed,
                    f"auth={auth!r}",
                )
            )
            checks.append(
                Check(
                    f"endpoint auth == `{spec['endpoint_auth_expected']}`",
                    auth == spec["endpoint_auth_expected"],
                    f"auth={auth!r}",
                )
            )
        # used_by_skills[] must have at least one entry
        used_by = ep_fm.get("used_by_skills") or []
        checks.append(
            Check(
                "endpoint has at least one used_by_skills[] entry",
                len(used_by) >= 1,
                f"used_by_skills={used_by!r}",
            )
        )

    # --- absence of v1 artefacts ---
    checks.append(
        Check(
            "no `capabilities/` directory",
            not (flow_map / "capabilities").is_dir(),
            "capabilities/ absent (good)" if not (flow_map / "capabilities").is_dir() else "capabilities/ directory exists",
        )
    )
    checks.append(
        Check(
            "no `tools-proposed.json` file",
            not (flow_map / "tools-proposed.json").exists(),
            "tools-proposed.json absent (good)" if not (flow_map / "tools-proposed.json").exists() else "tools-proposed.json exists",
        )
    )

    # --- glossary ---
    checks.append(check_glossary_thin(flow_map / "glossary.md"))

    # --- round-trip ---
    checks.append(round_trip_skill_endpoint(flow_map / "skills", flow_map / "endpoints"))

    # --- HTTP-detail leakage ---
    checks.append(check_no_http_detail_leakage(flow_map / "flows", flow_map / "skills"))

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

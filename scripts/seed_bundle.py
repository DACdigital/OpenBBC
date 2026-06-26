#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.12"
# dependencies = [
#     "psycopg[binary]>=3.2",
#     "pyyaml>=6.0",
# ]
# ///
"""Seed an aikdm bundle YAML onto an existing agent_versions row.

Usage:
    DATABASE_URL=postgres://... \\
        uv run scripts/seed_bundle.py --version-id <uuid> --bundle path/to/bundle.yaml [--force]

Splits the bundle into:
    agents.architecture  ← tools, flows, skills metadata, external_actions, metadata
    agent_versions.prompts ← main_prompt + per-skill prompts

The split must match types.SplitBundle in open-bbcd (kept in lockstep).
Sets agents.finalized_at = now() on first land. Sets the version's status
to 'READY'.

Without --force, refuses to re-land when prompts have already been written
on this version (avoids accidental overwrites of edited prompts). With
--force, overwrites unconditionally (dev escape hatch).
"""

import argparse
import json
import os
import sys

import psycopg
import yaml


def split_bundle(bundle: dict) -> tuple[dict, dict]:
    """Mirror of internal/types/bundle.go SplitBundle.

    Architecture: tools, flows, skills_meta (name+description only),
    external_actions, metadata. Prompts: main_prompt + skill_prompts
    (skill name → prompt body).
    """
    skills = bundle.get("skills") or []
    skills_meta = [
        {"name": s.get("name", ""), "description": s.get("description", "")}
        for s in skills
    ]
    skill_prompts = {
        s["name"]: s.get("prompt", "")
        for s in skills
        if s.get("name") and s.get("prompt")
    }

    architecture = {
        "metadata": bundle.get("metadata") or {},
        "tools": bundle.get("tools") or [],
        "flows": bundle.get("flows") or [],
        "external_actions": bundle.get("external_actions") or [],
        "skills_meta": skills_meta,
    }
    prompts = {
        "main_prompt": bundle.get("main_prompt", ""),
        "skill_prompts": skill_prompts,
    }
    return architecture, prompts


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--version-id", required=True, help="UUID of the agent_versions row to seed")
    p.add_argument("--bundle", required=True, help="Path to bundle YAML file")
    p.add_argument("--force", action="store_true", help="Overwrite even when prompts already set")
    args = p.parse_args()

    dsn = os.environ.get("DATABASE_URL")
    if not dsn:
        print("error: DATABASE_URL not set", file=sys.stderr)
        return 2

    try:
        with open(args.bundle, "r") as f:
            data = yaml.safe_load(f)
    except FileNotFoundError:
        print(f"error: bundle file not found: {args.bundle}", file=sys.stderr)
        return 2
    except yaml.YAMLError as e:
        print(f"error: parse YAML: {e}", file=sys.stderr)
        return 2

    architecture, prompts = split_bundle(data)
    arch_json = json.dumps(architecture)
    prompts_json = json.dumps(prompts)

    try:
        with psycopg.connect(dsn) as conn, conn.cursor() as cur:
            # Resolve the version → agent and check whether prompts are
            # already populated. The check mirrors types.SplitBundle's
            # contract enforced by AgentVersionRepository.LandBundle in Go.
            cur.execute(
                "SELECT agent_id::text, "
                "(prompts IS NULL OR prompts::text = '{}'::jsonb::text) "
                "FROM agent_versions WHERE id = %s::uuid",
                (args.version_id,),
            )
            row = cur.fetchone()
            if not row:
                print("error: version not found", file=sys.stderr)
                return 1
            agent_id, prompts_empty = row
            if not prompts_empty and not args.force:
                print("error: prompts already set; use --force to overwrite", file=sys.stderr)
                return 1

            # Architecture is frozen on first land; finalized_at is stamped
            # iff null. Re-landing with --force re-stamps architecture and
            # leaves finalized_at unchanged (still the first-land time).
            cur.execute(
                "UPDATE agents SET architecture = %s::jsonb, "
                "finalized_at = COALESCE(finalized_at, now()) WHERE id = %s::uuid",
                (arch_json, agent_id),
            )
            cur.execute(
                "UPDATE agent_versions SET prompts = %s::jsonb, status = 'READY', "
                "updated_at = now() WHERE id = %s::uuid",
                (prompts_json, args.version_id),
            )
            conn.commit()
    except psycopg.OperationalError as e:
        print(f"error: database connection: {e}", file=sys.stderr)
        return 1

    print(
        f"✓ landed bundle on version {args.version_id} "
        f"(arch={len(arch_json)}B, prompts={len(prompts_json)}B), status=READY"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())

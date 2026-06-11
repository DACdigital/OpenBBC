#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.12"
# dependencies = [
#     "psycopg[binary]>=3.2",
#     "pyyaml>=6.0",
# ]
# ///
"""Seed a bundle YAML into agents.bundle JSONB.

Usage:
    DATABASE_URL=postgres://... \\
        uv run scripts/seed_bundle.py --agent-id <uuid> --bundle path/to/bundle.yaml [--force]

Without --force, refuses to overwrite a non-NULL bundle. With --force,
overwrites (dev escape hatch for when no chat sessions reference the row).
"""

import argparse
import json
import os
import sys

import psycopg
import yaml


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--agent-id", required=True, help="UUID of the agent (version row) to seed")
    p.add_argument("--bundle", required=True, help="Path to bundle YAML file")
    p.add_argument("--force", action="store_true", help="Overwrite a non-NULL bundle")
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

    payload = json.dumps(data)

    try:
        with psycopg.connect(dsn) as conn, conn.cursor() as cur:
            if args.force:
                cur.execute(
                    "UPDATE agents SET bundle = %s::jsonb, updated_at = now() WHERE id = %s::uuid",
                    (payload, args.agent_id),
                )
            else:
                cur.execute(
                    "UPDATE agents SET bundle = %s::jsonb, updated_at = now() "
                    "WHERE id = %s::uuid AND bundle IS NULL",
                    (payload, args.agent_id),
                )

            if cur.rowcount == 0:
                # Distinguish "agent doesn't exist" from "bundle already set".
                cur.execute(
                    "SELECT EXISTS(SELECT 1 FROM agents WHERE id = %s::uuid), "
                    "COALESCE((SELECT bundle IS NOT NULL FROM agents WHERE id = %s::uuid), false)",
                    (args.agent_id, args.agent_id),
                )
                exists, has_bundle = cur.fetchone()
                if not exists:
                    print("error: agent not found", file=sys.stderr)
                    return 1
                if has_bundle and not args.force:
                    print("error: bundle already set; use --force to overwrite", file=sys.stderr)
                    return 1
                print("error: update affected 0 rows", file=sys.stderr)
                return 1
            conn.commit()
    except psycopg.OperationalError as e:
        print(f"error: database connection: {e}", file=sys.stderr)
        return 1

    print(f"✓ bundle written for agent {args.agent_id} ({len(payload)} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())

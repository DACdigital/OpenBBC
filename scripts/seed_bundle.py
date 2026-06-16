#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.12"
# dependencies = [
#     "psycopg[binary]>=3.2",
#     "pyyaml>=6.0",
# ]
# ///
"""Seed a bundle YAML into agent_versions.bundle JSONB.

Usage:
    DATABASE_URL=postgres://... \\
        uv run scripts/seed_bundle.py --version-id <uuid> --bundle path/to/bundle.yaml [--force]

Without --force, refuses to overwrite a non-NULL bundle. With --force,
overwrites (dev escape hatch for when no chat sessions reference the version).
"""

import argparse
import json
import os
import sys

import psycopg
import yaml


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--version-id", required=True, help="UUID of the agent_versions row to seed")
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
            # Seeded bundles represent a hand-verified READY version, so we
            # also flip status to 'READY' in the same write.
            if args.force:
                cur.execute(
                    "UPDATE agent_versions SET bundle = %s::jsonb, status = 'READY', "
                    "updated_at = now() WHERE id = %s::uuid",
                    (payload, args.version_id),
                )
            else:
                cur.execute(
                    "UPDATE agent_versions SET bundle = %s::jsonb, status = 'READY', "
                    "updated_at = now() WHERE id = %s::uuid AND bundle IS NULL",
                    (payload, args.version_id),
                )

            if cur.rowcount == 0:
                # Distinguish "version doesn't exist" from "bundle already set".
                cur.execute(
                    "SELECT EXISTS(SELECT 1 FROM agent_versions WHERE id = %s::uuid), "
                    "COALESCE((SELECT bundle IS NOT NULL FROM agent_versions WHERE id = %s::uuid), false)",
                    (args.version_id, args.version_id),
                )
                exists, has_bundle = cur.fetchone()
                if not exists:
                    print("error: version not found", file=sys.stderr)
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

    print(f"✓ bundle written for version {args.version_id} ({len(payload)} bytes), status=READY")
    return 0


if __name__ == "__main__":
    sys.exit(main())

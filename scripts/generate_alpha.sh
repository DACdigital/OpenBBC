#!/usr/bin/env bash
# Generate an alpha bundle for a given agent version and land it.
#
# Given a version_id, this script:
#   1. GET  /agent_versions/{version_id}/config.yaml       (fetch FlowMapConfig)
#   2. uv run aikdm generate-agent --config … --output …   (LLM run, ~30-60s)
#   3. uv run scripts/seed_bundle.py --version-id … --bundle …
#      (split into agents.architecture + agent_versions.prompts, status → READY,
#       stamps agents.finalized_at on first land)
#
# Usage:
#   scripts/generate_alpha.sh <version_id> [--force] [--keep]
#
#   --force  pass through to seed_bundle.py — overwrite prompts even if
#            already populated on this version (dev escape hatch).
#   --keep   keep the tempdir with config.yaml + bundle.yaml for inspection.
#
# Env:
#   OPENBBCD_URL       base URL of the open-bbcd server (default http://localhost:8080)
#   DATABASE_URL       Postgres DSN — required by seed_bundle.py
#   AIKDM_MODEL_GENERATOR / AIKDM_MODEL_CRITIC  models (default claude-haiku-4-5)
#   <PROVIDER>_API_KEY the key of each of those models' providers
#                      (ANTHROPIC_API_KEY, OPENAI_API_KEY or GEMINI_API_KEY)
#                      This script checks only the generator/critic keys, but
#                      aikdm's load_settings still requires keys for all six
#                      roles: set every AIKDM_MODEL_* for a non-Anthropic setup.
#   OPENBBC_LLM_REQUEST_EXTRAS  optional per-model extra headers/body, read by aikdm
#
# open-bbcd/.env and aikdm/.env are auto-sourced if present.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

usage() {
    echo "usage: scripts/generate_alpha.sh <version_id> [--force] [--keep]" >&2
    exit 2
}

VERSION_ID="${1:-}"
[ -z "$VERSION_ID" ] && usage
shift

FORCE=""
KEEP=false
while [ $# -gt 0 ]; do
    case "$1" in
        --force) FORCE="--force" ;;
        --keep)  KEEP=true ;;
        *) usage ;;
    esac
    shift
done

# Env files: open-bbcd/.env for DATABASE_URL, aikdm/.env for the model keys.
[ -f "$ROOT/open-bbcd/.env" ] && set -a && . "$ROOT/open-bbcd/.env" && set +a
[ -f "$ROOT/aikdm/.env" ]     && set -a && . "$ROOT/aikdm/.env"     && set +a

# provider_key_env <model>: the API-key variable of the model's provider, with
# the same inference as aikdm's config._provider_of (aikdm's load_settings
# still checks every role and fails fast).
provider_key_env() {
    local model="$1" provider
    case "$model" in
        */*)          provider="${model%%/*}" ;;
        claude*)      provider=anthropic ;;
        gpt*|openai*) provider=openai ;;
        gemini*)      provider=gemini ;;
        *) echo "error: cannot infer provider from model name: $model" >&2; return 1 ;;
    esac
    case "$provider" in
        anthropic) echo ANTHROPIC_API_KEY ;;
        openai)    echo OPENAI_API_KEY ;;
        gemini)    echo GEMINI_API_KEY ;;
        *) echo "error: unknown provider '$provider' for model $model" >&2; return 1 ;;
    esac
}

OPENBBCD_URL="${OPENBBCD_URL:-http://localhost:8080}"
: "${DATABASE_URL:?DATABASE_URL not set}"
for model in "${AIKDM_MODEL_GENERATOR:-claude-haiku-4-5}" "${AIKDM_MODEL_CRITIC:-claude-haiku-4-5}"; do
    key_env="$(provider_key_env "$model")" || exit 2
    if [ -z "${!key_env:-}" ]; then
        echo "error: $key_env not set (needed for model $model)" >&2
        exit 2
    fi
done

WORK="$(mktemp -d -t generate-alpha-XXXXXX)"
if [ "$KEEP" = true ]; then
    trap 'echo "workdir kept at: $WORK"' EXIT
else
    trap 'rm -rf "$WORK"' EXIT
fi

CONFIG="$WORK/config.yaml"
BUNDLE="$WORK/bundle.yaml"

echo ">>> Fetching config.yaml for version $VERSION_ID"
if ! curl -sSf "$OPENBBCD_URL/agent_versions/$VERSION_ID/config.yaml" -o "$CONFIG"; then
    echo "error: could not download config.yaml (is $OPENBBCD_URL up? does the version exist?)" >&2
    exit 1
fi
echo "    saved $(wc -l < "$CONFIG") lines"

echo ">>> Running aikdm generate-agent"
( cd "$ROOT/aikdm" && uv run aikdm generate-agent --config "$CONFIG" --output "$BUNDLE" )
echo "    bundle: $BUNDLE ($(wc -l < "$BUNDLE") lines)"

echo ">>> Landing bundle onto version $VERSION_ID"
( cd "$ROOT" && DATABASE_URL="$DATABASE_URL" \
  uv run scripts/seed_bundle.py --version-id "$VERSION_ID" --bundle "$BUNDLE" $FORCE )

echo ""
echo "✓ alpha ready — $OPENBBCD_URL/agent_versions/$VERSION_ID/configure/prompts"

#!/usr/bin/env bash
# Usage: scripts/run_eval.sh <eval_id>
# Env:   OPENBBCD_URL (required), aikdm LLM keys (as needed by aikdm)
set -euo pipefail
eval_id="${1:?usage: run_eval.sh <eval_id>}"
: "${OPENBBCD_URL:?set OPENBBCD_URL}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
input="$work/input.yaml"
result="$work/result.json"

echo "→ fetching eval-input.yaml"
curl -fsSL "$OPENBBCD_URL/evals/$eval_id/export.yaml" -o "$input"
echo "→ marking eval IN_PROGRESS"
curl -fsSL -X POST "$OPENBBCD_URL/evals/$eval_id/start"

echo "→ running aikdm evaluate"
if (cd aikdm && uv run aikdm evaluate --input "$input" --output "$result"); then
    echo "→ uploading result"
    curl -fsSL -X POST -H 'Content-Type: application/json' \
        --data-binary "@$result" "$OPENBBCD_URL/evals/$eval_id/result"
    echo "done"
else
    echo "aikdm failed — marking eval FAILED" >&2
    curl -fsSL -X POST -H 'Content-Type: application/json' \
        --data '{"error_message":"aikdm exited non-zero (see stderr)"}' \
        "$OPENBBCD_URL/evals/$eval_id/fail"
    exit 1
fi

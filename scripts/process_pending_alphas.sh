#!/usr/bin/env bash
# Usage: scripts/process_pending_alphas.sh
# Env:   OPENBBCD_URL (required), plus whatever generate_alpha.sh needs
#        (DATABASE_URL for seed_bundle.py, ANTHROPIC_API_KEY for aikdm).
#
# Drains all PENDING agent versions via GET /agent_versions.json?status=PENDING
# and delegates each to scripts/generate_alpha.sh, which fetches config.yaml,
# runs `aikdm generate-agent`, and lands the bundle via seed_bundle.py
# (transitioning the version PENDING → READY). Serial; continue-on-error;
# flock-protected so overlapping cron invocations don't stack.
#
# Suggested cron:
#   */5 * * * *  OPENBBCD_URL=http://localhost:8080 /path/to/repo/scripts/process_pending_alphas.sh
#
# Exit codes:
#   0  success (incl. "no PENDING alphas")
#   1  at least one per-item failure
#   2  infra/config error (server unreachable, missing env, malformed JSON)

set -euo pipefail

: "${OPENBBCD_URL:?set OPENBBCD_URL (e.g. http://localhost:8080)}"

LOCK=/tmp/openbbc-process-alphas.lock
exec 9>"$LOCK"
if ! flock -n 9; then
    exit 0
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

ts() { date -Iseconds; }

echo "$(ts) → listing PENDING agent versions"
list_json=$(curl -fsSL "$OPENBBCD_URL/agent_versions.json?status=PENDING&limit=100")

ids=$(echo "$list_json" | python3 -c 'import json,sys; print("\n".join(v["id"] for v in json.load(sys.stdin)))')

if [ -z "$ids" ]; then
    echo "$(ts) no PENDING alphas"
    exit 0
fi

count=0
fails=0
while IFS= read -r version_id; do
    [ -z "$version_id" ] && continue
    count=$((count + 1))
    echo "$(ts) → generating alpha for version $version_id"
    if OPENBBCD_URL="$OPENBBCD_URL" "$root/scripts/generate_alpha.sh" "$version_id"; then
        echo "$(ts)   ↳ ok"
    else
        rc=$?
        fails=$((fails + 1))
        echo "$(ts)   ↳ FAILED (exit $rc)"
    fi
done <<< "$ids"

echo "$(ts) processed $count alphas, $fails failures"
if [ "$fails" -gt 0 ]; then
    exit 1
fi
exit 0

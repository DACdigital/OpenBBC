#!/usr/bin/env bash
# Usage: scripts/process_pending_evals.sh
# Env:   OPENBBCD_URL (required), aikdm LLM keys (as needed by scripts/run_eval.sh)
#
# Drains all PENDING evals via GET /evals.json?status=PENDING and delegates each
# to scripts/run_eval.sh. Serial; continue-on-error; flock-protected so multiple
# cron invocations don't stack.
#
# Suggested cron:
#   */10 * * * *  OPENBBCD_URL=http://localhost:8080 /path/to/repo/scripts/process_pending_evals.sh
#
# Exit codes:
#   0  success (incl. "no PENDING evals")
#   1  at least one per-item failure
#   2  infra/config error (server unreachable, missing env, malformed JSON)

set -euo pipefail

: "${OPENBBCD_URL:?set OPENBBCD_URL (e.g. http://localhost:8080)}"

LOCK=/tmp/openbbc-process-evals.lock
exec 9>"$LOCK"
if ! flock -n 9; then
    exit 0
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

ts() { date -Iseconds; }

echo "$(ts) → listing PENDING evals"
list_json=$(curl -fsSL "$OPENBBCD_URL/evals.json?status=PENDING&limit=100")

ids=$(echo "$list_json" | python3 -c 'import json,sys; print("\n".join(e["id"] for e in json.load(sys.stdin)))')

if [ -z "$ids" ]; then
    echo "$(ts) no PENDING evals"
    exit 0
fi

count=0
fails=0
while IFS= read -r eval_id; do
    [ -z "$eval_id" ] && continue
    count=$((count + 1))
    echo "$(ts) → processing eval $eval_id"
    if OPENBBCD_URL="$OPENBBCD_URL" "$root/scripts/run_eval.sh" "$eval_id"; then
        echo "$(ts)   ↳ ok"
    else
        rc=$?
        fails=$((fails + 1))
        echo "$(ts)   ↳ FAILED (exit $rc)"
    fi
done <<< "$ids"

echo "$(ts) processed $count evals, $fails failures"
if [ "$fails" -gt 0 ]; then
    exit 1
fi
exit 0

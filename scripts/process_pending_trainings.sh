#!/usr/bin/env bash
# Usage: scripts/process_pending_trainings.sh
# Env:   OPENBBCD_URL (required), aikdm LLM keys (as needed by scripts/train_from_session.sh)
#
# Drains all PENDING training sessions via GET /training-sessions.json?status=PENDING
# and delegates each to scripts/train_from_session.sh --yes. Serial; continue-on-error;
# flock-protected so multiple cron invocations don't stack.
#
# Suggested cron (trainings are minutes-per-epoch — schedule less aggressively than evals):
#   */15 * * * *  OPENBBCD_URL=http://localhost:8080 /path/to/repo/scripts/process_pending_trainings.sh
#
# Exit codes:
#   0  success (incl. "no PENDING sessions")
#   1  at least one per-item failure
#   2  infra/config error (server unreachable, missing env, malformed JSON)

set -euo pipefail

: "${OPENBBCD_URL:?set OPENBBCD_URL (e.g. http://localhost:8080)}"

LOCK=/tmp/openbbc-process-trainings.lock
exec 9>"$LOCK"
if ! flock -n 9; then
    exit 0
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

ts() { date -Iseconds; }

echo "$(ts) → listing PENDING training sessions"
list_json=$(curl -fsSL "$OPENBBCD_URL/training-sessions.json?status=PENDING&limit=100")

ids=$(echo "$list_json" | python3 -c 'import json,sys; print("\n".join(s["id"] for s in json.load(sys.stdin)))')

if [ -z "$ids" ]; then
    echo "$(ts) no PENDING training sessions"
    exit 0
fi

count=0
fails=0
while IFS= read -r session_id; do
    [ -z "$session_id" ] && continue
    count=$((count + 1))
    echo "$(ts) → processing training session $session_id"
    if OPENBBCD_URL="$OPENBBCD_URL" "$root/scripts/train_from_session.sh" --yes "$session_id"; then
        echo "$(ts)   ↳ ok"
    else
        rc=$?
        fails=$((fails + 1))
        echo "$(ts)   ↳ FAILED (exit $rc)"
    fi
done <<< "$ids"

echo "$(ts) processed $count training sessions, $fails failures"
if [ "$fails" -gt 0 ]; then
    exit 1
fi
exit 0

#!/usr/bin/env bash
# Usage:   scripts/train_from_session.sh <session_id> [--epochs N] [--patience K]
# Env:     OPENBBCD_URL (required, e.g. http://localhost:8080)
#          Anthropic/OpenAI/Gemini keys per aikdm's model settings.
#
# Picks up a PENDING training session and drives it through IN_PROGRESS → DONE
# (or FAILED). Refuses if the session isn't PENDING at start.
#
# Flow:
#   1. GET  /training-sessions/{id}/json           (fetch inputs)
#   2. GET  /evals/{source_eval_id}/export.yaml    (fetch eval-input.yaml)
#   3. POST /training-sessions/{id}/start          (→ IN_PROGRESS)
#   4. uv run aikdm train-agent --input ... --out ...
#   5. Print score-diff summary, ask y/N
#   6. YES: POST /training-sessions/{id}/complete  (→ DONE, creates version)
#      NO:  POST /training-sessions/{id}/fail       (→ FAILED with reason)

set -euo pipefail

usage() {
    echo "usage: scripts/train_from_session.sh <session_id> [--epochs N] [--patience K]" >&2
    exit 2
}

session_id="${1:-}"
[ -z "$session_id" ] && usage
shift

epochs=5
patience=3
while [[ $# -gt 0 ]]; do
    case "$1" in
        --epochs)   epochs="${2:?}"; shift 2;;
        --patience) patience="${2:?}"; shift 2;;
        *) echo "unknown flag: $1" >&2; usage;;
    esac
done

: "${OPENBBCD_URL:?set OPENBBCD_URL (e.g. http://localhost:8080)}"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d -t train_from_session.XXXXXX)"
input="$work/input.yaml"
out_dir="$work/out"

echo "→ fetching training session $session_id"
session_json=$(curl -fsSL -H 'Accept: application/json' "$OPENBBCD_URL/training-sessions/$session_id/json")

status=$(cd "$root/aikdm" && uv run python -c "import json,sys;print(json.loads(sys.argv[1])['status'])" "$session_json")
if [ "$status" != "PENDING" ]; then
    echo "session is $status — refusing to run (only PENDING sessions can be picked up)" >&2
    exit 2
fi
source_eval_id=$(cd "$root/aikdm" && uv run python -c "import json,sys;print(json.loads(sys.argv[1])['source_eval_id'])" "$session_json")

echo "→ fetching eval-input.yaml for source eval $source_eval_id"
curl -fsSL "$OPENBBCD_URL/evals/$source_eval_id/export.yaml" -o "$input"

echo "→ marking session IN_PROGRESS (epochs=$epochs, patience=$patience)"
curl -fsSL -X POST -H 'Content-Type: application/json' \
    --data "{\"epochs\":$epochs,\"patience\":$patience}" \
    "$OPENBBCD_URL/training-sessions/$session_id/start" >/dev/null

fail_session() {
    local reason="$1"
    echo "→ marking session FAILED: $reason" >&2
    curl -fsSL -X POST -H 'Content-Type: application/json' \
        --data "{\"error_message\":$(printf '%s' "$reason" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')}" \
        "$OPENBBCD_URL/training-sessions/$session_id/fail" >/dev/null || true
}

echo "→ running aikdm train-agent"
if ! (cd "$root/aikdm" && uv run aikdm train-agent \
        --input "$input" --epochs "$epochs" --patience "$patience" --out "$out_dir"); then
    fail_session "aikdm exited non-zero (see stderr)"
    exit 1
fi

echo
echo "=== training summary ==="
cd "$root/aikdm" && uv run python <<PY
import json
r = json.load(open("$out_dir/training-report.json"))
delta = r['final_score'] - r['initial_score']
sign = '+' if delta >= 0 else ''
print(f"initial score : {r['initial_score']:.4f}")
print(f"final score   : {r['final_score']:.4f}  ({sign}{delta:.4f})")
print(f"epochs run    : {r['total_epochs_run']}  (stopped: {r['stopped_reason']})")
promoted = sum(1 for e in r['epochs'] if e['promoted'])
print(f"promoted      : {promoted}/{r['total_epochs_run']} epochs")
print()
print("per-epoch:")
for e in r['epochs']:
    if e['promoted']:
        tag = 'PROMOTED'
    elif e.get('error'):
        tag = 'ERROR   '
    else:
        tag = 'discarded'
    print(f"  epoch {e['epoch']}: {e['baseline_score']:.4f} -> {e['candidate_score']:.4f}  {tag}")
    if e.get('error'):
        print(f"     error: {e['error']}")
PY
cd - >/dev/null

echo
echo "outputs remain at: $out_dir"
echo

read -r -p "commit this training session (creates a new READY agent version)? [y/N] " ans
case "$ans" in
    y|Y|yes|YES) ;;
    *)
        fail_session "operator declined at y/N prompt"
        exit 0
        ;;
esac

echo "→ completing session + creating READY agent version"
result=$(
    cd "$root/aikdm" && uv run python <<PY
import json, sys, yaml, urllib.request, urllib.error

with open("$out_dir/bundle.yaml") as f:
    bundle = yaml.safe_load(f)
with open("$out_dir/training-report.json") as f:
    report = json.load(f)

body = json.dumps({"bundle": bundle, "training_report": report}).encode("utf-8")
url = "$OPENBBCD_URL/training-sessions/$session_id/complete"
req = urllib.request.Request(url, data=body, method="POST",
    headers={"Content-Type": "application/json"})
try:
    with urllib.request.urlopen(req, timeout=60) as resp:
        payload = json.loads(resp.read())
        print(payload.get("session_url","") + " ← " + payload.get("new_version_id",""))
except urllib.error.HTTPError as e:
    print(f"POST /complete failed: {e.code} {e.reason}", file=sys.stderr)
    print(e.read().decode("utf-8", errors="replace"), file=sys.stderr)
    sys.exit(1)
PY
)

echo "→ done: $OPENBBCD_URL$result"

#!/usr/bin/env bash
# Usage:   scripts/train_from_eval.sh <eval_id> [--epochs N] [--patience K]
# Env:     OPENBBCD_URL (required)
#          Anthropic/OpenAI/Gemini keys as required by aikdm's model settings.
#
# End-to-end training flow:
#   1. Fetch eval-input.yaml from openbbcd for the given eval_id.
#   2. Run `aikdm train-agent` (N epochs, K patience).
#   3. Print initial→final score delta + per-epoch summary.
#   4. Prompt: create a new agent version from the trained bundle? y/N.
#   5. On yes: POST the bundle's prompts to
#         POST /agent_versions/{parent}/configure/prompts
#      which forks a new DRAFT version chained via parent_version_id
#      and copies MCP attachments forward. Prints the new version URL.
#
# Bundle + report are left in the temp dir path printed at the end so
# you can re-inspect them.

set -euo pipefail

usage() {
    echo "usage: scripts/train_from_eval.sh <eval_id> [--epochs N] [--patience K]" >&2
    exit 2
}

eval_id="${1:-}"
[ -z "$eval_id" ] && usage
shift

epochs=5
patience=3
while [[ $# -gt 0 ]]; do
    case "$1" in
        --epochs)   epochs="${2:?}";   shift 2;;
        --patience) patience="${2:?}"; shift 2;;
        *) echo "unknown flag: $1" >&2; usage;;
    esac
done

: "${OPENBBCD_URL:?set OPENBBCD_URL (e.g. http://localhost:8080)}"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d -t train_from_eval.XXXXXX)"
input="$work/input.yaml"
out_dir="$work/out"

echo "→ fetching eval-input.yaml for eval $eval_id"
curl -fsSL "$OPENBBCD_URL/evals/$eval_id/export.yaml" -o "$input"

parent_version_id=$(
    cd "$root/aikdm" && uv run python -c "
import sys, yaml
d = yaml.safe_load(open('$input'))
print(d['agent_version']['id'])
"
)
echo "→ parent agent_version_id: $parent_version_id"

echo "→ running aikdm train-agent (epochs=$epochs patience=$patience)"
(cd "$root/aikdm" && uv run aikdm train-agent \
    --input "$input" \
    --epochs "$epochs" --patience "$patience" \
    --out "$out_dir")

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
    elif e['error']:
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
echo "  bundle:  $out_dir/bundle.yaml"
echo "  report:  $out_dir/training-report.json  (also .csv)"
echo

read -r -p "create a new agent version in openbbcd from this trained bundle? [y/N] " ans
case "$ans" in
    y|Y|yes|YES) ;;
    *) echo "skipped."; exit 0;;
esac

echo "→ posting bundle prompts to openbbcd (forks new version from $parent_version_id)"
new_url=$(
    cd "$root/aikdm" && uv run python <<PY
import sys, yaml, urllib.parse, urllib.request, urllib.error

with open("$out_dir/bundle.yaml") as f:
    b = yaml.safe_load(f)

form = [("main_prompt", b.get("main_prompt", "") or "")]
for skill in b.get("skills") or []:
    name = skill.get("name")
    prompt = skill.get("prompt", "") or ""
    if name:
        form.append((f"skill_prompt[{name}]", prompt))

body = urllib.parse.urlencode(form).encode("utf-8")
url = "$OPENBBCD_URL/agent_versions/$parent_version_id/configure/prompts"

# openbbcd returns 303 → we want the Location, not the followed page.
class Grab(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise urllib.error.HTTPError(req.full_url, code, "captured", headers, fp)

opener = urllib.request.build_opener(Grab)
req = urllib.request.Request(url, data=body, method="POST",
    headers={"Content-Type": "application/x-www-form-urlencoded"})
try:
    opener.open(req, timeout=30)
    print("ERR: openbbcd did not return a redirect", file=sys.stderr)
    sys.exit(1)
except urllib.error.HTTPError as e:
    if e.code in (301, 302, 303, 307, 308):
        loc = e.headers.get("Location", "")
        # Location is a path like /agent_versions/<id>/configure/prompts.
        if loc.startswith("/"):
            loc = "$OPENBBCD_URL" + loc
        print(loc)
    else:
        print(f"POST failed: {e.code} {e.reason}", file=sys.stderr)
        print(e.read().decode("utf-8", errors="replace"), file=sys.stderr)
        sys.exit(1)
PY
)

echo "new version: $new_url"

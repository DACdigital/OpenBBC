#!/usr/bin/env bash
# E2E test prep: take a discovery zip, push it through the wizard,
# generate an aikdm bundle from the served config.yaml, seed the
# bundle onto the version row, and print the agent URL.
#
# Usage:
#   scripts/e2e_seed_agent.sh [zip_path]
#
#   zip_path defaults to .test-project/frontend/flow-map.zip.
#
# Required env (sourced from open-bbcd/.env + aikdm/.env if present):
#   DATABASE_URL        — Postgres DSN for open-bbcd
#   ANTHROPIC_API_KEY   — for aikdm generate
#
# Assumes open-bbcd is already running on $BBCD_URL (default http://localhost:8080).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ZIP="${1:-$ROOT/.test-project/frontend/flow-map.zip}"
BBCD_URL="${BBCD_URL:-http://localhost:8080}"

# Source env files for DATABASE_URL + ANTHROPIC_API_KEY.
[ -f "$ROOT/open-bbcd/.env" ] && set -a && . "$ROOT/open-bbcd/.env" && set +a
[ -f "$ROOT/aikdm/.env" ]     && set -a && . "$ROOT/aikdm/.env"     && set +a
: "${DATABASE_URL:?DATABASE_URL not set}"
: "${ANTHROPIC_API_KEY:?ANTHROPIC_API_KEY not set}"

[ -f "$ZIP" ] || { echo "zip not found: $ZIP" >&2; exit 2; }

WORK="$(mktemp -d -t e2e-seed-XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

# Unique-ish suffix so reruns don't collide on agents.name UNIQUE.
SUFFIX="$(date +%s | tail -c 5)"
NAME="minierp-t${SUFFIX}"

echo ">>> Submitting wizard: agent '$NAME' from $ZIP"
LOC=$(curl -sS -i \
  -F "name=$NAME" \
  -F "scope=Help internal staff explore the mini ERP: read orders, customers, products, inventory; never change financial data." \
  -F "should_do=Answer questions about orders, customers, products, and inventory by calling the ERP backend; summarise clearly." \
  -F "should_not_do=Never delete or modify financial records; never invent data; never assume identity of another user." \
  -F "business_domain=Mini ERP: small-business order/customer/product/inventory management. Internal staff users only." \
  -F "discovery_file=@${ZIP};type=application/zip" \
  "$BBCD_URL/agents/wizard" \
  | grep -i '^Location:' | tr -d '\r')

VERSION_ID="$(echo "$LOC" | sed -E 's|.*/agent_versions/([^/]+)/configure.*|\1|')"
[ -n "$VERSION_ID" ] || { echo "could not extract version id from: $LOC" >&2; exit 1; }
echo "    version_id=$VERSION_ID"

# Get agent id via DB lookup (wizard response only carries version).
AGENT_ID=$(psql "$DATABASE_URL" -At -c \
  "SELECT agent_id FROM agent_versions WHERE id = '$VERSION_ID'::uuid")
[ -n "$AGENT_ID" ] || { echo "no agent_id found for version $VERSION_ID" >&2; exit 1; }
echo "    agent_id=$AGENT_ID"

echo ">>> Downloading config.yaml"
CONFIG="$WORK/config.yaml"
curl -sSf "$BBCD_URL/agent_versions/$VERSION_ID/config.yaml" -o "$CONFIG"
echo "    saved $(wc -l < "$CONFIG") lines"

echo ">>> Running aikdm generate-agent (this calls the LLM, ~30-60s)"
BUNDLE="$WORK/bundle.yaml"
( cd "$ROOT/aikdm" && uv run aikdm generate-agent --config "$CONFIG" --output "$BUNDLE" )
echo "    bundle: $BUNDLE ($(wc -l < "$BUNDLE") lines)"

echo ">>> Seeding bundle onto version $VERSION_ID"
( cd "$ROOT" && DATABASE_URL="$DATABASE_URL" \
  uv run scripts/seed_bundle.py --version-id "$VERSION_ID" --bundle "$BUNDLE" --force )

# Create an HTTP-endpoint backend with a bearer default header, then wire
# every one of the seeded agent's endpoints to it. Bearer token is a dev
# placeholder; override BACKEND_BASE_URL / BACKEND_BEARER_TOKEN via env.
BACKEND_BASE_URL="${BACKEND_BASE_URL:-http://localhost:3001}"
BACKEND_BEARER_TOKEN="${BACKEND_BEARER_TOKEN:-tok_test_abc123}"
BACKEND_NAME="mcp-http-t${SUFFIX}"

echo ">>> Creating HTTP-endpoint backend '$BACKEND_NAME'"
# handler uses r.ParseForm() (urlencoded), not r.ParseMultipartForm; --data-urlencode required
curl -sSf -o /dev/null \
  --data-urlencode "name=$BACKEND_NAME" \
  --data-urlencode "kind=http_endpoint" \
  --data-urlencode "base_url=$BACKEND_BASE_URL" \
  --data-urlencode "default_headers_key=Authorization" \
  --data-urlencode "default_headers_value=Bearer $BACKEND_BEARER_TOKEN" \
  "$BBCD_URL/mcp"

BACKEND_ID=$(psql "$DATABASE_URL" -At -c \
  "SELECT id FROM tool_backends WHERE name = '$BACKEND_NAME'")
[ -n "$BACKEND_ID" ] || { echo "backend '$BACKEND_NAME' not found after create" >&2; exit 1; }
echo "    backend_id=$BACKEND_ID (base_url=$BACKEND_BASE_URL, auth=bearer)"

echo ">>> Wiring every endpoint of agent $AGENT_ID to $BACKEND_NAME"
curl -sSf -o /dev/null \
  --data-urlencode "backend_id=$BACKEND_ID" \
  "$BBCD_URL/agents/$AGENT_ID/configure/architecture/endpoints/bulk"

WIRED_COUNT=$(psql "$DATABASE_URL" -At -c \
  "SELECT COUNT(*) FROM agent_endpoint_backend WHERE agent_id = '$AGENT_ID'::uuid AND backend_id = '$BACKEND_ID'::uuid")
echo "    $WIRED_COUNT endpoints wired"

echo ""
echo "=========================================="
echo " Ready. Agent URL:"
echo "   $BBCD_URL/agents/ui?agent=$AGENT_ID"
echo ""
echo " Version configurator:"
echo "   $BBCD_URL/agent_versions/$VERSION_ID/configure/architecture/flows"
echo ""
echo " Backend wired:"
echo "   $BBCD_URL/mcp/$BACKEND_ID  ($BACKEND_NAME → $BACKEND_BASE_URL, bearer token)"
echo ""
echo " Workdir kept at: $WORK (config.yaml + bundle.yaml for inspection)"
echo "   (auto-cleaned on script exit — rerun without trap if you want to keep)"
echo "=========================================="
trap - EXIT  # keep the workdir for the user

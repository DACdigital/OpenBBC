#!/usr/bin/env bash
# Render tests for the declarative-seed Helm wiring. Needs `helm` on PATH.
set -uo pipefail

chart="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
release=t

cat >"$tmp/agents.yaml" <<'YAML'
format: openbbc-seed-v1
agents: []
YAML

fail=0
check() { # check <desc> <present|absent> <pattern> <file>
  local desc=$1 want=$2 pat=$3 file=$4
  if grep -qE -- "$pat" "$file"; then got=present; else got=absent; fi
  if [ "$got" = "$want" ]; then echo "ok   $desc"; else echo "FAIL $desc (expected $want: $pat)"; fail=1; fi
}

helm template "$release" "$chart" --set seed.enabled=true \
  --set-file 'seed.files.agents\.yaml'="$tmp/agents.yaml" >"$tmp/on.yaml" 2>"$tmp/on.err" \
  || { echo "FAIL helm template (enabled)"; cat "$tmp/on.err"; exit 1; }
helm template "$release" "$chart" >"$tmp/off.yaml" 2>"$tmp/off.err" \
  || { echo "FAIL helm template (disabled)"; cat "$tmp/off.err"; exit 1; }

# fullname for release "t" is "t-openbbc"; the seed ConfigMap is "<fullname>-seed".
check "enabled: seed ConfigMap"      present '^  name: t-openbbc-seed$' "$tmp/on.yaml"
check "enabled: ConfigMap file key"  present '^  "agents\.yaml": "' "$tmp/on.yaml"
check "enabled: mountPath"           present 'mountPath: /etc/openbbc/seed' "$tmp/on.yaml"
check "enabled: readOnly"            present 'readOnly: true' "$tmp/on.yaml"
check "enabled: SEED_PATH"           present 'name: SEED_PATH' "$tmp/on.yaml"
check "enabled: checksum/seed"       present 'checksum/seed:' "$tmp/on.yaml"
check "enabled: startupProbe"        present 'startupProbe:' "$tmp/on.yaml"

check "disabled: no seed ConfigMap"  absent  't-openbbc-seed' "$tmp/off.yaml"
check "disabled: no mountPath"       absent  '/etc/openbbc/seed' "$tmp/off.yaml"
check "disabled: no readOnly"        absent  'readOnly: true' "$tmp/off.yaml"
check "disabled: no SEED_PATH"       absent  'SEED_PATH' "$tmp/off.yaml"
check "disabled: no checksum/seed"   absent  'checksum/seed' "$tmp/off.yaml"
check "disabled: startupProbe kept"  present 'startupProbe:' "$tmp/off.yaml"

# existingConfigMap: no chart-owned ConfigMap, mounts the named one.
helm template "$release" "$chart" --set seed.enabled=true --set seed.existingConfigMap=mine >"$tmp/ex.yaml" 2>&1 \
  || { echo "FAIL helm template (existing)"; cat "$tmp/ex.yaml"; exit 1; }
check "existing: no chart ConfigMap" absent  't-openbbc-seed' "$tmp/ex.yaml"
check "existing: mounts named map"   present '^ +name: mine$' "$tmp/ex.yaml"
check "existing: SEED_PATH"          present 'name: SEED_PATH' "$tmp/ex.yaml"
check "existing: no checksum/seed"   absent  'checksum/seed' "$tmp/ex.yaml"

exit $fail

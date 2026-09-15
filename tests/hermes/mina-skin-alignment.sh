#!/usr/bin/env bash
set -euo pipefail
umask 077

repo=$(cd "$(dirname "$0")/../.." && pwd)
source=${HERMES_SOURCE_DIR:?Set HERMES_SOURCE_DIR to the locked Hermes source}
python=${HERMES_TEST_PYTHON:-python3}
mkdir -p "$repo/.loom-acceptance"
root=$(mktemp -d "$repo/.loom-acceptance/mina-skin-alignment.XXXXXX")
printf 'Acceptance root: %s\n' "$root"
mkdir -p "$root/hermes/apps"
cp "$source/package.json" "$source/package-lock.json" "$root/hermes/"
cp -R "$source/ui-tui" "$root/hermes/"
cp -R "$source/apps/shared" "$root/hermes/apps/"
chmod -R u+w "$root/hermes"
cd "$root/hermes"
cp ui-tui/src/components/branding.tsx ui-tui/src/components/branding-before-loom.tsx
patch -p1 --batch --fuzz=0 -i "$repo/nix/patches/hermes-centered-skin-banner.patch"
npm ci --ignore-scripts --no-audit --no-fund \
  --workspace ui-tui --workspace ui-tui/packages/hermes-ink --workspace apps/shared
cp "$repo/tests/hermes/mina-skin-alignment.test.ts" ui-tui/src/__tests__/
"$python" - "$repo" > "$root/skins.json" <<'PY'
import json, pathlib, sys, yaml
base = pathlib.Path(sys.argv[1]) / "nix/files/mina/skins"
print(json.dumps([yaml.safe_load((base / ("mina-matrix-teal-" + n + ".yaml")).read_text())
                  for n in ["4", "5"]]))
PY
cd ui-tui
npm run build:ink
LOOM_ALIGNMENT_SKINS="$root/skins.json" LOOM_ALIGNMENT_FRAMES="$root" \
  ../node_modules/.bin/vitest run src/__tests__/mina-skin-alignment.test.ts \
  src/__tests__/brandingMcpCount.test.ts
node scripts/build.mjs
printf 'PASS: native skin alignment, legacy MCP banner, TUI bundle; evidence %s\n' "$root"

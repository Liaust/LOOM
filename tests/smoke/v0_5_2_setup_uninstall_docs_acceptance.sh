#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

DOC="$REPO_ROOT/docs/Uninstall And Decommission.md"
INSTALLER="$REPO_ROOT/docs/installer.md"

[[ -f "$DOC" ]] || {
  printf 'v0.5.2 setup uninstall docs smoke: missing %s\n' "$DOC" >&2
  exit 1
}

for needle in \
  "Disable Only" \
  "Preserve Data" \
  "Non-Main Decommission" \
  "Purge" \
  "--allow-production-main" \
  "--confirm-node" \
  "--backup-ref"
do
  grep -F -- "$needle" "$DOC" >/dev/null || {
    printf 'v0.5.2 setup uninstall docs smoke: missing %s\n' "$needle" >&2
    exit 1
  }
done

grep -F -- "docs/Uninstall And Decommission.md" "$INSTALLER" >/dev/null || {
  printf 'v0.5.2 setup uninstall docs smoke: installer docs do not link uninstall docs\n' >&2
  exit 1
}

printf '[smoke] v0.5.2 setup uninstall docs acceptance passed\n'

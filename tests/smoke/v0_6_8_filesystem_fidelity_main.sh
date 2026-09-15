#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

main_documents_root="${LOOM_MAIN_DOCUMENTS_ROOT:-/var/lib/loom/main-documents}"
target="${LOOM_ACCEPTANCE_ROOT:-$main_documents_root/.loom-acceptance/v0.6.8-filesystem-fidelity}"

if [[ "${LOOM_ACCEPTANCE_RUN:-0}" != "1" ]]; then
  cat <<MSG
Main Documents filesystem fidelity acceptance is opt-in.

Target:
  $target

Run on main only after confirming the path:
  LOOM_ACCEPTANCE_RUN=1 tests/smoke/v0_6_8_filesystem_fidelity_main.sh

The script writes only under .loom-acceptance, waits for main/Documents import,
then runs report/backfill dry-runs.
MSG
  exit 0
fi

LOOM_ACCEPTANCE_ROOT="$target" tests/smoke/v0_6_8_filesystem_fidelity_local.sh

echo "Waiting briefly for main/Documents import..."
sleep "${LOOM_ACCEPTANCE_WAIT_SECONDS:-20}"

loom storage main-documents status || true
loom storage fidelity report --prefix "main/Documents/.loom-acceptance/v0.6.8-filesystem-fidelity" || true
loom storage fidelity backfill --source main-documents --dry-run || true

echo "v0.6.8 main Documents filesystem fidelity acceptance probe complete"

#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

target="${LOOM_ACCEPTANCE_ROOT:-$HOME/loom-box/Documents/.loom-acceptance/v0.6.8-filesystem-fidelity}"

if [[ "${LOOM_ACCEPTANCE_RUN:-0}" != "1" ]]; then
  cat <<MSG
MacBook filesystem fidelity acceptance is opt-in.

Target:
  $target

Run:
  LOOM_ACCEPTANCE_RUN=1 tests/smoke/v0_6_8_filesystem_fidelity_macbook.sh

The script writes only under .loom-acceptance and then asks LOOM to report the
MacBook storage view after the watched-root worker has time to converge.
MSG
  exit 0
fi

LOOM_ACCEPTANCE_ROOT="$target" tests/smoke/v0_6_8_filesystem_fidelity_local.sh

echo "Waiting briefly for watched-root convergence..."
sleep "${LOOM_ACCEPTANCE_WAIT_SECONDS:-20}"

loom watched-roots status --include-fidelity || true
loom storage fidelity report --node macbook --prefix "macbook/Backups/Documents/current/.loom-acceptance/v0.6.8-filesystem-fidelity" || true
loom storage safe-delete check "$target" --json >/dev/null || true

echo "v0.6.8 MacBook filesystem fidelity acceptance probe complete"

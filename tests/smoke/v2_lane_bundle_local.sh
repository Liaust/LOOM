#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"

run_test() {
  (cd "$ROOT" && go test "$@")
}

printf '[smoke] validating Lane content-policy profiles locally\n'
run_test ./internal/filepolicy -count=1

printf '[smoke] validating file-tree and bundle lifecycle with temporary fixtures\n'
run_test ./internal/lane -count=1 -v

printf '[smoke] validating offline read-only Portal planning\n'
run_test ./internal/loomcli/portal \
  -run 'TestLanePlanExecutesWithoutTransferToolsMainOrFilesystemMutation|TestLoomignorePortalActionInventoryAndAvailability|TestLoomignorePortalNarrowRenderingKeepsPolicySignals' \
  -count=1

printf '[ok] v2 Lane bundle local lifecycle smoke passed\n'

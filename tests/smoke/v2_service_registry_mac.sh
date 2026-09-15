#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

if [[ "$(uname -s)" != "Darwin" ]]; then
  printf '[skip] disposable user LaunchAgent acceptance requires macOS\n'
  exit 0
fi

LOOM_RUN_SERVICE_MAC_ACCEPTANCE=1 \
  go test ./internal/nodeagent -run TestDisposableMacServiceRegistryLifecycle -count=1 -v
go test ./internal/serviceregistry -run 'TestRegisterProject|TestReconcileProject|TestRedactManagerResult' -count=1
go test ./internal/loomcli/portal -run 'TestArchivedAndInactiveServicesHideLifecycleActions|TestServiceActionsUseRegisteredCapabilityAndConfirmation' -count=1

printf '[ok] disposable Mac service registry lifecycle and cleanup passed\n'

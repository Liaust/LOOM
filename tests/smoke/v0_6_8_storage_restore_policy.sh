#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

go test ./internal/storageexport ./internal/storageretention ./internal/storagefidelity
go run ./cmd/loom storage restore plan --help >/dev/null
go run ./cmd/loom storage safe-delete check --help >/dev/null

cat <<MSG
v0.6.8 restore policy smoke passed.

Manual restore checks once fixtures have converged:
  loom storage restore plan main/Documents/.loom-acceptance/v0.6.8-filesystem-fidelity --mode safe --target /tmp/loom-restore-safe
  loom storage restore plan main/Documents/.loom-acceptance/v0.6.8-filesystem-fidelity --mode faithful --target /tmp/loom-restore-faithful
MSG

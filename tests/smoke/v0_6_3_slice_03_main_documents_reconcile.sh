#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MAIN_HOST="${LOOM_MAIN_HOST:-loom-main}"
pass_count=0

fail() {
  printf 'v0.6.3 slice 03 main documents reconcile: %s\n' "$*" >&2
  exit 1
}

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$*"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

require_file_contains() {
  local file="$1"
  local needle="$2"
  grep -Fq "$needle" "$file" || fail "$file does not contain expected text: $needle"
}

ssh_main() {
  ssh -o BatchMode=yes "$MAIN_HOST" "$@"
}

require_command bash
require_command grep

bash -n "$0"
pass "smoke script parses"

require_file_contains "$ROOT_DIR/internal/mainstorage/service.go" "func (s Service) Reconcile"
require_file_contains "$ROOT_DIR/internal/mainstorage/service.go" "TombstoneMainDocument"
require_file_contains "$ROOT_DIR/internal/loomcli/storage.go" "Use:   \"reconcile\""
require_file_contains "$ROOT_DIR/internal/loomcli/storage_doctor.go" "source == \"main-documents\""
require_file_contains "$ROOT_DIR/internal/storageview/builder.go" "mainDocumentCurrentFileShouldAppear"
require_file_contains "$ROOT_DIR/internal/workers/runtimes/main_documents_import.go" "FilesTombstoned"
pass "main Documents reconciliation code paths are present"

if [[ "${LOOM_RUN_PRODUCTION_V0_6_3_RECONCILE_SMOKE:-}" != "1" ]]; then
  pass "production reconcile checks skipped; set LOOM_RUN_PRODUCTION_V0_6_3_RECONCILE_SMOKE=1 after applying the release"
  printf '[smoke] completed %d checks\n' "$pass_count"
  exit 0
fi

require_command ssh

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
rel=".loom-acceptance/000-v0.6.3-slice-03-main-documents-reconcile/${stamp}/delete-me.txt"
source_path="/var/lib/loom/main-documents/${rel}"
view_path="main/Documents/${rel}"

ssh_main "
  set -euo pipefail
  mkdir -p \"\$(dirname \"$source_path\")\"
  printf 'slice 03 reconcile smoke %s\n' '$stamp' > \"$source_path\"
  touch -d '2 minutes ago' \"$source_path\"
  for attempt in \$(seq 1 30); do
    if loom storage inspect \"$view_path\" >/dev/null 2>&1; then
      exit 0
    fi
    sleep 2
  done
  printf 'timed out waiting for main Documents import: %s\n' \"$view_path\" >&2
  exit 1
"
pass "production smoke cataloged .loom-acceptance main/Documents file through the supervisor"

ssh_main "
  set -euo pipefail
  rm -f \"$source_path\"
  dry_run=\"\$(loom storage main-documents reconcile --dry-run)\"
  printf '%s\n' \"\$dry_run\" | grep -F '$rel' >/dev/null
  printf '%s\n' \"\$dry_run\" | grep -F 'Missing cataloged:' >/dev/null
"
pass "production smoke dry-run reports deleted source path"

ssh_main "
  set -euo pipefail
  loom storage main-documents reconcile --yes --reason 'v0.6.3 slice 03 reconcile smoke delete' --created-by 'v0.6.3-slice03-smoke' >/tmp/loom-v063-slice03-reconcile.out
  loom storage export rebuild >/tmp/loom-v063-slice03-export.out
  if loom storage tree --plain | grep -Fx \"$view_path\" >/dev/null; then
    printf 'view path still exposed after tombstone: %s\n' \"$view_path\" >&2
    exit 1
  fi
  loom storage list --source-area main_documents --availability-state tombstoned --limit 1000 | grep -F '$rel' >/dev/null
"
pass "production smoke tombstoned deleted path and removed it from the current tree"

printf '[smoke] completed %d checks\n' "$pass_count"

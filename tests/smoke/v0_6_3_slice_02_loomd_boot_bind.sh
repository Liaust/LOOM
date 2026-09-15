#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MAIN_HOST="${LOOM_MAIN_HOST:-loom-main}"
pass_count=0

fail() {
  printf 'v0.6.3 slice 02 loomd boot bind: %s\n' "$*" >&2
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

require_file_not_contains() {
  local file="$1"
  local needle="$2"
  if grep -Fq "$needle" "$file"; then
    fail "$file still contains forbidden text: $needle"
  fi
}

ssh_main() {
  ssh -o BatchMode=yes "$MAIN_HOST" "$@"
}

require_command bash
require_command grep

storage_module="$ROOT_DIR/nix/modules/loom-storage.nix"
service_module="$ROOT_DIR/nix/modules/loom-service.nix"

bash -n "$0"
pass "smoke script parses"

require_file_contains "$storage_module" "systemd.services.loom-main-documents-bind"
require_file_contains "$storage_module" "LOOM main Documents export bind mount"
require_file_contains "$storage_module" "systemd-tmpfiles-setup.service"
require_file_contains "$storage_module" "mount --bind"
require_file_contains "$storage_module" "not mounted and is not empty"
require_file_not_contains "$storage_module" "systemd.mounts = ["
pass "Nix storage module uses service-managed bind mount"

require_file_contains "$service_module" "loom-main-documents-bind.service"
require_file_not_contains "$service_module" "\"\${cfg.storageExportRoot}/main/Documents\""
pass "loomd depends on bind service instead of RequiresMountsFor export path"

if [[ "${LOOM_RUN_PRODUCTION_V0_6_3_BIND_SMOKE:-}" != "1" ]]; then
  pass "production bind checks skipped; set LOOM_RUN_PRODUCTION_V0_6_3_BIND_SMOKE=1 after applying the rebuild"
  printf '[smoke] completed %d checks\n' "$pass_count"
  exit 0
fi

require_command ssh

ssh_main '
  set -euo pipefail
  systemctl is-active loom-main-documents-bind.service >/dev/null
  systemctl is-active loomd >/dev/null
  findmnt /var/lib/loom/storage-views/main-export/main/Documents >/dev/null
  loom status --json | jq -e ".ok == true and .data.status == \"ok\"" >/dev/null
'
pass "production services and bind mount are active"

ssh_main '
  set -euo pipefail
  loom --json storage doctor --include-mount | jq -e "
    any(.checks[]?; .id == \"main_documents.bind_mount\" and .status == \"ok\")
  " >/dev/null
'
pass "production storage doctor reports healthy main Documents bind mount"

printf '[smoke] completed %d checks\n' "$pass_count"

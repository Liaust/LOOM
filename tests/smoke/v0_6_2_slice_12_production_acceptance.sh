#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

HELPER="${LOOM_MACBOOK_HELPER:-$ROOT_DIR/scripts/loom-macbook}"
BOX_PATH="${LOOM_BOX_PATH:-$HOME/loom-box}"
NODE_AGENT_BIN="${LOOM_NODE_AGENT_BIN:-$HOME/.local/bin/loom-node-agent}"
MAIN_SSH="${LOOM_MAIN_SSH:-loom-main}"
NODE_KEY="${LOOM_WORKSPACE_NODE_KEY:-macbook}"
MAIN_SMB_SHARE="${LOOM_MAIN_SMB_SHARE:-loom-storage}"
MAIN_STORAGE_MOUNT="${LOOM_MAIN_STORAGE_MOUNT:-$HOME/loom-storage}"
FINDER_MOUNT="${LOOM_MAIN_SMB_FINDER_MOUNT:-/Volumes/$MAIN_SMB_SHARE}"
ACCEPTANCE_ROOT_NAME="${LOOM_ACCEPTANCE_ROOT_NAME:-.loom-acceptance/v0.6.2}"
RUN_ID="${LOOM_ACCEPTANCE_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
pass_count=0

fail() {
  printf 'v0.6.2 slice 12 production acceptance: %s\n' "$*" >&2
  exit 1
}

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$*"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

json_query() {
  jq -r "$@"
}

shell_quote() {
  printf "%s" "$1" | sed "s/'/'\\\\''/g; 1s/^/'/; \$s/\$/'/"
}

main_ssh() {
  ssh "$MAIN_SSH" "$@"
}

mounted_storage_root() {
  local path
  for path in "$MAIN_STORAGE_MOUNT" "$FINDER_MOUNT"; do
    if mount 2>/dev/null | grep -F " on ${path} " >/dev/null 2>&1; then
      printf '%s\n' "$path"
      return 0
    fi
  done
  return 1
}

wait_for_path() {
  local path="$1"
  local label="$2"
  local attempt
  for attempt in $(seq 1 24); do
    if [[ -e "$path" ]]; then
      return 0
    fi
    main_ssh 'loom storage export rebuild >/dev/null 2>&1 || true'
    sleep 5
  done
  fail "timed out waiting for $label at $path"
}

wait_for_main_status_state() {
  local relative_path="$1"
  local expected_state="$2"
  local attempt status
  for attempt in $(seq 1 18); do
    main_ssh 'loom worker run main.main_documents_import --once --reason v0.6.2-slice-12 >/dev/null 2>&1 || true'
    status="$(main_ssh 'loom --json storage main-documents status')"
    if printf '%s' "$status" | json_query --arg p "$relative_path" --arg s "$expected_state" \
      '.data.imports[]? | select(.relative_path == $p and .state == $s) | .state' | grep -qx "$expected_state"; then
      return 0
    fi
    sleep 5
  done
  printf '%s\n' "$status" >&2
  fail "timed out waiting for main Documents import state $expected_state for $relative_path"
}

wait_for_main_document_cataloged() {
  local relative_path="$1"
  local logical_path="main/Documents/$relative_path"
  local attempt status
  for attempt in $(seq 1 18); do
    main_ssh 'loom worker run main.main_documents_import --once --reason v0.6.2-slice-12 >/dev/null 2>&1 || true'
    status="$(main_ssh "loom --json storage status $(shell_quote "$logical_path") 2>/dev/null || true")"
    if printf '%s' "$status" | json_query -e \
      '(.safe_to_delete.entry.metadata.import_state == "accepted") or (.entry.metadata.import_state == "accepted")' >/dev/null 2>&1; then
      return 0
    fi
    sleep 5
  done
  printf '%s\n' "$status" >&2
  fail "timed out waiting for main Documents catalog entry for $relative_path"
}

wait_for_dropzone_transfer() {
  local relative_path="$1"
  local attempt status
  for attempt in $(seq 1 18); do
    "$NODE_AGENT_BIN" --json dropzone run --once >/dev/null || true
    status="$("$NODE_AGENT_BIN" --json dropzone status)"
    if printf '%s' "$status" | json_query --arg p "$relative_path" '
      [.data.status.accepted[]?.relative_dropzone_path,
       .data.status.active[]?.relative_dropzone_path,
       .data.status.failed[]?.relative_dropzone_path] | index($p) != null
    ' | grep -qx true; then
      return 0
    fi
    sleep 5
  done
  printf '%s\n' "$status" >&2
  fail "Dropzone transfer did not appear in local status for $relative_path"
}

status_state() {
  local logical_path="$1"
  main_ssh "loom --plain storage status $(shell_quote "$logical_path")" | tr -d '\r'
}

make_large_fixture() {
  local path="$1"
  if command -v mkfile >/dev/null 2>&1; then
    mkfile 12m "$path"
  else
    dd if=/dev/zero of="$path" bs=1M count=12 >/dev/null 2>&1
  fi
}

bash -n "$HELPER"
bash -n "$0"
pass "acceptance script and MacBook helper parse"

if [[ "${LOOM_RUN_PRODUCTION_V0_6_2_ACCEPTANCE:-}" != "1" ]]; then
  pass "production deployment acceptance skipped; set LOOM_RUN_PRODUCTION_V0_6_2_ACCEPTANCE=1 to run live MacBook/main checks"
  printf '[smoke] completed %d checks\n' "$pass_count"
  exit 0
fi

[[ "$(uname -s)" == "Darwin" ]] || fail "production acceptance must run from the production MacBook workspace node"
require_command jq
require_command ssh
require_command nc

[[ -x "$NODE_AGENT_BIN" ]] || fail "node-agent binary is missing or not executable: $NODE_AGENT_BIN"
[[ -d "$BOX_PATH/Documents" ]] || fail "LOOM Box Documents path missing: $BOX_PATH/Documents"
[[ -d "$BOX_PATH/Dropzone" ]] || fail "LOOM Box Dropzone path missing: $BOX_PATH/Dropzone"
pass "production MacBook LOOM Box paths exist"

"$HELPER" network-check >/dev/null
pass "WireGuard, SSH, and main HTTP are reachable"

mounted="$(mounted_storage_root || true)"
[[ -n "$mounted" ]] || fail "LOOM Main storage is not mounted at $MAIN_STORAGE_MOUNT or $FINDER_MOUNT"
[[ -d "$mounted/main/Documents" ]] || fail "mounted storage missing main/Documents: $mounted"
[[ -d "$mounted/$NODE_KEY/Backups/Documents/current" ]] || fail "mounted storage missing $NODE_KEY Backups/Documents/current"
pass "LOOM Main SMB storage view is mounted and has expected roots"

documents_root_json="$("$NODE_AGENT_BIN" --json watched-roots list)"
documents_root="$(printf '%s' "$documents_root_json" | json_query -r '
  [.data.roots[]? | select(.root_key == "loom_box__documents" or .config.root_relative_path == "Documents") | .root_key][0] // empty
')"
[[ -n "$documents_root" ]] || fail "could not find LOOM Box Documents watched root in node-agent config"
pass "Documents watched root is registered as $documents_root"

documents_rel="$ACCEPTANCE_ROOT_NAME/Documents/$RUN_ID"
documents_dir="$BOX_PATH/Documents/$documents_rel"
mkdir -p "$documents_dir/nested"
printf 'small acceptance file for %s\n' "$RUN_ID" >"$documents_dir/small.txt"
printf 'nested acceptance file for %s\n' "$RUN_ID" >"$documents_dir/nested/nested.txt"
make_large_fixture "$documents_dir/large-over-old-ceiling.bin"

"$NODE_AGENT_BIN" --json watched-roots run "$documents_root" --once --mode full --stability-window 0s --flush >/dev/null
main_ssh 'loom storage export rebuild >/dev/null'
wait_for_path "$mounted/$NODE_KEY/Backups/Documents/current/$documents_rel/small.txt" "Documents small backup"
wait_for_path "$mounted/$NODE_KEY/Backups/Documents/current/$documents_rel/nested/nested.txt" "Documents nested backup"
wait_for_path "$mounted/$NODE_KEY/Backups/Documents/current/$documents_rel/large-over-old-ceiling.bin" "Documents large backup"
pass "LOOM Box Documents small, nested, and large files appear in mounted Backups/Documents"

backup_status="$(status_state "$NODE_KEY/Backups/Documents/current/$documents_rel/small.txt")"
case "$backup_status" in
  accepted|safe_to_delete|not_safe_to_delete) ;;
  *) fail "unexpected storage status for Documents backup: $backup_status" ;;
esac
pass "storage status explains the Documents backup path as $backup_status"

dropzone_rel="$ACCEPTANCE_ROOT_NAME/Dropzone/$RUN_ID/dropzone.txt"
dropzone_file="$BOX_PATH/Dropzone/$dropzone_rel"
mkdir -p "$(dirname "$dropzone_file")"
printf 'dropzone acceptance file for %s\n' "$RUN_ID" >"$dropzone_file"
wait_for_dropzone_transfer "$dropzone_rel"
pass "Dropzone accepts or tracks an explicit custody-transfer file"

main_rel="$ACCEPTANCE_ROOT_NAME/Main Documents/$RUN_ID"
main_dir="$mounted/main/Documents/$main_rel"
mkdir -p "$main_dir"
printf 'main Documents direct SMB file for %s\n' "$RUN_ID" >"$main_dir/direct.txt"
printf 'finder metadata should be ignored\n' >"$main_dir/.DS_Store"
printf 'appledouble metadata should be ignored\n' >"$main_dir/._direct.txt"

wait_for_main_document_cataloged "$main_rel/direct.txt"
wait_for_main_status_state "$main_rel/.DS_Store" "ignored"
wait_for_main_status_state "$main_rel/._direct.txt" "ignored"
main_ssh 'loom storage export rebuild >/dev/null'
wait_for_path "$mounted/main/Documents/$main_rel/direct.txt" "main Documents direct file"
pass "direct SMB writes to main/Documents are imported after stable-file checks and Apple sidecars are ignored"

main_status="$(status_state "main/Documents/$main_rel/direct.txt")"
case "$main_status" in
  accepted|safe_to_delete|not_safe_to_delete) ;;
  *) fail "unexpected storage status for main Documents file: $main_status" ;;
esac
pass "storage status explains the direct main Documents path as $main_status"

main_ssh 'loom storage failures --plain >/dev/null'
main_ssh 'loom storage transfers --plain >/dev/null'
main_ssh 'loom storage export status --plain >/dev/null'
pass "CLI storage failures/transfers/export status commands are live"

printf '[smoke] completed %d checks\n' "$pass_count"

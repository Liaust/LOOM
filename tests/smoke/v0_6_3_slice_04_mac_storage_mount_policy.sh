#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

pass() {
  printf '[ok] %s\n' "$*"
}

require_file_contains() {
  local path="$1"
  local needle="$2"
  grep -F "$needle" "$path" >/dev/null || fail "$path missing: $needle"
}

export XDG_STATE_HOME="$TMP_DIR/state"
export LOOM_STORAGE_MOUNT_POLICY_PATH="$TMP_DIR/storage-mount-policy.json"
export LOOM_STORAGE_MOUNT_STATUS_PATH="$TMP_DIR/storage-mount-status.json"
export LOOM_MAIN_STORAGE_MOUNT="$TMP_DIR/LOOM Main"
export LOOM_MAIN_SMB_HOST="loom-storage"
export LOOM_MAIN_SMB_IP="10.44.0.2"
export LOOM_MAIN_SMB_SHARE="loom-storage"
export LOOM_MAIN_SMB_USER="loomshare"
export LOOM_CLOUD_FOLDER_MOUNT="$TMP_DIR/loom-cloud"
export LOOM_CLOUD_FOLDER_SMB_HOST="127.0.0.1"
export LOOM_CLOUD_FOLDER_SMB_SHARE="backup"
export LOOM_CLOUD_FOLDER_SMB_USER="loom-cloud-test"
export LOOM_CLOUD_FOLDER_SMB_FINDER_MOUNT="$TMP_DIR/Volumes/backup"

status_json="$(cd "$ROOT_DIR" && go run ./cmd/loom-node-agent storage-mount status --json)"
printf '%s\n' "$status_json" | jq -e '
  .ok == true and
  .data.schema_version == "loom.storage.mount_policy.v0.6.3" and
  .data.desired_state == "unmounted" and
  .data.protocol == "smb" and
  .data.host == "loom-storage" and
  .data.share == "loom-storage" and
  .data.cloud_storage.desired_state == "unmounted" and
  .data.cloud_storage.host == "127.0.0.1" and
  .data.cloud_storage.share == "backup"
' >/dev/null || fail "node-agent storage-mount status JSON did not match expected defaults"
pass "node-agent storage-mount status reports conservative default policy"

enable_json="$(cd "$ROOT_DIR" && go run ./cmd/loom-node-agent storage-mount enable --json)"
printf '%s\n' "$enable_json" | jq -e '.ok == true and .data.desired_state == "mounted"' >/dev/null \
  || fail "storage-mount enable did not persist desired mounted state"
pass "node-agent storage-mount enable persists mounted desired state"

repair_json="$(cd "$ROOT_DIR" && go run ./cmd/loom-node-agent storage-mount repair-once --json --force)"
printf '%s\n' "$repair_json" | jq -e '
  .ok == true and
  .data.desired_state == "mounted" and
  (.data.actual_state == "mounted" or .data.last_error_category != "")
' >/dev/null || fail "storage-mount repair-once did not return a structured status"
pass "node-agent storage-mount repair-once returns structured non-interactive status"

disable_json="$(cd "$ROOT_DIR" && go run ./cmd/loom-node-agent storage-mount disable --json --keep-mounted)"
printf '%s\n' "$disable_json" | jq -e '.ok == true and .data.desired_state == "unmounted"' >/dev/null \
  || fail "storage-mount disable did not persist desired unmounted state"
pass "node-agent storage-mount disable persists unmounted desired state"

cloud_status_json="$(cd "$ROOT_DIR" && go run ./cmd/loom-node-agent storage-mount cloud status --json)"
printf '%s\n' "$cloud_status_json" | jq -e '
  .ok == true and
  .data.name == "LOOM Cloud storage mount" and
  .data.desired_state == "unmounted" and
  .data.host == "127.0.0.1" and
  .data.share == "backup"
' >/dev/null || fail "storage-mount cloud status did not report expected cloud defaults"
pass "node-agent storage-mount cloud status is wired"

cloud_enable_json="$(cd "$ROOT_DIR" && go run ./cmd/loom-node-agent storage-mount cloud enable --json)"
printf '%s\n' "$cloud_enable_json" | jq -e '.ok == true and .data.desired_state == "mounted"' >/dev/null \
  || fail "storage-mount cloud enable did not persist desired mounted state"
pass "node-agent storage-mount cloud enable persists mounted desired state"

cloud_repair_json="$(cd "$ROOT_DIR" && go run ./cmd/loom-node-agent storage-mount cloud repair-once --json --force)"
printf '%s\n' "$cloud_repair_json" | jq -e '
  .ok == true and
  .data.desired_state == "mounted" and
  (.data.actual_state == "mounted" or .data.last_error_category != "")
' >/dev/null || fail "storage-mount cloud repair-once did not return structured status"
pass "node-agent storage-mount cloud repair-once returns structured status"

cloud_disable_json="$(cd "$ROOT_DIR" && go run ./cmd/loom-node-agent storage-mount cloud disable --json --keep-mounted)"
printf '%s\n' "$cloud_disable_json" | jq -e '.ok == true and .data.desired_state == "unmounted"' >/dev/null \
  || fail "storage-mount cloud disable did not persist desired unmounted state"
pass "node-agent storage-mount cloud disable persists unmounted desired state"

loom_json="$(cd "$ROOT_DIR" && go run ./cmd/loom storage mount-policy status --local --json)"
printf '%s\n' "$loom_json" | jq -e '.schema_version == "loom.storage.mount_policy.v0.6.3" and .desired_state == "unmounted"' >/dev/null \
  || fail "loom storage mount-policy status --local did not use local policy"
pass "loom storage mount-policy local CLI is wired"

require_file_contains "$ROOT_DIR/internal/nodeagent/runtime/models.go" "KindStorageMount"
require_file_contains "$ROOT_DIR/internal/nodeagent/runtime/store.go" "WorkerKeyStorageMount"
require_file_contains "$ROOT_DIR/internal/nodeagent/runtime_workers.go" "storageMountRuntime"
require_file_contains "$ROOT_DIR/scripts/loom-macbook" "storage-enable"
require_file_contains "$ROOT_DIR/scripts/loom-macbook" "cloud-enable"
require_file_contains "$ROOT_DIR/scripts/raycast/loom-network-enable.sh" "storage-enable"
require_file_contains "$ROOT_DIR/scripts/raycast/loom-network-enable.sh" "cloud-enable"
require_file_contains "$ROOT_DIR/scripts/raycast/loom-network-disable.sh" "storage-disable"
require_file_contains "$ROOT_DIR/scripts/raycast/loom-network-disable.sh" "cloud-disable"
pass "runtime worker and Raycast/helper wiring are present"

printf '[ok] v0.6.3 slice 04 Mac storage mount policy smoke passed\n'

#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-6-2-slice-03.XXXXXX")"
pass_count=0

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

run_loom() {
  if [[ -n "${LOOM_BIN:-}" ]]; then
    "$LOOM_BIN" "$@"
  else
    (cd "$ROOT_DIR" && go run ./cmd/loom "$@")
  fi
}

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$*"
}

check_json() {
  local label="$1"
  local file="$2"
  local filter="$3"

  jq -e "$filter" "$file" >/dev/null
  pass "$label"
}

command -v jq >/dev/null

BOX_ROOT="$TMP_DIR/LOOM Box"
INIT_JSON="$TMP_DIR/init.json"
WATCH_JSON="$TMP_DIR/watch.json"
LEGACY_ROOT="$TMP_DIR/Legacy LOOM Box"
LEGACY_JSON="$TMP_DIR/legacy-init.json"
LEGACY_MISSING_ROOT="$TMP_DIR/Legacy Missing Launchpad LOOM Box"
LEGACY_MISSING_JSON="$TMP_DIR/legacy-missing-init.json"

run_loom --json box init --path "$BOX_ROOT" --profile workspace >"$INIT_JSON"
test -d "$BOX_ROOT/Documents"
pass "new Box init creates Documents"
check_json "new Box reaches ok state" \
  "$INIT_JSON" \
  '.status_after.state == "ok"'

run_loom --json box watch-plan --path "$BOX_ROOT" --profile workspace >"$WATCH_JSON"
check_json "watch plan includes Documents watched root" \
  "$WATCH_JSON" \
  '([.watched_roots[] | select(.backend_root_key == "loom_box__documents" and .root_relative_path == "Documents")] | length) == 1'
check_json "Documents watched root uses responsive large-file defaults" \
  "$WATCH_JSON" \
  '.watched_roots[] | select(.backend_root_key == "loom_box__documents") | .config_json.scan.full_rescan_interval == "1m" and .config_json.scan.max_hash_file_bytes == 536870912 and .config_json.backup_policy.max_file_bytes == 536870912'
check_json "Notes watched root keeps text-oriented defaults" \
  "$WATCH_JSON" \
  '.watched_roots[] | select(.backend_root_key == "loom_box__notes") | .config_json.scan.full_rescan_interval == "1m" and .config_json.scan.max_hash_file_bytes == 52428800 and .config_json.sync_policy.mode == "selected_files"'

mkdir -p \
  "$LEGACY_ROOT/.loom/policies" \
  "$LEGACY_ROOT/.loom/state/dropzone" \
  "$LEGACY_ROOT/Projects" \
  "$LEGACY_ROOT/Notes" \
  "$LEGACY_ROOT/Launchpad" \
  "$LEGACY_ROOT/Dropzone"
printf 'legacy launchpad file\n' >"$LEGACY_ROOT/Launchpad/legacy.txt"
cat >"$LEGACY_ROOT/.loom/box.yaml" <<EOF
schema_version: loom.box.v0.4.1
box_id: box_legacy
owner_node: macbook
profile: workspace
root_path: "$LEGACY_ROOT"
areas:
  projects:
    path: Projects
    enabled: true
  notes:
    path: Notes
    enabled: true
  launchpad:
    path: Launchpad
    enabled: true
  dropzone:
    path: Dropzone
    enabled: false
default_project_path: Projects
policies:
  notes: .loom/policies/notes.watch.yaml
  launchpad: .loom/policies/launchpad.watch.yaml
  dropzone: .loom/policies/dropzone.transfer.yaml
metadata:
  created_at: "2026-05-30T12:00:00Z"
  updated_at: "2026-05-30T12:00:00Z"
  created_by: loom box init
EOF

run_loom --json box init --path "$LEGACY_ROOT" --profile workspace >"$LEGACY_JSON"
test -d "$LEGACY_ROOT/Documents"
test -f "$LEGACY_ROOT/Launchpad/legacy.txt"
pass "legacy Launchpad Box upgrades without deleting user files"
check_json "legacy Box upgrade records contract backup" \
  "$LEGACY_JSON" \
  '.status_after.state == "ok" and (.backup_files | length) == 1'
backup_count="$(find "$LEGACY_ROOT/.loom/backups/box-init" -type f -name '*.bak' | wc -l | tr -d ' ')"
test "$backup_count" = "1"
backup_path="$(jq -r '.backup_files[0]' "$LEGACY_JSON")"
grep -q 'launchpad:' "$backup_path"
pass "legacy contract backup preserves Launchpad contract content"

mkdir -p \
  "$LEGACY_MISSING_ROOT/.loom/policies" \
  "$LEGACY_MISSING_ROOT/.loom/state/dropzone" \
  "$LEGACY_MISSING_ROOT/Projects" \
  "$LEGACY_MISSING_ROOT/Notes" \
  "$LEGACY_MISSING_ROOT/Dropzone"
cat >"$LEGACY_MISSING_ROOT/.loom/box.yaml" <<EOF
schema_version: loom.box.v0.4.1
box_id: box_legacy_missing_launchpad
owner_node: macbook
profile: workspace
root_path: "$LEGACY_MISSING_ROOT"
areas:
  projects:
    path: Projects
    enabled: true
  notes:
    path: Notes
    enabled: true
  launchpad:
    path: Launchpad
    enabled: true
  dropzone:
    path: Dropzone
    enabled: false
default_project_path: Projects
policies:
  notes: .loom/policies/notes.watch.yaml
  launchpad: .loom/policies/launchpad.watch.yaml
  dropzone: .loom/policies/dropzone.transfer.yaml
metadata:
  created_at: "2026-05-30T12:00:00Z"
  updated_at: "2026-05-30T12:00:00Z"
  created_by: loom box init
EOF

run_loom --json box init --path "$LEGACY_MISSING_ROOT" --profile workspace >"$LEGACY_MISSING_JSON"
test -d "$LEGACY_MISSING_ROOT/Documents"
test ! -e "$LEGACY_MISSING_ROOT/Launchpad"
pass "missing legacy Launchpad is not recreated during Documents migration"
check_json "missing legacy Launchpad migration reaches ok state" \
  "$LEGACY_MISSING_JSON" \
  '.status_after.state == "ok" and (.status_after.contract.areas.launchpad == null) and (.status_after.contract.policies.launchpad == null)'

(cd "$ROOT_DIR" && go test ./internal/setup -run 'TestApplyWorkspacePreservesExistingBoxSafeRootCeiling|TestFilesystemConfigFromPlanDoesNotLetExplicitSafeRootDowngradeBox')
pass "setup regression tests preserve existing safe-root ceilings"

printf '[smoke] completed %d checks\n' "$pass_count"

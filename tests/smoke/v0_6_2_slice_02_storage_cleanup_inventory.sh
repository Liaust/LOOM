#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-6-2-slice-02.XXXXXX")"
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

STORAGE_ROOT="$TMP_DIR/storage-export"
MAIN_DOC="$STORAGE_ROOT/main/Documents/v0-6-2-slice-02-smoke.txt"
GENERATED_VIEW="$STORAGE_ROOT/macbook/Backups/Documents/current/v0-6-2-slice-02-smoke.txt"
PLAN_PATH="$TMP_DIR/cleanup-plan.json"
INVENTORY_JSON="$TMP_DIR/inventory.json"
DRY_RUN_JSON="$TMP_DIR/dry-run.json"
APPLY_JSON="$TMP_DIR/apply.json"

mkdir -p "$(dirname "$MAIN_DOC")" "$(dirname "$GENERATED_VIEW")"
printf 'main owned\n' >"$MAIN_DOC"
printf 'generated view\n' >"$GENERATED_VIEW"

run_loom --json storage inventory --root "$STORAGE_ROOT" --production >"$INVENTORY_JSON"
check_json "inventory finds one auto item and one manual-review item" \
  "$INVENTORY_JSON" \
  '.summary.candidates == 2 and .summary.auto_apply == 1 and .summary.manual_review == 1'
check_json "inventory distinguishes view/materialized cleanup from catalog disposition" \
  "$INVENTORY_JSON" \
  '([.items[] | select(.cleanup_scope != null and .catalog_disposition != null)] | length) == 2'

run_loom storage cleanup plan --root "$STORAGE_ROOT" --production --out "$PLAN_PATH" >/dev/null
test -s "$PLAN_PATH"
pass "cleanup plan file written"

run_loom --json storage cleanup apply --plan "$PLAN_PATH" --dry-run >"$DRY_RUN_JSON"
check_json "dry-run would remove only the auto-applicable item" \
  "$DRY_RUN_JSON" \
  '.summary.would_remove == 1 and .summary.manual_review == 1 and .summary.removed == 0'
test -f "$GENERATED_VIEW"
pass "dry-run preserves generated view file"

run_loom --json storage cleanup apply --plan "$PLAN_PATH" --yes >"$APPLY_JSON"
check_json "apply removes the generated item and skips manual-review data" \
  "$APPLY_JSON" \
  '.summary.removed == 1 and .summary.manual_review == 1 and .summary.blocked == 0'
test ! -e "$GENERATED_VIEW"
test -f "$MAIN_DOC"
pass "apply preserves main Documents"

printf '[smoke] completed %d checks\n' "$pass_count"

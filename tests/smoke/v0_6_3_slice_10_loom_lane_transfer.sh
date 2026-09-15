#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

if ! command -v rsync >/dev/null 2>&1 || ! command -v ssh >/dev/null 2>&1; then
  printf '[skip] v0.6.3 slice 10 LOOM Lane transfer smoke requires rsync and ssh\n'
  exit 0
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

BOX_PATH="$TMP_DIR/loom-box"
LANE_PATH="$BOX_PATH/loom-lane"

fail() {
  printf 'v0.6.3 slice 10 LOOM Lane transfer: %s\n' "$*" >&2
  exit 1
}

pass() {
  printf '[ok] %s\n' "$*"
}

run_loom() {
  go run ./cmd/loom "$@"
}

run_loom box repair --path "$BOX_PATH" --profile workspace --json >"$TMP_DIR/box-repair.json"
mkdir -p "$LANE_PATH/.loom-acceptance/slice-10"
printf 'lane dry-run payload\n' >"$LANE_PATH/.loom-acceptance/slice-10/payload.txt"

DRY_RUN="$(run_loom lane send --path "$BOX_PATH" --profile workspace --source-node macbook --dry-run --json)"
printf '%s' "$DRY_RUN" | grep -q '"status":"dry_run"' || fail "lane send dry-run did not report dry_run: $DRY_RUN"
printf '%s' "$DRY_RUN" | grep -q '"visible_storage_path":"macbook/Lane/' || fail "lane send dry-run did not report visible Lane storage path: $DRY_RUN"
printf '%s' "$DRY_RUN" | grep -q '"name":"ssh"' || fail "lane send dry-run did not include ssh command plan: $DRY_RUN"
printf '%s' "$DRY_RUN" | grep -q '"name":".*rsync"' || fail "lane send dry-run did not include rsync command plan: $DRY_RUN"
test -f "$LANE_PATH/.loom-acceptance/slice-10/payload.txt" || fail "dry-run should not remove visible Lane payload"
pass "lane send dry-run plans rsync/ssh and preserves visible Lane files"

PORTAL_OUTPUT="$(LOOM_BOX_PATH="$BOX_PATH" LOOM_BOX_PROFILE=workspace run_loom --no-color --no-animation enter --start box --exit-after-render 2>/dev/null || true)"
printf '%s' "$PORTAL_OUTPUT" | grep -q 'Send Lane To Main' || fail "portal Box render missing Send Lane To Main action"
pass "portal Box screen exposes Send Lane To Main"

printf '[ok] v0.6.3 slice 10 LOOM Lane transfer smoke complete\n'

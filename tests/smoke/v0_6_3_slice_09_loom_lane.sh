#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

BOX_PATH="$TMP_DIR/loom-box"
LANE_PATH="$BOX_PATH/loom-lane"

fail() {
  printf 'v0.6.3 slice 09 LOOM Lane: %s\n' "$*" >&2
  exit 1
}

pass() {
  printf '[ok] %s\n' "$*"
}

run_loom() {
  go run ./cmd/loom "$@"
}

run_loom box repair --path "$BOX_PATH" --profile workspace --json >"$TMP_DIR/loom-lane-repair.json"
test -d "$LANE_PATH" || fail "LOOM Lane directory missing after repair"
test -d "$BOX_PATH/.loom/state/lane/batches" || fail "Lane batches state dir missing after repair"
test -f "$LANE_PATH/README.md" || fail "Lane README missing after repair"
pass "box repair creates LOOM Lane and state directories"

mkdir -p "$LANE_PATH/.loom-acceptance/slice-09"
printf 'lane pending payload\n' >"$LANE_PATH/.loom-acceptance/slice-09/pending.txt"

BOX_STATUS="$(run_loom box status --path "$BOX_PATH" --profile workspace --json)"
printf '%s' "$BOX_STATUS" | grep -q '"lane_state":"pending"' || fail "box status did not report pending Lane state: $BOX_STATUS"
printf '%s' "$BOX_STATUS" | grep -q '"pending_items":1' || fail "box status did not report one pending Lane item: $BOX_STATUS"
pass "box status includes pending Lane state"

LANE_STATUS="$(run_loom lane status --path "$BOX_PATH" --profile workspace --json)"
printf '%s' "$LANE_STATUS" | grep -q '"state":"pending"' || fail "lane status did not report pending state: $LANE_STATUS"
printf '%s' "$LANE_STATUS" | grep -q '"pending_items":1' || fail "lane status did not report one pending item: $LANE_STATUS"
printf '%s' "$LANE_STATUS" | grep -q '"relative_path":".loom-acceptance"' || fail "lane status did not include pending directory: $LANE_STATUS"
pass "lane status scans pending items"

PORTAL_OUTPUT="$(LOOM_BOX_PATH="$BOX_PATH" LOOM_BOX_PROFILE=workspace run_loom --no-color --no-animation enter --start box --exit-after-render 2>/dev/null || true)"
printf '%s' "$PORTAL_OUTPUT" | grep -q 'LOOM Lane' || fail "portal Box render missing LOOM Lane section"
printf '%s' "$PORTAL_OUTPUT" | grep -q 'pending=1' || fail "portal Box render missing pending Lane count"
printf '%s' "$PORTAL_OUTPUT" | grep -q '.loom-acceptance' || fail "portal Box render missing pending Lane item"
pass "portal Box screen displays pending Lane items"

printf '[ok] v0.6.3 slice 09 LOOM Lane smoke complete\n'

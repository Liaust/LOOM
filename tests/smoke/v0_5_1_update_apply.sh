#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

fail() {
  printf 'v0.5.1 update apply smoke: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

loom_cli() {
  if [[ -n "${LOOM_BIN:-}" ]]; then
    "$LOOM_BIN" "$@"
  else
    (cd "$REPO_ROOT" && go run ./cmd/loom "$@")
  fi
}

write_migration() {
  local dir="$1"
  local name="$2"
  mkdir -p "$dir/migrations"
  cat >"$dir/migrations/$name" <<'SQL'
-- +goose Up
SELECT 1;
SQL
}

assert_link() {
  local link="$1"
  local want="$2"
  local got
  got="$(readlink "$link")"
  got="$(cd "$got" && pwd -P)"
  want="$(cd "$want" && pwd -P)"
  [[ "$got" == "$want" ]] || fail "symlink $link points at $got, want $want"
}

require_command jq

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-5-1-update-apply.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

ACTIVE="$TMP_DIR/active"
TARGET="$TMP_DIR/target"
CURRENT="$TMP_DIR/current"
STATE="$TMP_DIR/update-state"
write_migration "$ACTIVE" "00036_current.sql"
write_migration "$TARGET" "00036_current.sql"
ln -s "$ACTIVE" "$CURRENT"

loom_cli --json update apply \
  --release-path "$TARGET" \
  --active-path "$CURRENT" \
  --active-migrations-dir "$CURRENT/migrations" \
  --current-migration 36 \
  --state-dir "$STATE" \
  --yes \
  --allow-non-production \
  --skip-backup \
  --skip-rebuild \
  --skip-health-check \
  | jq -e '
      .status == "succeeded"
      and .manifest.rollback.class == "service_only_possible"
      and (.changed[] | select(.step == "switch_release" and .status == "changed"))
    ' >/dev/null

assert_link "$CURRENT" "$TARGET"
loom_cli --json update status --state-dir "$STATE" \
  | jq -e '.active_exists == true and .active.status == "succeeded" and (.history | length) >= 1' >/dev/null

loom_cli --json update rollback \
  --state-dir "$STATE" \
  --yes \
  --allow-non-production \
  --service-only \
  --skip-rebuild \
  --skip-health-check \
  | jq -e '.status == "rolled_back" and (.changed[] | select(.step == "switch_release" and .status == "changed"))' >/dev/null

assert_link "$CURRENT" "$ACTIVE"

AHEAD_ACTIVE="$TMP_DIR/ahead-active"
AHEAD_TARGET="$TMP_DIR/ahead-target"
AHEAD_CURRENT="$TMP_DIR/ahead-current"
AHEAD_STATE="$TMP_DIR/ahead-state"
write_migration "$AHEAD_ACTIVE" "00036_current.sql"
write_migration "$AHEAD_TARGET" "00036_current.sql"
write_migration "$AHEAD_TARGET" "00037_next.sql"
ln -s "$AHEAD_ACTIVE" "$AHEAD_CURRENT"

loom_cli --json update apply \
  --release-path "$AHEAD_TARGET" \
  --active-path "$AHEAD_CURRENT" \
  --active-migrations-dir "$AHEAD_CURRENT/migrations" \
  --current-migration 36 \
  --state-dir "$AHEAD_STATE" \
  --yes \
  --allow-non-production \
  --skip-backup \
  --skip-rebuild \
  --skip-health-check \
  | jq -e '.status == "succeeded" and .manifest.rollback.restore_required == true' >/dev/null

loom_cli --json update rollback \
  --state-dir "$AHEAD_STATE" \
  --yes \
  --allow-non-production \
  --restore-required \
  | jq -e '.status == "database_restore_required" and (.runbook | length) > 0' >/dev/null

assert_link "$AHEAD_CURRENT" "$AHEAD_TARGET"

printf '[smoke] v0.5.1 update apply smoke passed\n'

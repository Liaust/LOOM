#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

fail() {
  printf 'v0.5.1 update maintenance smoke: %s\n' "$*" >&2
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

require_command jq

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-5-1-update-maintenance.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

ACTIVE="$TMP_DIR/active"
TARGET="$TMP_DIR/target"
CURRENT="$TMP_DIR/current"
STATE="$TMP_DIR/update-state"
write_migration "$ACTIVE" "00036_current.sql"
write_migration "$TARGET" "00036_current.sql"
ln -s "$ACTIVE" "$CURRENT"

LOOM_DB_URL="" loom_cli --json update apply \
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
      and .manifest.maintenance_window.status == "skipped"
      and .manifest.maintenance_window.pause_policy.pause_schedules == true
      and .manifest.maintenance_window.pause_policy.pause_direct_event_endpoints == true
    ' >/dev/null

loom_cli --json update maintenance status --state-dir "$STATE" \
  | jq -e '
      .maintenance_window.status == "skipped"
      and .maintenance_window.schema_version == "loom.update.maintenance_window.v0.5.1"
    ' >/dev/null

loom_cli --json update maintenance resume \
  --state-dir "$STATE" \
  --yes \
  --allow-non-production \
  | jq -e '.status == "skipped"' >/dev/null

printf '[smoke] v0.5.1 update maintenance smoke passed\n'

#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

fail() {
  printf 'v0.5.1 update plan smoke: %s\n' "$*" >&2
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

require_command jq

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-5-1-update.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

ACTIVE="$TMP_DIR/active"
TARGET="$TMP_DIR/target"
STATE="$TMP_DIR/update-state"
mkdir -p "$ACTIVE/migrations" "$TARGET/migrations"

cat >"$ACTIVE/migrations/00036_current.sql" <<'SQL'
-- +goose Up
SELECT 1;
SQL

cat >"$TARGET/migrations/00036_current.sql" <<'SQL'
-- +goose Up
SELECT 1;
SQL

cat >"$TARGET/migrations/00037_next.sql" <<'SQL'
-- +goose Up
SELECT 1;
SQL

cat >"$TARGET/loom-release.yaml" <<'YAML'
schema_version: loom.release.v0.5.1
release_id: release_smoke
version: 0.5.1-smoke
commit: smoke
flake_output: .#custom-smoke-main
migrations_dir: migrations
metadata:
  token: should_be_redacted
YAML

loom_cli --json update status --state-dir "$STATE" \
  | jq -e '.active_exists == false and (.diagnostics[0].code == "update.active_manifest_missing")' >/dev/null

loom_cli --json update plan \
  --release-path "$TARGET" \
  --active-path "$ACTIVE" \
  --active-migrations-dir "$ACTIVE/migrations" \
  --current-migration 36 \
  --state-dir "$STATE" \
  | jq -e '
      .schema_version == "loom.update.v0.5.1"
      and .target.release_id == "release_smoke"
      and .nix.target_flake_output == ".#custom-smoke-main"
      and .migrations.pending == 1
      and .rollback.class == "restore_required_if_migrations_apply"
    ' >/dev/null

printf '[smoke] v0.5.1 update plan smoke passed\n'

#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

fail() {
  printf 'v0.5.1 backup CLI smoke: %s\n' "$*" >&2
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

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-5-1-backup.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

BACKUP_DIR="$TMP_DIR/backup"
mkdir -p "$BACKUP_DIR/object-store" "$BACKUP_DIR/private-backups"
printf '{"ok":true}\n' >"$BACKUP_DIR/health.json"
printf 'database dump\n' >"$BACKUP_DIR/loom_main.dump"
printf 'profile: main_full\n' >"$BACKUP_DIR/install.yaml"
printf 'LOOM_DB_URL=[REDACTED]\nLOOM_ENV=production\n' >"$BACKUP_DIR/loom.env.redacted"
cat >"$BACKUP_DIR/manifest.json" <<'JSON'
{
  "schema": "loom.backup.manifest.v0.5.1",
  "backup_kind": "loom_main_state",
  "created_at": "2026-06-02T10:00:00Z",
  "source": {
    "hostname": "loom-main",
    "node_id": "main",
    "node_role": "main",
    "environment": "production",
    "is_vps": false
  },
  "loom": {
    "version": "0.5.1-smoke",
    "source_commit": "smoke",
    "current_migration": 36,
    "latest_migration": 36
  },
  "system": {
    "nix_generation": "/nix/store/smoke-system"
  },
  "database": {
    "name": "loom_main",
    "dump_file": "loom_main.dump",
    "format": "pg_dump_custom"
  },
  "paths": {
    "object_store": "object-store",
    "private_backups": "private-backups",
    "install_manifest": "install.yaml",
    "service_env_redacted": "loom.env.redacted"
  },
  "retention": {
    "mode": "smoke"
  },
  "exclusions": [
    "wireguard_private_keys",
    "ssh_private_keys",
    "node_credential_tokens",
    "enrollment_tokens",
    "environment_secret_files",
    "raw_loom_db_url"
  ]
}
JSON

loom_cli --json backup create --production --dry-run \
  | jq -e '.ok == true and .data.status == "planned" and .data.backend == "maintenance.main_backup"' >/dev/null

loom_cli --json backup verify "$BACKUP_DIR" \
  | jq -e '.ok == true and .data.status == "succeeded" and .data.manifest_schema == "loom.backup.manifest.v0.5.1"' >/dev/null

loom_cli --json backup restore-drill "$BACKUP_DIR" --dry-run \
  | jq -e '.ok == true and .data.status == "planned" and (.data.target_database | startswith("loom_restore_drill_"))' >/dev/null

if loom_cli --plain backup restore-drill "$BACKUP_DIR" --dry-run --target-database loom_main >/dev/null 2>&1; then
  fail "restore-drill dry-run accepted active database"
fi

printf '[smoke] v0.5.1 backup CLI smoke passed\n'

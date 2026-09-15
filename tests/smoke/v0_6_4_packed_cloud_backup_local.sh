#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"

if ! command -v borg >/dev/null 2>&1; then
  printf '[skip] borg is not installed; skipping v0.6.4 packed cloud backup local smoke\n'
  exit 0
fi

if ! command -v jq >/dev/null 2>&1; then
  printf '[skip] jq is not installed; skipping v0.6.4 packed cloud backup local smoke\n'
  exit 0
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

pass() {
  printf '[ok] %s\n' "$*"
}

DATA_DIR="$TMP_DIR/data"
BACKUP_DIR="$DATA_DIR/backups/main/20260614T100000Z-smoke"
BACKUP_DIR_2="$DATA_DIR/backups/main/20260614T110000Z-smoke-new"
STATE_DIR="$TMP_DIR/cloud-state"
BORG_REPO="$TMP_DIR/borg-repo"
BORG_PASSPHRASE_FILE="$TMP_DIR/borg.passphrase"

mkdir -p \
  "$DATA_DIR/object-store" \
  "$DATA_DIR/private-backups" \
  "$DATA_DIR/main-documents" \
  "$DATA_DIR/storage-archive/objects" \
  "$DATA_DIR/storage-archive/manifests" \
  "$BACKUP_DIR/object-store" \
  "$BACKUP_DIR/private-backups" \
  "$BACKUP_DIR/main-documents" \
  "$BACKUP_DIR/storage-archive/objects" \
  "$BACKUP_DIR/storage-archive/manifests" \
  "$BACKUP_DIR_2/object-store" \
  "$BACKUP_DIR_2/private-backups" \
  "$BACKUP_DIR_2/main-documents" \
  "$BACKUP_DIR_2/storage-archive/objects" \
  "$BACKUP_DIR_2/storage-archive/manifests" \
  "$STATE_DIR"

printf 'hello\n' >"$DATA_DIR/main-documents/.loom-acceptance.md"
printf 'hello from backup\n' >"$BACKUP_DIR/main-documents/.loom-acceptance.md"
printf '{"ok":true}\n' >"$BACKUP_DIR/health.json"
printf 'database dump\n' >"$BACKUP_DIR/loom_main.dump"
printf 'local-smoke-passphrase\n' >"$BORG_PASSPHRASE_FILE"
chmod 0600 "$BORG_PASSPHRASE_FILE"

cat >"$BACKUP_DIR/manifest.json" <<'JSON'
{
  "schema": "loom.backup.manifest.v0.6.3",
  "backup_kind": "loom_main_state",
  "created_at": "2026-06-14T10:00:00Z",
  "source": {
    "hostname": "loom-main",
    "node_id": "main",
    "node_role": "main",
    "environment": "production",
    "is_vps": false
  },
  "loom": {
    "version": "smoke",
    "current_migration": 1,
    "latest_migration": 1
  },
  "database": {
    "name": "loom_main",
    "dump_file": "loom_main.dump",
    "format": "pg_dump_custom"
  },
  "paths": {
    "object_store": "object-store",
    "private_backups": "private-backups",
    "main_documents": "main-documents",
    "storage_archive": "storage-archive"
  },
  "policies": {
    "main_box": "not_copied_requires_explicit_watched_roots_or_private_backups"
  }
}
JSON

cp "$BACKUP_DIR/health.json" "$BACKUP_DIR_2/health.json"
cp "$BACKUP_DIR/loom_main.dump" "$BACKUP_DIR_2/loom_main.dump"
cp "$BACKUP_DIR/main-documents/.loom-acceptance.md" "$BACKUP_DIR_2/main-documents/.loom-acceptance.md"
cat >"$BACKUP_DIR_2/manifest.json" <<'JSON'
{
  "schema": "loom.backup.manifest.v0.6.3",
  "backup_kind": "loom_main_state",
  "created_at": "2026-06-14T11:00:00Z",
  "source": {
    "hostname": "loom-main",
    "node_id": "main",
    "node_role": "main",
    "environment": "production",
    "is_vps": false
  },
  "loom": {
    "version": "smoke",
    "current_migration": 1,
    "latest_migration": 1
  },
  "database": {
    "name": "loom_main",
    "dump_file": "loom_main.dump",
    "format": "pg_dump_custom"
  },
  "paths": {
    "object_store": "object-store",
    "private_backups": "private-backups",
    "main_documents": "main-documents",
    "storage_archive": "storage-archive"
  },
  "policies": {
    "main_box": "not_copied_requires_explicit_watched_roots_or_private_backups"
  }
}
JSON

cat >"$TMP_DIR/cloud.json" <<JSON
{
  "schema_version": "loom.cloud.config.v0.6.4",
  "enabled": true,
  "provider": "local_borg_smoke",
  "driver": "rclone",
  "remote_name": "loom-cloud",
  "remote_root": "loom",
  "rclone_config_path": "$TMP_DIR/rclone.conf",
  "rclone_binary": "rclone",
  "state_dir": "$STATE_DIR",
  "roots": {
    "main_snapshots": "main-snapshots",
    "full_offload": "full-offload",
    "cloud_folder": "cloud-folder"
  },
  "snapshots": {
    "backend": "borg",
    "borg": {
      "binary": "borg",
      "repository": "$BORG_REPO",
      "passphrase_file": "$BORG_PASSPHRASE_FILE",
      "cache_dir": "$STATE_DIR/borg/cache",
      "security_dir": "$STATE_DIR/borg/security",
      "encryption": "repokey-blake2",
      "compression": "none",
      "check_mode": "repository"
    }
  }
}
JSON
printf '[loom-cloud]\ntype = sftp\n' >"$TMP_DIR/rclone.conf"
chmod 0600 "$TMP_DIR/rclone.conf"

export LOOM_ENV=production
export LOOM_NODE_ID=loom-main
export LOOM_NODE_KIND=main
export LOOM_NODE_ROLE=main
export LOOM_RUNTIME_CLASS=main_full
export LOOM_DATA_DIR="$DATA_DIR"
export LOOM_OBJECT_STORE="$DATA_DIR/object-store"
export LOOM_STORAGE_EXPORT_ROOT="$DATA_DIR/storage-views/main-export"
export LOOM_MAIN_DOCUMENTS_ROOT="$DATA_DIR/main-documents"
export LOOM_SOCKET_PATH="$TMP_DIR/run/loomd.sock"
export LOOM_MIGRATIONS_DIR="$ROOT_DIR/migrations"

init_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot backend init --cloud-config "$TMP_DIR/cloud.json" --confirm)"
printf '%s\n' "$init_json" | jq -e '.ok == true and (.data.status == "succeeded" or .data.status == "already_initialized") and .data.initialized == true' >/dev/null \
  || fail "borg backend init did not initialize repository"
pass "borg backend init initializes repository"

status_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot backend status --cloud-config "$TMP_DIR/cloud.json")"
printf '%s\n' "$status_json" | jq -e '.ok == true and .data.backend == "borg" and .data.initialized == true' >/dev/null \
  || fail "borg backend status did not report initialized repository"
pass "borg backend status reports initialized repository"

dry_run_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot push --cloud-config "$TMP_DIR/cloud.json" --backup latest --backup-root "$DATA_DIR/backups/main" --data-dir "$DATA_DIR" --dry-run)"
printf '%s\n' "$dry_run_json" | jq -e '.ok == true and .data.status == "planned" and .data.backend == "borg" and .data.archive == "loom-main-20260614T100000Z-smoke"' >/dev/null \
  || fail "borg snapshot dry-run did not plan archive"
pass "borg snapshot dry-run plans archive"

push_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot push --cloud-config "$TMP_DIR/cloud.json" --backup latest --backup-root "$DATA_DIR/backups/main" --data-dir "$DATA_DIR")"
printf '%s\n' "$push_json" | jq -e '.ok == true and .data.status == "succeeded" and .data.backend == "borg" and .data.manifest.backend == "borg" and .data.manifest.schema_version == "loom.cloud.snapshot_upload.v0.6.4"' >/dev/null \
  || fail "borg snapshot push did not create archive and manifest"
pass "borg snapshot push creates archive and local manifest"

list_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot list --cloud-config "$TMP_DIR/cloud.json")"
printf '%s\n' "$list_json" | jq -e '.ok == true and .data.status == "ok" and .data.latest.archive == "loom-main-20260614T100000Z-smoke"' >/dev/null \
  || fail "borg snapshot list did not return archive"
pass "borg snapshot list returns archive"

verify_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot verify --cloud-config "$TMP_DIR/cloud.json" latest)"
printf '%s\n' "$verify_json" | jq -e '.ok == true and .data.status == "succeeded" and .data.backend == "borg"' >/dev/null \
  || fail "borg snapshot verify failed"
pass "borg snapshot verify succeeds"

fetch_dir="$TMP_DIR/fetched"
fetch_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot fetch --cloud-config "$TMP_DIR/cloud.json" latest --to "$fetch_dir")"
printf '%s\n' "$fetch_json" | jq -e '.ok == true and .data.status == "succeeded" and .data.verification.status == "succeeded"' >/dev/null \
  || fail "borg snapshot fetch did not extract and verify backup"
pass "borg snapshot fetch extracts and verifies backup"

drill_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot restore-drill --local --cloud-config "$TMP_DIR/cloud.json" latest --dry-run)"
printf '%s\n' "$drill_json" | jq -e '.ok == true and .data.status == "planned" and .data.backend == "borg" and .data.plan.status == "planned"' >/dev/null \
  || fail "borg snapshot restore-drill --dry-run did not plan from fetched snapshot"
pass "borg snapshot restore-drill dry-run plans from fetched snapshot"

push_new_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot push --cloud-config "$TMP_DIR/cloud.json" --backup "$BACKUP_DIR_2" --backup-root "$DATA_DIR/backups/main" --data-dir "$DATA_DIR")"
printf '%s\n' "$push_new_json" | jq -e '.ok == true and .data.status == "succeeded" and .data.archive == "loom-main-20260614T110000Z-smoke-new"' >/dev/null \
  || fail "second borg snapshot push failed"
pass "second borg snapshot push creates newer archive"

retention_plan_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot retention plan --cloud-config "$TMP_DIR/cloud.json" --keep-latest 1)"
printf '%s\n' "$retention_plan_json" | jq -e '.ok == true and .data.backend == "borg" and (.data.remove | length) == 1 and .data.remove[0].archive == "loom-main-20260614T100000Z-smoke" and .data.remove[0].action == "delete_archive"' >/dev/null \
  || fail "borg retention plan did not select old archive"
pass "borg retention plan selects old archive"

retention_apply_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot retention apply --cloud-config "$TMP_DIR/cloud.json" --keep-latest 1 --confirm)"
printf '%s\n' "$retention_apply_json" | jq -e '.ok == true and .data.status == "succeeded" and (.data.moved | length) == 1 and .data.moved[0].archive == "loom-main-20260614T100000Z-smoke"' >/dev/null \
  || fail "borg retention apply did not delete old archive"
pass "borg retention apply deletes old archive"

printf '[ok] v0.6.4 packed cloud backup local smoke passed\n'

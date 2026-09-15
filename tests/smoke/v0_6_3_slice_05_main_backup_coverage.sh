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

DATA_DIR="$TMP_DIR/data"
BACKUP_DIR="$DATA_DIR/backups/main/20260614T100000Z-smoke"
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
  "$BACKUP_DIR/storage-archive/manifests"

printf 'hello\n' >"$DATA_DIR/main-documents/.loom-acceptance.md"
printf '{"ok":true}\n' >"$BACKUP_DIR/health.json"
printf 'database dump\n' >"$BACKUP_DIR/loom_main.dump"
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

coverage_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json backup coverage \
  --data-dir "$DATA_DIR" \
  --backup-root "$DATA_DIR/backups/main" \
  --manifest "$BACKUP_DIR/manifest.json")"
printf '%s\n' "$coverage_json" | jq -e '
  .ok == true and
  .data.schema_version == "loom.backup.coverage.v0.6.3" and
  .data.status == "ok" and
  (.data.entries[] | select(.key == "main-documents" and .status == "covered")) and
  (.data.entries[] | select(.key == "latest-storage-archive" and .status == "covered"))
' >/dev/null || fail "backup coverage JSON did not report covered main-documents and storage-archive"
pass "backup coverage reports durable main roots"

verify_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json backup verify "$BACKUP_DIR")"
printf '%s\n' "$verify_json" | jq -e '
  .ok == true and
  .data.status == "succeeded" and
  .data.checks.main_documents_snapshot == "succeeded" and
  .data.checks.storage_archive_snapshot == "succeeded"
' >/dev/null || fail "backup verify did not validate main-documents and storage-archive"
pass "backup verify validates expanded backup manifest paths"

drill_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json backup restore-drill "$BACKUP_DIR" --dry-run)"
printf '%s\n' "$drill_json" | jq -e '.ok == true and .data.status == "planned"' >/dev/null \
  || fail "backup restore-drill --dry-run did not accept expanded backup layout"
pass "restore drill recognizes expanded backup layout"

dry_run_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json backup create --production --dry-run)"
printf '%s\n' "$dry_run_json" | jq -e '
  .ok == true and
  (.data.captures | index("main-documents snapshot")) and
  (.data.captures | index("storage-archive snapshot"))
' >/dev/null || fail "backup create --dry-run does not list expanded capture roots"
pass "backup create dry-run announces expanded capture roots"

require_file_contains "$ROOT_DIR/internal/workers/runtimes/main_backup.go" "IncludeMainDocuments"
require_file_contains "$ROOT_DIR/internal/maintenance/backup_manifest.go" "MainDocuments"
require_file_contains "$ROOT_DIR/internal/backupcoverage/coverage.go" "loom.backup.coverage.v0.6.3"
require_file_contains "$ROOT_DIR/notes/Operations - Backup And Restore.md" "/var/lib/loom/main-documents"
pass "slice 05 implementation files contain expected wiring"

printf '[ok] v0.6.3 slice 05 main backup coverage smoke passed\n'

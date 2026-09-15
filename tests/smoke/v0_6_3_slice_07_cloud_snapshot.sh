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

DATA_DIR="$TMP_DIR/data"
BACKUP_DIR="$DATA_DIR/backups/main/20260614T100000Z-smoke"
REMOTE_DIR="$TMP_DIR/remote"
STATE_DIR="$TMP_DIR/cloud-state"
FAKE_BIN="$TMP_DIR/bin"

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
  "$REMOTE_DIR/loom/main-snapshots" \
  "$REMOTE_DIR/loom/full-offload" \
  "$REMOTE_DIR/loom/cloud-folder" \
  "$STATE_DIR" \
  "$FAKE_BIN"

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

cat >"$FAKE_BIN/rclone" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

remote_root="${LOOM_FAKE_RCLONE_REMOTE:?}"

translate() {
  local uri="$1"
  case "$uri" in
    loom-cloud:*) printf '%s/%s\n' "$remote_root" "${uri#loom-cloud:}" ;;
    *) printf '%s\n' "$uri" ;;
  esac
}

while [[ "${1:-}" == "--config" ]]; do
  shift 2
done
cmd="${1:-}"
shift || true

opts=()
while [[ "${1:-}" == --* ]]; do
  opts+=("$1")
  shift
done

case "$cmd" in
  lsf)
    target="$(translate "$1")"
    [[ -d "$target" ]] || exit 1
    shopt -s nullglob
    for child in "$target"/*; do
      name="$(basename "$child")"
      if [[ -d "$child" ]]; then
        printf '%s/\n' "$name"
      else
        printf '%s\n' "$name"
      fi
    done
    ;;
  copy)
    src="$1"
    dst="$2"
    if [[ "$src" == loom-cloud:* ]]; then
      src_path="$(translate "$src")"
      mkdir -p "$dst"
      cp -R "$src_path"/. "$dst"/
    else
      dst_path="$(translate "$dst")"
      mkdir -p "$dst_path"
      cp -R "$src"/. "$dst_path"/
    fi
    ;;
  copyto)
    src="$1"
    dst="$2"
    if [[ "$src" == loom-cloud:* ]]; then
      src_path="$(translate "$src")"
      mkdir -p "$(dirname "$dst")"
      cp "$src_path" "$dst"
    else
      dst_path="$(translate "$dst")"
      mkdir -p "$(dirname "$dst_path")"
      cp "$src" "$dst_path"
    fi
    ;;
  check)
    src="$1"
    dst_path="$(translate "$2")"
    diff -qr "$src" "$dst_path" >/dev/null
    ;;
  moveto)
    src_path="$(translate "$1")"
    dst_path="$(translate "$2")"
    mkdir -p "$(dirname "$dst_path")"
    rm -rf "$dst_path"
    mv "$src_path" "$dst_path"
    ;;
  *)
    printf 'unexpected rclone command: %s %s\n' "$cmd" "$*" >&2
    exit 2
    ;;
esac
SH
chmod +x "$FAKE_BIN/rclone"

cat >"$TMP_DIR/rclone.conf" <<'CONF'
[loom-cloud]
type = sftp
CONF
chmod 0600 "$TMP_DIR/rclone.conf"

cat >"$TMP_DIR/cloud.json" <<JSON
{
  "schema_version": "loom.cloud.config.v0.6.3",
  "enabled": true,
  "provider": "hetzner_storage_box",
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
  }
}
JSON

export PATH="$FAKE_BIN:$PATH"
export LOOM_FAKE_RCLONE_REMOTE="$REMOTE_DIR"
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

dry_run_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot push --cloud-config "$TMP_DIR/cloud.json" --backup latest --backup-root "$DATA_DIR/backups/main" --data-dir "$DATA_DIR" --dry-run)"
printf '%s\n' "$dry_run_json" | jq -e '.ok == true and .data.status == "planned" and .data.verification.status == "succeeded"' >/dev/null \
  || fail "cloud snapshot push dry-run did not plan verified backup"
pass "cloud snapshot push dry-run plans verified backup"

push_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot push --cloud-config "$TMP_DIR/cloud.json" --backup latest --backup-root "$DATA_DIR/backups/main" --data-dir "$DATA_DIR")"
printf '%s\n' "$push_json" | jq -e '.ok == true and .data.status == "succeeded" and .data.manifest.schema_version == "loom.cloud.snapshot_upload.v0.6.4" and .data.manifest.backend == "legacy_tree"' >/dev/null \
  || fail "cloud snapshot push did not upload and write manifest"
pass "cloud snapshot push uploads and writes cloud manifest"

list_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot list --cloud-config "$TMP_DIR/cloud.json")"
printf '%s\n' "$list_json" | jq -e '.ok == true and .data.status == "ok" and (.data.snapshots | length) == 1 and .data.latest.ref == "20260614T100000Z-smoke"' >/dev/null \
  || fail "cloud snapshot list did not return uploaded snapshot"
pass "cloud snapshot list returns uploaded snapshot"

verify_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot verify --cloud-config "$TMP_DIR/cloud.json" latest)"
printf '%s\n' "$verify_json" | jq -e '.ok == true and .data.status == "succeeded" and .data.manifest.schema_version == "loom.cloud.snapshot_upload.v0.6.4" and .data.manifest.backend == "legacy_tree"' >/dev/null \
  || fail "cloud snapshot verify did not read acceptance manifest"
pass "cloud snapshot verify reads acceptance manifest"

fetch_dir="$TMP_DIR/fetched"
fetch_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot fetch --cloud-config "$TMP_DIR/cloud.json" latest --to "$fetch_dir")"
printf '%s\n' "$fetch_json" | jq -e '.ok == true and .data.status == "succeeded" and .data.verification.status == "succeeded"' >/dev/null \
  || fail "cloud snapshot fetch did not download and verify backup"
pass "cloud snapshot fetch downloads and verifies backup"

drill_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot restore-drill --local --cloud-config "$TMP_DIR/cloud.json" latest --dry-run)"
printf '%s\n' "$drill_json" | jq -e '.ok == true and .data.status == "planned" and .data.plan.status == "planned"' >/dev/null \
  || fail "cloud snapshot restore-drill --dry-run did not plan from fetched snapshot"
pass "cloud snapshot restore-drill dry-run plans from fetched snapshot"

printf '[ok] v0.6.3 slice 07 cloud snapshot smoke passed\n'

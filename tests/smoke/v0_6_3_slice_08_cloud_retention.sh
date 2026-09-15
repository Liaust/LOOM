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
REMOTE_DIR="$TMP_DIR/remote"
STATE_DIR="$TMP_DIR/cloud-state"
FAKE_BIN="$TMP_DIR/bin"

mkdir -p \
  "$DATA_DIR/object-store" \
  "$DATA_DIR/private-backups" \
  "$DATA_DIR/main-documents" \
  "$DATA_DIR/storage-archive/objects" \
  "$DATA_DIR/storage-archive/manifests" \
  "$REMOTE_DIR/loom/main-snapshots" \
  "$REMOTE_DIR/loom/full-offload" \
  "$REMOTE_DIR/loom/cloud-folder" \
  "$STATE_DIR" \
  "$FAKE_BIN"

create_backup() {
  local ref="$1"
  local backup_dir="$DATA_DIR/backups/main/$ref"
  mkdir -p \
    "$backup_dir/object-store" \
    "$backup_dir/private-backups" \
    "$backup_dir/main-documents" \
    "$backup_dir/storage-archive/objects" \
    "$backup_dir/storage-archive/manifests"
  printf '{"ok":true}\n' >"$backup_dir/health.json"
  printf 'database dump %s\n' "$ref" >"$backup_dir/loom_main.dump"
  cat >"$backup_dir/manifest.json" <<JSON
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
}

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

while [[ "${1:-}" == --* ]]; do
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

for ref in 20260614T100000Z-old 20260614T110000Z-mid 20260614T120000Z-new; do
  create_backup "$ref"
  push_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot push --cloud-config "$TMP_DIR/cloud.json" --backup "$ref" --backup-root "$DATA_DIR/backups/main" --data-dir "$DATA_DIR")"
  printf '%s\n' "$push_json" | jq -e '.ok == true and .data.status == "succeeded"' >/dev/null \
    || fail "push failed for $ref"
done
pass "uploaded three cloud snapshots"

mkdir -p "$REMOTE_DIR/loom/main-snapshots/loom-main/not-a-loom-snapshot"
printf 'not a manifest\n' >"$REMOTE_DIR/loom/main-snapshots/loom-main/not-a-loom-snapshot/readme.txt"

plan_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot retention plan --cloud-config "$TMP_DIR/cloud.json" --keep-latest 2)"
printf '%s\n' "$plan_json" | jq -e '.ok == true and .data.status == "planned" and (.data.remove | length) == 1 and .data.remove[0].ref == "20260614T100000Z-old" and (.data.ignored | length) == 1' >/dev/null \
  || fail "retention plan did not keep latest two and ignore unknown folder"
pass "retention plan keeps latest two and ignores unknown folders"

set +e
no_confirm_output="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot retention apply --cloud-config "$TMP_DIR/cloud.json" --keep-latest 2 2>&1)"
no_confirm_status=$?
set -e
if [[ "$no_confirm_status" -eq 0 ]]; then
  fail "retention apply without --confirm unexpectedly succeeded: $no_confirm_output"
fi
pass "retention apply requires explicit confirmation"

apply_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud snapshot retention apply --cloud-config "$TMP_DIR/cloud.json" --keep-latest 2 --confirm)"
printf '%s\n' "$apply_json" | jq -e '.ok == true and .data.status == "succeeded" and (.data.moved | length) == 1 and .data.moved[0].ref == "20260614T100000Z-old"' >/dev/null \
  || fail "retention apply did not move old snapshot"

if [[ -d "$REMOTE_DIR/loom/main-snapshots/loom-main/20260614T100000Z-old" ]]; then
  fail "old snapshot still exists in active snapshot root"
fi
if [[ ! -d "$REMOTE_DIR/loom/main-snapshots/loom-main/20260614T120000Z-new" ]]; then
  fail "latest snapshot was moved unexpectedly"
fi
if [[ ! -d "$REMOTE_DIR/loom/main-snapshots/loom-main/not-a-loom-snapshot" ]]; then
  fail "unknown folder was moved unexpectedly"
fi
if [[ -z "$(find "$REMOTE_DIR/loom/_system/retention/trash" -type d -name '20260614T100000Z-old' -print -quit)" ]]; then
  fail "old snapshot was not moved into retention trash"
fi
pass "retention apply moves only old valid snapshots into trash"

printf '[ok] v0.6.3 slice 08 cloud retention smoke passed\n'

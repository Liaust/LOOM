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
SOURCE_DIR="$DATA_DIR/storage-archive/objects/.loom-acceptance/cloud-offload-demo"
FETCH_DIR="$TMP_DIR/fetched"

mkdir -p \
  "$SOURCE_DIR/nested" \
  "$REMOTE_DIR/loom/full-offload" \
  "$REMOTE_DIR/loom/main-snapshots" \
  "$REMOTE_DIR/loom/cloud-folder" \
  "$STATE_DIR" \
  "$FAKE_BIN"

printf 'cloud offload payload\n' >"$SOURCE_DIR/payload.txt"
printf 'nested payload\n' >"$SOURCE_DIR/nested/child.txt"

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

dry_run=0
checksum=0
while [[ "${1:-}" == --* ]]; do
  case "$1" in
    --dry-run) dry_run=1 ;;
    --checksum) checksum=1 ;;
  esac
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
    [[ "$dry_run" == 1 ]] && exit 0
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
    [[ "$dry_run" == 1 ]] && exit 0
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
export LOOM_SOCKET_PATH="$TMP_DIR/run/loomd.sock"
export LOOM_MIGRATIONS_DIR="$ROOT_DIR/migrations"

plan_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json storage cloud-offload plan "$SOURCE_DIR" --cloud-config "$TMP_DIR/cloud.json" --node-id loom-main)"
printf '%s\n' "$plan_json" | jq -e '.ok == true and .data.status == "planned" and .data.file_count == 2 and .data.custody == "local_primary"' >/dev/null \
  || fail "cloud offload plan did not report expected dry plan: $plan_json"
remote_prefix="$(printf '%s\n' "$plan_json" | jq -r '.data.remote_prefix')"
pass "cloud offload plan reports remote prefix and source inventory"

set +e
no_confirm_output="$(cd "$ROOT_DIR" && go run ./cmd/loom --json storage cloud-offload apply "$SOURCE_DIR" --cloud-config "$TMP_DIR/cloud.json" --node-id loom-main 2>&1)"
no_confirm_status=$?
set -e
if [[ "$no_confirm_status" -eq 0 ]]; then
  fail "cloud offload apply without --confirm unexpectedly succeeded: $no_confirm_output"
fi
pass "cloud offload apply requires explicit confirmation"

apply_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json storage cloud-offload apply "$SOURCE_DIR" --cloud-config "$TMP_DIR/cloud.json" --node-id loom-main --confirm)"
printf '%s\n' "$apply_json" | jq -e '.ok == true and .data.status == "succeeded" and .data.custody == "replicated_to_cloud" and .data.local_source_action == "retain_until_manual_delete"' >/dev/null \
  || fail "cloud offload apply did not succeed: $apply_json"
apply_remote_prefix="$(printf '%s\n' "$apply_json" | jq -r '.data.remote_prefix')"
remote_payload="$(printf '%s\n' "$apply_json" | jq -r '.data.payload_remote_prefix')"
test -f "$REMOTE_DIR/loom/$remote_payload/payload.txt" || fail "remote payload file missing"
test -f "$REMOTE_DIR/loom/$apply_remote_prefix/offload-manifest.json" || fail "remote offload manifest missing"
test -f "$SOURCE_DIR/payload.txt" || fail "local source should not be deleted by cloud offload"
pass "cloud offload apply uploads payload and manifest while retaining local source"

status_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json storage cloud-status "$apply_remote_prefix" --cloud-config "$TMP_DIR/cloud.json" --node-id loom-main)"
printf '%s\n' "$status_json" | jq -e '.ok == true and .data.status == "succeeded" and .data.manifest.custody == "replicated_to_cloud"' >/dev/null \
  || fail "cloud offload status did not read remote manifest: $status_json"
pass "cloud offload status reads remote manifest"

fetch_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json storage cloud-fetch "$apply_remote_prefix" --cloud-config "$TMP_DIR/cloud.json" --node-id loom-main --to "$FETCH_DIR")"
printf '%s\n' "$fetch_json" | jq -e '.ok == true and .data.status == "succeeded" and .data.target_path == "'"$FETCH_DIR"'"' >/dev/null \
  || fail "cloud offload fetch did not succeed: $fetch_json"
diff -qr "$SOURCE_DIR" "$FETCH_DIR" >/dev/null || fail "fetched offload payload does not match source"
pass "cloud offload fetch restores payload"

printf '[ok] v0.6.3 slice 12 cloud offload smoke passed\n'

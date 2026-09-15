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
  grep -F -- "$needle" "$path" >/dev/null || fail "$path missing: $needle"
}

FAKE_BIN="$TMP_DIR/bin"
mkdir -p "$FAKE_BIN"
cat >"$FAKE_BIN/rclone" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

cmd=""
for arg in "$@"; do
  case "$arg" in
    lsf|copy|copyto|check)
      cmd="$arg"
      break
      ;;
  esac
done

case "$cmd" in
  lsf)
    printf 'main-snapshots/\nfull-offload/\ncloud-folder/\n'
    ;;
  copy|copyto|check)
    printf 'ok\n'
    ;;
  *)
    printf 'unexpected rclone args: %s\n' "$*" >&2
    exit 2
    ;;
esac
SH
chmod +x "$FAKE_BIN/rclone"

mkdir -p "$TMP_DIR/etc/cloud" "$TMP_DIR/state"
printf '[loom-cloud]\ntype = sftp\n' >"$TMP_DIR/etc/cloud/rclone.conf"
chmod 0600 "$TMP_DIR/etc/cloud/rclone.conf"
cat >"$TMP_DIR/etc/cloud/config.json" <<JSON
{
  "schema_version": "loom.cloud.config.v0.6.3",
  "enabled": true,
  "provider": "hetzner_storage_box",
  "driver": "rclone",
  "remote_name": "loom-cloud",
  "remote_root": "loom",
  "rclone_config_path": "$TMP_DIR/etc/cloud/rclone.conf",
  "rclone_binary": "rclone",
  "state_dir": "$TMP_DIR/state",
  "roots": {
    "main_snapshots": "main-snapshots",
    "full_offload": "full-offload",
    "cloud_folder": "cloud-folder"
  }
}
JSON

export PATH="$FAKE_BIN:$PATH"
export LOOM_ENV=production
export LOOM_NODE_ID=loom-main
export LOOM_NODE_KIND=main
export LOOM_NODE_ROLE=main
export LOOM_RUNTIME_CLASS=main_full
export LOOM_DATA_DIR="$TMP_DIR/data"
export LOOM_OBJECT_STORE="$TMP_DIR/data/object-store"
export LOOM_STORAGE_EXPORT_ROOT="$TMP_DIR/data/storage-views/main-export"
export LOOM_MAIN_DOCUMENTS_ROOT="$TMP_DIR/data/main-documents"
export LOOM_SOCKET_PATH="$TMP_DIR/run/loomd.sock"
export LOOM_MIGRATIONS_DIR="$ROOT_DIR/migrations"

doctor_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud doctor --cloud-config "$TMP_DIR/etc/cloud/config.json")"
printf '%s\n' "$doctor_json" | jq -e '
  .ok == true and
  .data.schema_version == "loom.cloud.config.v0.6.3" and
  .data.status == "ok" and
  (.data.roots | length) == 3 and
  (.data.roots[] | select(.name == "main_snapshots" and .status == "present"))
' >/dev/null || fail "cloud doctor JSON did not report an ok fake remote"
pass "cloud doctor validates fake rclone remote"

status_json="$(cd "$ROOT_DIR" && go run ./cmd/loom --json cloud status --cloud-config "$TMP_DIR/etc/cloud/config.json")"
printf '%s\n' "$status_json" | jq -e '
  .ok == true and
  .data.status == "reachable" and
  .data.config.enabled == true and
  .data.config.remote_name == "loom-cloud"
' >/dev/null || fail "cloud status JSON did not report fake remote"
pass "cloud status reports reachable fake remote"

require_file_contains "$ROOT_DIR/internal/cloudstorage/doctor.go" "cloud.rclone.config"
require_file_contains "$ROOT_DIR/internal/cloudstorage/rclone_driver.go" "--config"
require_file_contains "$ROOT_DIR/nix/modules/loom-base.nix" "installRclone"
require_file_contains "$ROOT_DIR/nix/modules/loom-storage.nix" 'd ${cloudCfg.stateDir}/snapshots'
pass "slice 06 implementation files contain expected cloud foundation wiring"

printf '[ok] v0.6.3 slice 06 cloud foundation smoke passed\n'

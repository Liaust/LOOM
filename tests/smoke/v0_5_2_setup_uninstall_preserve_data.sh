#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

fail() {
  printf 'v0.5.2 setup uninstall preserve-data smoke: %s\n' "$*" >&2
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

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-5-2-uninstall-preserve.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

CONFIG_DIR="$TMP_DIR/config"
STATE_DIR="$TMP_DIR/state"
DATA_DIR="$TMP_DIR/data"
OBJECT_STORE="$TMP_DIR/object-store"
BOX_DIR="$TMP_DIR/LOOM Box"
SOCKET_PATH="$TMP_DIR/run/loomd.sock"
MANIFEST="$CONFIG_DIR/install.yaml"
mkdir -p "$CONFIG_DIR" "$STATE_DIR" "$DATA_DIR" "$OBJECT_STORE" "$BOX_DIR" "$(dirname "$SOCKET_PATH")"
printf 'LOOM_ENV=production\n' >"$CONFIG_DIR/loom.env"
printf 'socket\n' >"$SOCKET_PATH"

cat >"$MANIFEST" <<YAML
schema_version: loom.install.v0.5
install_id: install_preserve_smoke
installed_at: "2026-06-03T12:00:00Z"
updated_at: "2026-06-03T12:00:00Z"
setup_version: smoke
node_key: main-smoke
node_id: node_main_smoke
display_name: Main Smoke
node_kind: main
node_role: main
runtime_class: main_full
authority_profile: main_node_default
runtime_profile: main_full
install_mode: user
service_manager: none
package_mode: local-build
home_dir: "$TMP_DIR"
config_dir: "$CONFIG_DIR"
data_dir: "$DATA_DIR"
state_dir: "$STATE_DIR"
log_dir: "$TMP_DIR/log"
box_path: "$BOX_DIR"
box_profile: main
object_store_path: "$OBJECT_STORE"
socket_path: "$SOCKET_PATH"
YAML

loom_cli --json setup uninstall apply \
  --manifest "$MANIFEST" \
  --mode preserve-data \
  --allow-production-main \
  --yes \
  | jq -e '
      .mode == "preserve-data"
      and (.blocked | length) == 0
      and ([.changed[] | select(.id == "loomd_env")] | length) == 1
      and ([.changed[] | select(.id == "socket")] | length) == 1
      and ([.preserved[] | select(.id == "data_dir")] | length) == 1
      and ([.preserved[] | select(.id == "object_store")] | length) == 1
    ' >/dev/null

[[ ! -e "$CONFIG_DIR/loom.env" ]] || fail "loom.env was not removed"
[[ ! -e "$SOCKET_PATH" ]] || fail "socket was not removed"
[[ -d "$DATA_DIR" ]] || fail "data dir was removed"
[[ -d "$OBJECT_STORE" ]] || fail "object store was removed"
[[ -d "$BOX_DIR" ]] || fail "Box dir was removed"

loom_cli --json setup status --manifest "$MANIFEST" \
  | jq -e '.summary.status == "uninstalled_preserve_data"' >/dev/null

printf '[smoke] v0.5.2 setup uninstall preserve-data smoke passed\n'

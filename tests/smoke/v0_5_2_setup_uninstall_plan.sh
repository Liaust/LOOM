#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

fail() {
  printf 'v0.5.2 setup uninstall plan smoke: %s\n' "$*" >&2
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

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-5-2-uninstall-plan.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

CONFIG_DIR="$TMP_DIR/config"
STATE_DIR="$TMP_DIR/state"
MANIFEST="$CONFIG_DIR/install.yaml"
mkdir -p "$CONFIG_DIR" "$STATE_DIR" "$TMP_DIR/data"

cat >"$MANIFEST" <<YAML
schema_version: loom.install.v0.5
install_id: install_plan_smoke
installed_at: "2026-06-03T12:00:00Z"
updated_at: "2026-06-03T12:00:00Z"
setup_version: smoke
node_key: main-smoke
node_id: node_main_smoke
display_name: Main Smoke
node_kind: main
node_role: main
runtime_class: main_full
bootstrap_mode: production
authority_profile: main_node_default
runtime_profile: main_full
install_mode: user
service_manager: none
package_mode: local-build
home_dir: "$TMP_DIR"
config_dir: "$CONFIG_DIR"
data_dir: "$TMP_DIR/data"
state_dir: "$STATE_DIR"
log_dir: "$TMP_DIR/log"
object_store_path: "$TMP_DIR/object-store"
YAML

loom_cli --json setup uninstall plan --manifest "$MANIFEST" \
  | jq -e '
      .schema_version == "loom.setup.uninstall.v0.5.2"
      and .mode == "preserve-data"
      and .production == true
      and (.guardrails[] | select(.id == "allow_production_main" and .satisfied == false))
    ' >/dev/null

loom_cli --json setup uninstall plan --manifest "$MANIFEST" --mode disable-only \
  | jq -e '
      .mode == "disable-only"
      and ([.paths[] | select(.durable == true and .action == "preserve")] | length) >= 2
    ' >/dev/null

loom_cli --json setup uninstall plan --manifest "$MANIFEST" --mode purge \
  | jq -e '
      .mode == "purge"
      and (.guardrails[] | select(.id == "confirm_node" and .satisfied == false))
      and (.guardrails[] | select(.id == "production_backup_ref" and .satisfied == false))
    ' >/dev/null

printf '[smoke] v0.5.2 setup uninstall plan smoke passed\n'

#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

fail() {
  printf 'v0.5.2 setup uninstall disable-only smoke: %s\n' "$*" >&2
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

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-5-2-uninstall-disable.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

CONFIG_DIR="$TMP_DIR/config"
STATE_DIR="$TMP_DIR/state"
DATA_DIR="$TMP_DIR/data"
BOX_DIR="$TMP_DIR/LOOM Box"
MANIFEST="$CONFIG_DIR/install.yaml"
mkdir -p "$CONFIG_DIR" "$STATE_DIR" "$DATA_DIR" "$BOX_DIR"

cat >"$MANIFEST" <<YAML
schema_version: loom.install.v0.5
install_id: install_disable_smoke
installed_at: "2026-06-03T12:00:00Z"
updated_at: "2026-06-03T12:00:00Z"
setup_version: smoke
node_key: workspace-smoke
node_id: node_workspace_smoke
display_name: Workspace Smoke
node_kind: workspace
node_role: primary_workspace
runtime_class: workspace_full
authority_profile: primary_workspace_default
runtime_profile: workspace_full
install_mode: user
service_manager: none
package_mode: local-build
home_dir: "$TMP_DIR"
config_dir: "$CONFIG_DIR"
data_dir: "$DATA_DIR"
state_dir: "$STATE_DIR"
log_dir: "$TMP_DIR/log"
box_path: "$BOX_DIR"
box_profile: workspace
YAML

set +e
loom_cli --json setup uninstall apply \
  --manifest "$MANIFEST" \
  --mode disable-only \
  --no-interactive >"$TMP_DIR/refused.json" 2>"$TMP_DIR/refused.err"
status=$?
set -e
[[ "$status" -ne 0 ]] || fail "disable-only apply unexpectedly succeeded without --yes"
jq -e '.refused == true' "$TMP_DIR/refused.json" >/dev/null

loom_cli --json setup uninstall apply \
  --manifest "$MANIFEST" \
  --mode disable-only \
  --yes \
  | jq -e '
      .mode == "disable-only"
      and (.blocked | length) == 0
      and ([.preserved[] | select(.path | contains("LOOM Box"))] | length) == 1
    ' >/dev/null

[[ -d "$DATA_DIR" ]] || fail "data dir was removed"
[[ -d "$BOX_DIR" ]] || fail "Box dir was removed"

loom_cli --json setup status --manifest "$MANIFEST" \
  | jq -e '.summary.status == "disabled"' >/dev/null

printf '[smoke] v0.5.2 setup uninstall disable-only smoke passed\n'

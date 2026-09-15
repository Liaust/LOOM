#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

fail() {
  printf 'v0.5.2 setup uninstall purge guardrails smoke: %s\n' "$*" >&2
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

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-5-2-uninstall-purge.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

CONFIG_DIR="$TMP_DIR/config"
STATE_DIR="$TMP_DIR/state"
MANIFEST="$CONFIG_DIR/install.yaml"
mkdir -p "$CONFIG_DIR" "$STATE_DIR"

cat >"$MANIFEST" <<YAML
schema_version: loom.install.v0.5
install_id: install_smoke
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
data_dir: "$TMP_DIR"
state_dir: "$STATE_DIR"
log_dir: "$TMP_DIR/log"
YAML

loom_cli --json setup uninstall plan \
  --manifest "$MANIFEST" \
  --mode purge \
  --confirm-node workspace-smoke \
  --remove-db \
  | jq -e '
      .mode == "purge"
      and (.guardrails[] | select(.id == "safe_path_data_dir" and .satisfied == false))
    ' >/dev/null

set +e
loom_cli --json setup uninstall apply \
  --manifest "$MANIFEST" \
  --mode purge \
  --confirm-node workspace-smoke \
  --remove-db \
  --yes >"$TMP_DIR/apply.json" 2>"$TMP_DIR/apply.err"
status=$?
set -e

[[ "$status" -ne 0 ]] || fail "purge apply unexpectedly succeeded"

jq -e '
  .mode == "purge"
  and (.blocked[] | select(.id == "safe_path_data_dir"))
' "$TMP_DIR/apply.json" >/dev/null

printf '[smoke] v0.5.2 setup uninstall purge guardrails smoke passed\n'

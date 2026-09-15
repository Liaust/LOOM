#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

fail() {
  printf 'v0.5.2 setup uninstall decommission smoke: %s\n' "$*" >&2
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

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-5-2-uninstall-decommission.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

CONFIG_DIR="$TMP_DIR/config"
STATE_DIR="$TMP_DIR/state"
DATA_DIR="$TMP_DIR/data"
NODE_AGENT_DIR="$TMP_DIR/node-agent"
MANIFEST="$CONFIG_DIR/install.yaml"
mkdir -p "$CONFIG_DIR" "$STATE_DIR" "$DATA_DIR" "$NODE_AGENT_DIR"
printf '{}\n' >"$NODE_AGENT_DIR/config.json"
printf '{}\n' >"$NODE_AGENT_DIR/state.json"

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
main_url: http://127.0.0.1:8080
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
node_agent_config_path: "$NODE_AGENT_DIR/config.json"
node_agent_state_path: "$NODE_AGENT_DIR/state.json"
node_agent_data_dir: "$NODE_AGENT_DIR"
enrollment:
  status: credential_configured
credential:
  configured: true
  node_credential_id: node_cred_smoke
  credential_hint: node_cred_abc
YAML

loom_cli --json setup uninstall apply \
  --manifest "$MANIFEST" \
  --mode preserve-data \
  --skip-main-revoke \
  --remove-credentials \
  --yes \
  | jq -e '
      .mode == "preserve-data"
      and .decommission.status == "pending"
      and (.blocked | length) == 0
      and (.changed[] | select(.id == "decommission_main"))
    ' >/dev/null

[[ ! -e "$NODE_AGENT_DIR/config.json" ]] || fail "node-agent config was not removed"

loom_cli --json setup manifest inspect --manifest "$MANIFEST" \
  | jq -e '
      .manifest.last_status.status == "decommission_pending"
      and .manifest.credential.configured == false
    ' >/dev/null

printf '[smoke] v0.5.2 setup uninstall decommission smoke passed\n'

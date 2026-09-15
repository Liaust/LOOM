#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

fail() {
  printf 'v0.5.3 slice 02 launchd setup smoke: %s\n' "$*" >&2
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

TMP_ROOT="${TMPDIR:-/tmp}"
TMP_ROOT="${TMP_ROOT%/}"
TMP_DIR="$(mktemp -d "$TMP_ROOT/loom-v0-5-3-launchd-setup.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

HOME_DIR="$TMP_DIR/home/leonardo"
BOX_PATH="$HOME_DIR/LOOM Box"
MAIN_URL="http://10.44.0.2:8080"
PLIST_PATH="$HOME_DIR/Library/LaunchAgents/local.loom.node-agent.plist"
NODE_AGENT_CONFIG="$HOME_DIR/.config/loom-node-agent/config.json"
NODE_AGENT_STATE="$HOME_DIR/.local/state/loom-node-agent/state.json"
NODE_AGENT_DATA="$HOME_DIR/.local/state/loom-node-agent"

COMMON_ARGS=(
  setup
  plan
  --kind workspace
  --role primary_workspace
  --runtime-class workspace_full
  --node-key macbook
  --display-name "MacBook Primary Workspace"
  --main-url "$MAIN_URL"
  --install-mode service
  --service-manager launchd
  --home-dir "$HOME_DIR"
  --user-name leonardo
  --box-path "$BOX_PATH"
)

loom_cli --json "${COMMON_ARGS[@]}" \
  | jq -e \
    --arg home "$HOME_DIR" \
    --arg plist "$PLIST_PATH" \
    --arg cfg "$NODE_AGENT_CONFIG" \
    --arg state "$NODE_AGENT_STATE" \
    --arg data "$NODE_AGENT_DATA" '
      .spec.install_mode == "service"
      and .spec.service_manager == "launchd"
      and .paths.config_dir == ($home + "/.config/loom")
      and .paths.manifest_path == ($home + "/.config/loom/install.yaml")
      and .paths.launch_agent_plist_path == $plist
      and .paths.node_agent_config_path == $cfg
      and .paths.node_agent_state_path == $state
      and .paths.node_agent_data_dir == $data
      and ([.steps[] | select(.id == "install_service" and .privileged == false and .metadata.label == "local.loom.node-agent")] | length) == 1
    ' >/dev/null

APPLY_ARGS=("${COMMON_ARGS[@]}")
APPLY_ARGS[1]="apply"

loom_cli --json "${APPLY_ARGS[@]}" --dry-run \
  | jq -e \
    --arg plist "$PLIST_PATH" '
      .dry_run == true
      and .manifest.service_manager == "launchd"
      and .manifest.services[0].label == "local.loom.node-agent"
      and .manifest.services[0].path == $plist
      and ([.changed[] | select(.id == "write_launch_agent_plist" and .path == $plist)] | length) == 1
      and ([.changed[] | select(.id == "load_launch_agent")] | length) == 1
    ' >/dev/null

[[ ! -e "$PLIST_PATH" ]] || fail "dry-run wrote LaunchAgent plist"

printf '[smoke] v0.5.3 slice 02 launchd setup smoke passed\n'

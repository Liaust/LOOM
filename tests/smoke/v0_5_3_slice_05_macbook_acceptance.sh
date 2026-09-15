#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'v0.5.3 slice 05 MacBook acceptance: %s\n' "$*" >&2
  exit 1
}

pass() {
  printf '[ok] %s\n' "$*"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

HOME_DIR="${LOOM_WORKSPACE_HOME:-$HOME}"
NODE_KEY="${LOOM_WORKSPACE_NODE_KEY:-macbook}"
MAIN_URL="${LOOM_MAIN_URL:-http://10.44.0.2:8080}"
MAIN_SSH="${LOOM_MAIN_SSH:-loom-main}"
BOX_PATH="${LOOM_BOX_PATH:-$HOME_DIR/loom-box}"
LOOM_BIN="${LOOM_BIN:-$HOME_DIR/.local/bin/loom}"
NODE_AGENT_BIN="${LOOM_NODE_AGENT_BIN:-$HOME_DIR/.local/bin/loom-node-agent}"
CURRENT_PATH="${LOOM_WORKSPACE_CURRENT:-$HOME_DIR/.local/share/loom/current}"
UPDATE_STATE="${LOOM_WORKSPACE_UPDATE_STATE:-$HOME_DIR/.local/state/loom/update}"
MANIFEST_PATH="${LOOM_INSTALL_MANIFEST:-$HOME_DIR/.config/loom/install.yaml}"
NODE_AGENT_CONFIG="${LOOM_NODE_AGENT_CONFIG_PATH:-$HOME_DIR/.config/loom-node-agent/config.json}"
NODE_AGENT_STATE="${LOOM_NODE_AGENT_STATE_PATH:-$HOME_DIR/.local/state/loom-node-agent/state.json}"
NODE_AGENT_DATA="${LOOM_NODE_AGENT_DATA_DIR_PATH:-$HOME_DIR/.local/state/loom-node-agent}"
LAUNCH_AGENT_LABEL="${LOOM_LAUNCH_AGENT_LABEL:-local.loom.node-agent}"
LAUNCH_AGENT_TARGET="gui/$(id -u)/$LAUNCH_AGENT_LABEL"

require_command jq
require_command curl
require_command ssh
require_command launchctl
require_command readlink

[[ -x "$LOOM_BIN" ]] || fail "installed loom binary is not executable: $LOOM_BIN"
[[ -x "$NODE_AGENT_BIN" ]] || fail "installed loom-node-agent binary is not executable: $NODE_AGENT_BIN"
[[ -L "$CURRENT_PATH" ]] || fail "current release path is not a symlink: $CURRENT_PATH"
[[ -L "$LOOM_BIN" ]] || fail "loom binary is not a symlink: $LOOM_BIN"
[[ -L "$NODE_AGENT_BIN" ]] || fail "loom-node-agent binary is not a symlink: $NODE_AGENT_BIN"
[[ -f "$CURRENT_PATH/loom-release.yaml" ]] || fail "active release manifest missing: $CURRENT_PATH/loom-release.yaml"
[[ -f "$CURRENT_PATH/bin/loom" ]] || fail "active release loom binary missing"
[[ -f "$CURRENT_PATH/bin/loom-node-agent" ]] || fail "active release loom-node-agent binary missing"
pass "release layout and binary symlinks are present"

[[ -f "$MANIFEST_PATH" ]] || fail "install manifest missing: $MANIFEST_PATH"
[[ -f "$NODE_AGENT_CONFIG" ]] || fail "node-agent config missing: $NODE_AGENT_CONFIG"
[[ -f "$NODE_AGENT_STATE" ]] || fail "node-agent state missing: $NODE_AGENT_STATE"
[[ -d "$NODE_AGENT_DATA" ]] || fail "node-agent data dir missing: $NODE_AGENT_DATA"
pass "install manifest and node-agent files are present"

for dir in "$BOX_PATH" "$BOX_PATH/.loom" "$BOX_PATH/Projects" "$BOX_PATH/Notes" "$BOX_PATH/Documents" "$BOX_PATH/Dropzone" "$BOX_PATH/loom-lane"; do
  [[ -d "$dir" ]] || fail "LOOM Box directory missing: $dir"
done
pass "LOOM Box workspace directories are present"

curl -fsS "$MAIN_URL/v1/health" \
  | jq -e '.ok == true and .data.status == "ok"' >/dev/null
pass "direct private main HTTP health is ok"

"$LOOM_BIN" --json setup status \
  --node-key "$NODE_KEY" \
  --kind workspace \
  --role primary_workspace \
  --runtime-class workspace_full \
  --main-url "$MAIN_URL" \
  --home-dir "$HOME_DIR" \
  --install-mode service \
  --service-manager launchd \
  | jq -e '
      .summary.status == "configured"
      and .manifest.state == "loaded"
      and .services[0].status == "running"
      and .box.status == "configured"
      and .node_agent.status == "configured"
      and .node_agent.credential_configured == true
      and .enrollment.credential_configured == true
      and .enrollment.verified_on_main == true
      and (.binaries[] | select(.name == "loom" and .status == "present"))
      and (.binaries[] | select(.name == "loom-node-agent" and .status == "present"))
    ' >/dev/null
pass "setup status is configured"

"$LOOM_BIN" --json setup doctor \
  --node-key "$NODE_KEY" \
  --kind workspace \
  --role primary_workspace \
  --runtime-class workspace_full \
  --main-url "$MAIN_URL" \
  --home-dir "$HOME_DIR" \
  --install-mode service \
  --service-manager launchd \
  | jq -e '
      .summary == "configured"
      and (((.findings // []) | map(select(.severity == "blocking" or .severity == "error")) | length) == 0)
    ' >/dev/null
pass "setup doctor has no blocking/error findings"

launchctl print "$LAUNCH_AGENT_TARGET" \
  | grep -q 'state = running'
pass "LaunchAgent is loaded and running"

"$NODE_AGENT_BIN" --json status \
  | jq -e '
      .ok == true
      and .data.config.node_key == "'"$NODE_KEY"'"
      and .data.config.main_url == "'"$MAIN_URL"'"
      and .data.credential_configured == true
      and (.data.runtime.workers | length) >= 1
    ' >/dev/null
pass "node-agent status succeeds"

"$NODE_AGENT_BIN" --json heartbeat --once \
  | jq -e '.ok == true and .data.presence_state == "online" and (.data.node_heartbeat_id | length > 0)' >/dev/null
pass "node-agent heartbeat succeeds"

ssh "$MAIN_SSH" "loom --json node health '$NODE_KEY'" \
  | jq -e '
      .ok == true
      and .data.node.node_key == "'"$NODE_KEY"'"
      and .data.node.presence_state == "online"
      and (.data.last_heartbeat.node_heartbeat_id | length > 0)
    ' >/dev/null
pass "main node health reports MacBook online"

ssh "$MAIN_SSH" 'loom --json node list' \
  | jq -e '
      .ok == true
      and ([.data[]? | select(.node_key == "'"$NODE_KEY"'" and .presence_state == "online")] | length) == 1
    ' >/dev/null
pass "main CLI can list the MacBook node"

"$LOOM_BIN" --json update workspace status --state-dir "$UPDATE_STATE" \
  | jq -e '.state_dir == "'"$UPDATE_STATE"'"' >/dev/null
pass "workspace update status is readable"

"$LOOM_BIN" --json update workspace plan \
  --release-path "$CURRENT_PATH" \
  --skip-service-restart \
  --skip-health-check \
  | jq -e '
      .schema_version == "loom.update.workspace.v0.5.3"
      and .backup.required == false
      and .rollback.class == "service_only_possible"
      and ([.steps[].id] | index("nixos_rebuild") | not)
    ' >/dev/null
pass "installed workspace update command plans without main-node gates"

printf 'v0.5.3 slice 05 MacBook acceptance passed.\n'

#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_WORKSPACE_HOST:-loom-workspace}"
REMOTE_DIR="${LOOM_WORKSPACE_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-5-slice-07-$(date +%s)-$RANDOM"
pass_count=0

log() {
  printf '[smoke] %s\n' "$*"
}

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$*"
}

remote() {
  ssh -o BatchMode=yes "$HOST" "$@"
}

check_remote() {
  local label="$1"
  local command="$2"
  remote "$command" >/dev/null
  pass "$label"
}

check_json() {
  local label="$1"
  local command="$2"
  local filter="$3"
  remote "$command | jq -e '$filter' >/dev/null"
  pass "$label"
}

log "target: $HOST"
log "remote dir: $REMOTE_DIR"
log "run id: $RUN_ID"

check_remote "SSH target reachable" 'hostname'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-5-slice-07.XXXXXX'")"
cleanup() {
  remote "rm -rf '$TMP_DIR'" || true
}
trap cleanup EXIT

HOME_DIR="$TMP_DIR/home/loomadmin"
BOX_PATH="$HOME_DIR/LOOM Box"
CONFIG_DIR="$TMP_DIR/config"
DATA_DIR="$TMP_DIR/data"
STATE_DIR="$TMP_DIR/state"
LOG_DIR="$TMP_DIR/logs"
MANIFEST="$CONFIG_DIR/install.yaml"
NODE_AGENT_CONFIG="$HOME_DIR/.config/loom-node-agent/config.json"
NODE_AGENT_STATE="$HOME_DIR/.local/state/loom-node-agent/state.json"
NODE_AGENT_DATA="$HOME_DIR/.local/state/loom-node-agent"
MAIN_URL="http://10.44.0.2:8080"

APPLY_ARGS="
  --kind workspace
  --role primary_workspace
  --runtime-class workspace_full
  --node-key workspace-slice-07
  --display-name 'Workspace Slice 07'
  --main-url '$MAIN_URL'
  --install-mode user
  --service-manager none
  --home-dir '$HOME_DIR'
  --user-name loomadmin
  --config-dir '$CONFIG_DIR'
  --data-dir '$DATA_DIR'
  --state-dir '$STATE_DIR'
  --log-dir '$LOG_DIR'
  --box-path '$BOX_PATH'
  --box-profile workspace
  --manifest '$MANIFEST'
"

check_json "workspace setup apply dry-run previews local node preparation" \
  "loom --json setup apply $APPLY_ARGS --dry-run" \
  ".dry_run == true and (.changed | length) > 0 and .manifest.node_kind == \"workspace\" and .manifest.enrollment.status == \"skipped\""

check_remote "dry-run did not write non-main manifest" "test ! -e '$MANIFEST'"

check_json "workspace setup apply writes local Box and node-agent state" \
  "loom --json setup apply $APPLY_ARGS --yes" \
  ".dry_run == false and .manifest.node_kind == \"workspace\" and .manifest.node_agent_config_path == \"$NODE_AGENT_CONFIG\" and .manifest.enrollment.status == \"skipped\" and .manifest.credential.configured == false"

check_remote "workspace setup apply created Box and node-agent paths" "
  test -d '$CONFIG_DIR'
  test -d '$DATA_DIR'
  test -d '$STATE_DIR'
  test -d '$LOG_DIR'
  test -d '$BOX_PATH/Projects'
  test -d '$BOX_PATH/Notes'
  test -d '$BOX_PATH/Documents'
  test -d '$BOX_PATH/Dropzone'
  test -d '$BOX_PATH/.loom/storage/dropzone'
  test -f '$NODE_AGENT_CONFIG'
  test -f '$NODE_AGENT_STATE'
  test -d '$NODE_AGENT_DATA/workers/instances'
  test -f '$MANIFEST'
"

check_json "node-agent config records main URL and Box safe root" \
  "jq '.' '$NODE_AGENT_CONFIG'" \
  ".main_url == \"$MAIN_URL\" and .node_key == \"workspace-slice-07\" and ([.filesystem.safe_roots[] | select(.root_key == \"loom_box\" and .absolute_path == \"$BOX_PATH\" and .allow_ingest == true)] | length) == 1"

check_remote "runtime workers include heartbeat, dropzone, and Box watched roots" "
  test -f '$NODE_AGENT_DATA/workers/instances/node-agent.node_heartbeat.json'
  test -f '$NODE_AGENT_DATA/workers/instances/node-agent.dropzone_transfer.json'
  test -f '$NODE_AGENT_DATA/workers/instances/node-agent.watched_root.loom_box__notes.json'
  test -f '$NODE_AGENT_DATA/workers/instances/node-agent.watched_root.loom_box__documents.json'
"

check_json "workspace setup apply is idempotent for node-agent config" \
  "loom --json setup apply $APPLY_ARGS --yes" \
  ".manifest.node_kind == \"workspace\" and ([.skipped[]? | select(.id == \"write_node_agent_config\" and .status == \"already_satisfied\")] | length) == 1"

check_json "setup status inspects locally prepared workspace" \
  "loom --json setup status --manifest '$MANIFEST'" \
  ".manifest.exists == true and .node.node_kind == \"workspace\" and .node_agent.config_exists == true and .enrollment.status == \"skipped\""

check_json "setup doctor treats skipped enrollment as non-blocking" \
  "loom --json setup doctor --manifest '$MANIFEST'" \
  ".status.manifest.exists == true and .status.node.node_kind == \"workspace\" and ([.findings[]? | select(.severity == \"blocking\")] | length) == 0"

log "completed $pass_count checks"

#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-5-slice-06-$(date +%s)-$RANDOM"
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

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-5-slice-06.XXXXXX'")"
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
OBJECT_STORE="$TMP_DIR/object-store"
SOCKET_PATH="$TMP_DIR/run/loomd.sock"
MANIFEST="$CONFIG_DIR/install.yaml"

APPLY_ARGS="
  --kind main
  --role main
  --runtime-class main_full
  --node-key main
  --display-name 'Main'
  --install-mode user
  --service-manager none
  --home-dir '$HOME_DIR'
  --user-name loomadmin
  --config-dir '$CONFIG_DIR'
  --data-dir '$DATA_DIR'
  --state-dir '$STATE_DIR'
  --log-dir '$LOG_DIR'
  --object-store '$OBJECT_STORE'
  --socket-path '$SOCKET_PATH'
  --box-path '$BOX_PATH'
  --box-profile main
  --manifest '$MANIFEST'
"

check_json "main setup apply dry-run previews mutations" \
  "loom --json setup apply $APPLY_ARGS --dry-run" \
  ".dry_run == true and (.changed | length) > 0 and .manifest.node_kind == \"main\" and .manifest.box_profile == \"main\""

check_remote "dry-run did not write manifest" "test ! -e '$MANIFEST'"

check_json "main setup apply writes local state" \
  "loom --json setup apply $APPLY_ARGS --yes" \
  ".dry_run == false and .manifest.node_kind == \"main\" and .manifest.box_path == \"$BOX_PATH\" and .manifest.last_status.status == \"partial\""

check_remote "main setup apply created expected paths" "
  test -d '$CONFIG_DIR'
  test -d '$DATA_DIR'
  test -d '$OBJECT_STORE/temp'
  test -d '$BOX_PATH/Projects'
  test -d '$BOX_PATH/.loom/storage/dropzone'
  test -d '$BOX_PATH/.loom/logs'
  test -f '$CONFIG_DIR/loom.env'
  test -f '$MANIFEST'
"

check_remote "service env records main identity and Box path" "
  grep -q 'LOOM_NODE_KIND=main' '$CONFIG_DIR/loom.env'
  grep -q 'LOOM_RUNTIME_CLASS=main_full' '$CONFIG_DIR/loom.env'
  grep -q 'LOOM_BOX_PROFILE=main' '$CONFIG_DIR/loom.env'
  grep -q 'LOOM_BOOTSTRAP_MODE=production' '$CONFIG_DIR/loom.env'
  grep -q \"LOOM_BOX_PATH=$BOX_PATH\" '$CONFIG_DIR/loom.env'
"

check_json "main setup apply is idempotent" \
  "loom --json setup apply $APPLY_ARGS --yes" \
  ".manifest.node_kind == \"main\" and ([.skipped[]? | select(.id == \"write_loomd_env\" and .status == \"already_satisfied\")] | length) == 1"

check_json "setup status inspects applied manifest" \
  "loom --json setup status --manifest '$MANIFEST'" \
  ".manifest.exists == true and .node.node_kind == \"main\" and (.summary.status == \"configured\" or .summary.status == \"partial\")"

check_json "setup doctor can inspect applied manifest" \
  "loom --json setup doctor --manifest '$MANIFEST'" \
  ".status.manifest.exists == true and .status.node.node_kind == \"main\" and ([.findings[]? | select(.severity == \"blocking\")] | length) == 0"

log "completed $pass_count checks"

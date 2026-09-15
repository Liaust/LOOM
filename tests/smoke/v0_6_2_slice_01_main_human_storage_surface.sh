#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
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

check_remote "SSH target reachable" 'hostname'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-6-2-slice-01.XXXXXX'")"
cleanup() {
  remote "rm -rf '$TMP_DIR'" || true
}
trap cleanup EXIT

HOME_DIR="$TMP_DIR/home/loomadmin"
DESK_HOME="$TMP_DIR/home/loomdesk"
BOX_PATH="$HOME_DIR/LOOM Box"
CONFIG_DIR="$TMP_DIR/config"
DATA_DIR="$TMP_DIR/data"
STATE_DIR="$TMP_DIR/state"
LOG_DIR="$TMP_DIR/logs"
OBJECT_STORE="$TMP_DIR/object-store"
STORAGE_EXPORT="$TMP_DIR/storage-export"
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
  --storage-export-root '$STORAGE_EXPORT'
  --socket-path '$SOCKET_PATH'
  --box-path '$BOX_PATH'
  --box-profile main
  --manifest '$MANIFEST'
"

check_json "main setup dry-run previews human storage links" \
  "loom --json setup apply $APPLY_ARGS --dry-run" \
  ".dry_run == true and ([.changed[]? | select(.category == \"human_links\")] | length) >= 2"

check_remote "dry-run did not create human storage links" "
  test ! -e '$HOME_DIR/LOOM Storage'
  test ! -e '$DESK_HOME/LOOM Storage'
"

check_remote "prepare obsolete main desktop artifacts" "
  mkdir -p '$HOME_DIR' '$DESK_HOME'
  ln -s '$TMP_DIR/missing-admin-box' '$HOME_DIR/'\\''LOOM BOX'\\'''
  ln -s '$TMP_DIR/missing-desk-box' '$DESK_HOME/LOOM BOX'
"

check_json "main setup apply creates storage links and removes safe obsolete artifacts" \
  "loom --json setup apply $APPLY_ARGS --yes" \
  ".dry_run == false and ([.changed[]? | select(.category == \"human_links\")] | length) >= 4"

check_remote "human storage links point to export root" "
  test \"\$(readlink '$HOME_DIR/LOOM Storage')\" = '$STORAGE_EXPORT'
  test \"\$(readlink '$DESK_HOME/LOOM Storage')\" = '$STORAGE_EXPORT'
"

check_remote "obsolete broken desktop artifacts were removed" "
  test ! -L '$HOME_DIR/'\\''LOOM BOX'\\'''
  test ! -L '$DESK_HOME/LOOM BOX'
"

check_json "setup status reports human storage links present" \
  "loom --json setup status --manifest '$MANIFEST'" \
  "([.human_links[]? | select(.kind == \"storage\" and .status == \"present\")] | length) == 2"

check_json "setup doctor has no human-link findings after apply" \
  "loom --json setup doctor --manifest '$MANIFEST'" \
  "([.findings[]? | select(.code | startswith(\"setup.human_link.\"))] | length) == 0"

check_remote "create intentionally missing link for repair smoke" "
  rm '$DESK_HOME/LOOM Storage'
"

check_json "doctor reports repairable missing storage link" \
  "loom --json setup doctor --manifest '$MANIFEST'" \
  "([.findings[]? | select(.repair_id == \"repair-main-human-links\")] | length) >= 1"

check_json "setup repair restores missing human storage link" \
  "loom --json setup repair --manifest '$MANIFEST' --fix repair-main-human-links --yes" \
  "([.changed[]? | select(.repair_id == \"repair-main-human-links\")] | length) >= 1"

check_remote "repaired loomdesk storage link points to export root" "
  test \"\$(readlink '$DESK_HOME/LOOM Storage')\" = '$STORAGE_EXPORT'
"

log "completed $pass_count checks"

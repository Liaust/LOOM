#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

TARGET_HOST="${LOOM_V0_5_WORKSPACE_HOST:-${LOOM_WORKSPACE_HOST:-loom-workspace}}"
MAIN_HOST="${LOOM_V0_5_MAIN_HOST:-${LOOM_DEV_HOST:-loom-dev}}"
MAIN_URL="${LOOM_V0_5_MAIN_URL:-http://10.44.0.2:8080}"
REMOTE_BASE="${LOOM_V0_5_WORKSPACE_REMOTE_BASE:-}"
if [[ -z "$REMOTE_BASE" && -n "${LOOM_WORKSPACE_REMOTE_DIR:-}" ]]; then
  REMOTE_BASE="${LOOM_WORKSPACE_REMOTE_DIR}/tmp"
fi
RUN_ID="v0-5-workspace-$(date +%s)-$RANDOM"
KEEP="${LOOM_V0_5_KEEP_REMOTE:-0}"
pass_count=0

log() {
  printf '[smoke] %s\n' "$*"
}

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$*"
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

remote_target() {
  ssh -o BatchMode=yes "$TARGET_HOST" "$@"
}

remote_main() {
  ssh -o BatchMode=yes "$MAIN_HOST" "$@"
}

run_loom() {
  if [[ -n "${LOOM_BIN:-}" ]]; then
    "$LOOM_BIN" "$@"
  else
    (cd "$REPO_ROOT" && go run ./cmd/loom "$@")
  fi
}

check_target() {
  local label="$1"
  local command="$2"
  remote_target "$command" >/dev/null
  pass "$label"
}

check_main() {
  local label="$1"
  local command="$2"
  remote_main "$command" >/dev/null
  pass "$label"
}

check_json_file() {
  local label="$1"
  local file="$2"
  local filter="$3"
  jq -e "$filter" "$file" >/dev/null
  pass "$label"
}

check_target_json() {
  local label="$1"
  local command="$2"
  local filter="$3"
  local output
  output="$(remote_target "$command")"
  jq -e "$filter" <<<"$output" >/dev/null
  pass "$label"
}

check_main_json() {
  local label="$1"
  local command="$2"
  local filter="$3"
  local output
  output="$(remote_main "$command")"
  jq -e "$filter" <<<"$output" >/dev/null
  pass "$label"
}

create_remote_tmp() {
  if [[ -n "$REMOTE_BASE" ]]; then
    remote_target "mkdir -p '$REMOTE_BASE' && mktemp -d '$REMOTE_BASE/v0-5-workspace.XXXXXX'"
  else
    remote_target 'base="${HOME:-/tmp}/.cache/loom-smoke"; mkdir -p "$base" && mktemp -d "$base/v0-5-workspace.XXXXXX"'
  fi
}

detect_package_mode() {
  if [[ -n "${LOOM_V0_5_PACKAGE_MODE:-}" ]]; then
    printf '%s\n' "$LOOM_V0_5_PACKAGE_MODE"
  elif remote_target 'command -v go >/dev/null 2>&1'; then
    printf 'local-build\n'
  elif remote_target 'command -v nix >/dev/null 2>&1'; then
    printf 'nix\n'
  else
    fail "remote target needs go or nix to build LOOM"
  fi
}

remote_loom_command() {
  case "$1" in
    local-build) printf "cd '%s' && ./.loom/bin/loom" "$SOURCE_DIR" ;;
    nix) printf "cd '%s' && nix --extra-experimental-features 'nix-command flakes' run .#loom --" "$SOURCE_DIR" ;;
    *) printf "loom" ;;
  esac
}

remote_agent_command() {
  case "$1" in
    local-build) printf "cd '%s' && ./.loom/bin/loom-node-agent" "$SOURCE_DIR" ;;
    nix) printf "cd '%s' && nix --extra-experimental-features 'nix-command flakes' run .#loom-node-agent --" "$SOURCE_DIR" ;;
    *) printf "loom-node-agent" ;;
  esac
}

log "workspace target: $TARGET_HOST"
log "main host: $MAIN_HOST"
log "main URL: $MAIN_URL"
log "run id: $RUN_ID"

command -v jq >/dev/null 2>&1 || fail "local jq is required"
check_target "workspace SSH target reachable" 'hostname'
PACKAGE_MODE="$(detect_package_mode)"
pass "remote package mode selected: $PACKAGE_MODE"
check_main "main SSH target has loom CLI" 'command -v loom'
check_main_json "main SSH target health ok" 'loom health --json' '.ok == true'

TMP_DIR="$(create_remote_tmp)"
LOCAL_TMP="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-5-workspace.XXXXXX")"
cleanup() {
  rm -rf "$LOCAL_TMP"
  if [[ "$KEEP" != "1" ]]; then
    remote_target "rm -rf '$TMP_DIR'" || true
  else
    log "kept remote temp dir: $TMP_DIR"
  fi
}
trap cleanup EXIT

NODE_KEY="workspace-$RUN_ID"
HOME_DIR="$TMP_DIR/home/loomadmin"
SOURCE_DIR="$TMP_DIR/source"
BOX_PATH="$HOME_DIR/LOOM Box"
CONFIG_DIR="$TMP_DIR/config"
DATA_DIR="$TMP_DIR/data"
STATE_DIR="$TMP_DIR/state"
LOG_DIR="$TMP_DIR/logs"
MANIFEST="$CONFIG_DIR/install.yaml"
NODE_AGENT_CONFIG="$HOME_DIR/.config/loom-node-agent/config.json"
NODE_AGENT_STATE="$HOME_DIR/.local/state/loom-node-agent/state.json"
NODE_AGENT_DATA="$HOME_DIR/.local/state/loom-node-agent"
REMOTE_LOOM="$(remote_loom_command "$PACKAGE_MODE")"
REMOTE_AGENT="$(remote_agent_command "$PACKAGE_MODE") --config '$NODE_AGENT_CONFIG' --state '$NODE_AGENT_STATE' --data-dir '$NODE_AGENT_DATA'"

BOOTSTRAP_FLAGS=(
  --kind workspace
  --role primary_workspace
  --runtime-class workspace_full
  --node-key "$NODE_KEY"
  --display-name "Workspace $RUN_ID"
  --main-host "$MAIN_HOST"
  --main-url "$MAIN_URL"
  --install-mode user
  --service-manager none
  --package-mode "$PACKAGE_MODE"
  --source-path "$REPO_ROOT"
  --remote-source-dir "$SOURCE_DIR"
  --home-dir "$HOME_DIR"
  --user-name loomadmin
  --config-dir "$CONFIG_DIR"
  --data-dir "$DATA_DIR"
  --state-dir "$STATE_DIR"
  --log-dir "$LOG_DIR"
  --box-path "$BOX_PATH"
  --box-profile workspace
)

FIRST_JSON="$LOCAL_TMP/workspace-first.json"
SECOND_JSON="$LOCAL_TMP/workspace-second.json"

run_loom --json bootstrap ssh "$TARGET_HOST" "${BOOTSTRAP_FLAGS[@]}" --yes >"$FIRST_JSON"
check_json_file "workspace SSH bootstrap enrolled node" "$FIRST_JSON" \
  '.status == "applied" and .enrollment.status == "verified_on_main" and .remote_apply.manifest.node_kind == "workspace"'

check_target "workspace installer created Box, manifest, and node-agent state" "
  test -f '$MANIFEST'
  test -d '$BOX_PATH/Projects'
  test -d '$BOX_PATH/Dropzone'
  test -f '$NODE_AGENT_CONFIG'
  test -f '$NODE_AGENT_STATE'
"

check_target_json "workspace node-agent status shows credential configured" \
  "$REMOTE_AGENT --json status" \
  ".data.config.node_key == \"$NODE_KEY\" and .data.credential_configured == true"

check_target_json "workspace heartbeat succeeds from target" \
  "$REMOTE_AGENT --json heartbeat --once" \
  '.ok == true and .data.presence_state == "online"'

check_main_json "main can inspect workspace node health" \
  "loom --json node health '$NODE_KEY'" \
  ".ok == true and .data.node.node_key == \"$NODE_KEY\""

run_loom --json bootstrap ssh "$TARGET_HOST" "${BOOTSTRAP_FLAGS[@]}" --yes >"$SECOND_JSON"
check_json_file "workspace SSH bootstrap rerun resumes existing enrollment" "$SECOND_JSON" \
  '.status == "applied" and .enrollment.status == "verified_on_main" and ([.enrollment.steps[]? | select(.id == "create_token" and .status == "skipped")] | length) == 1'

check_target_json "workspace setup doctor reports no blocking findings" \
  "$REMOTE_LOOM --json setup doctor --manifest '$MANIFEST'" \
  '([.findings[]? | select(.severity == "blocking")] | length) == 0'

if grep -R -E 'node_enroll_|node_cred_' "$LOCAL_TMP" >/dev/null; then
  fail "bootstrap output leaked enrollment or credential token pattern"
fi
pass "workspace bootstrap output does not leak enrollment token patterns"

log "completed $pass_count checks"

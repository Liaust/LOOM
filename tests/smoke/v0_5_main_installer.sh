#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_V0_5_MAIN_HOST:-${LOOM_DEV_HOST:-loom-dev}}"
REMOTE_BASE="${LOOM_V0_5_MAIN_REMOTE_BASE:-${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}/tmp}"
RUN_ID="v0-5-main-$(date +%s)-$RANDOM"
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

remote() {
  ssh -o BatchMode=yes "$HOST" "$@"
}

run_loom() {
  if [[ -n "${LOOM_BIN:-}" ]]; then
    "$LOOM_BIN" "$@"
  else
    (cd "$REPO_ROOT" && go run ./cmd/loom "$@")
  fi
}

check_remote() {
  local label="$1"
  local command="$2"
  remote "$command" >/dev/null
  pass "$label"
}

detect_package_mode() {
  if [[ -n "${LOOM_V0_5_PACKAGE_MODE:-}" ]]; then
    printf '%s\n' "$LOOM_V0_5_PACKAGE_MODE"
  elif remote 'command -v go >/dev/null 2>&1'; then
    printf 'local-build\n'
  elif remote 'command -v nix >/dev/null 2>&1'; then
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

check_json_file() {
  local label="$1"
  local file="$2"
  local filter="$3"
  jq -e "$filter" "$file" >/dev/null
  pass "$label"
}

check_remote_json() {
  local label="$1"
  local command="$2"
  local filter="$3"
  local output
  output="$(remote "$command")"
  jq -e "$filter" <<<"$output" >/dev/null
  pass "$label"
}

log "main target: $HOST"
log "run id: $RUN_ID"

command -v jq >/dev/null 2>&1 || fail "local jq is required"
check_remote "SSH target reachable" 'hostname'
PACKAGE_MODE="$(detect_package_mode)"
pass "remote package mode selected: $PACKAGE_MODE"

TMP_DIR="$(remote "mkdir -p '$REMOTE_BASE' && mktemp -d '$REMOTE_BASE/v0-5-main.XXXXXX'")"
LOCAL_TMP="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-5-main.XXXXXX")"
cleanup() {
  rm -rf "$LOCAL_TMP"
  if [[ "$KEEP" != "1" ]]; then
    remote "rm -rf '$TMP_DIR'" || true
  else
    log "kept remote temp dir: $TMP_DIR"
  fi
}
trap cleanup EXIT

HOME_DIR="$TMP_DIR/home/loomadmin"
SOURCE_DIR="$TMP_DIR/source"
BOX_PATH="$HOME_DIR/LOOM Box"
CONFIG_DIR="$TMP_DIR/config"
DATA_DIR="$TMP_DIR/data"
STATE_DIR="$TMP_DIR/state"
LOG_DIR="$TMP_DIR/logs"
OBJECT_STORE="$TMP_DIR/object-store"
SOCKET_PATH="$TMP_DIR/run/loomd.sock"
MANIFEST="$CONFIG_DIR/install.yaml"
REMOTE_LOOM="$(remote_loom_command "$PACKAGE_MODE")"

BOOTSTRAP_FLAGS=(
  --kind main
  --role main
  --runtime-class main_full
  --node-key "main-$RUN_ID"
  --display-name "Main $RUN_ID"
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
  --object-store "$OBJECT_STORE"
  --socket-path "$SOCKET_PATH"
  --box-path "$BOX_PATH"
  --box-profile main
  --bootstrap-mode production
)

DRY_RUN_JSON="$LOCAL_TMP/main-dry-run.json"
FIRST_JSON="$LOCAL_TMP/main-first.json"
SECOND_JSON="$LOCAL_TMP/main-second.json"

run_loom --json bootstrap ssh "$HOST" "${BOOTSTRAP_FLAGS[@]}" --dry-run >"$DRY_RUN_JSON"
check_json_file "main SSH bootstrap dry-run plans a main install" "$DRY_RUN_JSON" \
  '.dry_run == true and .plan.setup_plan.spec.node_kind == "main" and .plan.setup_plan.spec.box_path != ""'

check_remote "main dry-run did not write manifest" "test ! -e '$MANIFEST'"

run_loom --json bootstrap ssh "$HOST" "${BOOTSTRAP_FLAGS[@]}" --yes >"$FIRST_JSON"
check_json_file "main SSH bootstrap apply completed" "$FIRST_JSON" \
  '.status == "applied" and .remote_apply.manifest.node_kind == "main" and .remote_apply.manifest.box_profile == "main"'

check_remote "main installer created manifest, env, object store, and Box" "
  test -f '$MANIFEST'
  test -f '$CONFIG_DIR/loom.env'
  test -d '$OBJECT_STORE/temp'
  test -d '$BOX_PATH/Projects'
  test -d '$BOX_PATH/.loom/storage/dropzone'
"

check_remote "main service env records Box and profile" "
  grep -q 'LOOM_NODE_KIND=main' '$CONFIG_DIR/loom.env'
  grep -q 'LOOM_BOX_PROFILE=main' '$CONFIG_DIR/loom.env'
  grep -q \"LOOM_BOX_PATH=$BOX_PATH\" '$CONFIG_DIR/loom.env'
"

run_loom --json bootstrap ssh "$HOST" "${BOOTSTRAP_FLAGS[@]}" --yes >"$SECOND_JSON"
check_json_file "main SSH bootstrap rerun remains idempotent" "$SECOND_JSON" \
  '.status == "applied" and ([.remote_apply.skipped[]? | select(.id == "write_loomd_env" and .status == "already_satisfied")] | length) == 1'

check_remote_json "main setup status works through installed remote source" \
  "$REMOTE_LOOM --json setup status --manifest '$MANIFEST'" \
  '.manifest.exists == true and .node.node_kind == "main"'

check_remote_json "main setup doctor reports no blocking findings" \
  "$REMOTE_LOOM --json setup doctor --manifest '$MANIFEST'" \
  '([.findings[]? | select(.severity == "blocking")] | length) == 0'

if grep -R -E 'node_enroll_|node_cred_' "$LOCAL_TMP" >/dev/null; then
  fail "bootstrap output leaked enrollment or credential token pattern"
fi
pass "main bootstrap output does not leak enrollment token patterns"

log "completed $pass_count checks"

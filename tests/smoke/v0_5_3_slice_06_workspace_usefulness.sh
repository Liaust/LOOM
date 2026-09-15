#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'v0.5.3 slice 06 workspace usefulness: %s\n' "$*" >&2
  exit 1
}

pass() {
  printf '[ok] %s\n' "$*"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

if [[ "${LOOM_RUN_PRODUCTION_MACBOOK_SMOKE:-}" != "1" ]]; then
  fail "refusing to run production MacBook smoke without LOOM_RUN_PRODUCTION_MACBOOK_SMOKE=1"
fi

HOME_DIR="${LOOM_WORKSPACE_HOME:-$HOME}"
NODE_KEY="${LOOM_WORKSPACE_NODE_KEY:-macbook}"
MAIN_URL="${LOOM_MAIN_URL:-http://10.44.0.2:8080}"
MAIN_SSH="${LOOM_MAIN_SSH:-loom-main}"
BOX_PATH="${LOOM_BOX_PATH:-$HOME_DIR/loom-box}"
LOOM_BIN="${LOOM_BIN:-$HOME_DIR/.local/bin/loom}"
NODE_AGENT_BIN="${LOOM_NODE_AGENT_BIN:-$HOME_DIR/.local/bin/loom-node-agent}"
CAPABILITY_ADDRESS="${LOOM_SMOKE_CAPABILITY_ADDRESS:-workspace/macbook@system.echo}"
CALL_TEXT="${LOOM_SMOKE_CALL_TEXT:-v0.5.3 slice 06 smoke}"

require_command jq
require_command curl
require_command ssh

[[ -x "$LOOM_BIN" ]] || fail "installed loom binary is not executable: $LOOM_BIN"
[[ -x "$NODE_AGENT_BIN" ]] || fail "installed loom-node-agent binary is not executable: $NODE_AGENT_BIN"

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
      and .box.status == "configured"
      and .node_agent.status == "configured"
      and .node_agent.credential_configured == true
      and .enrollment.credential_configured == true
      and .enrollment.verified_on_main == true
    ' >/dev/null
pass "setup status is configured"

"$NODE_AGENT_BIN" --json status \
  | jq -e '
      .ok == true
      and .data.config.node_key == "'"$NODE_KEY"'"
      and .data.config.main_url == "'"$MAIN_URL"'"
      and .data.credential_configured == true
    ' >/dev/null
pass "node-agent status succeeds"

WATCHED_ROOTS_JSON="$("$NODE_AGENT_BIN" --json watched-roots list)"
printf '%s\n' "$WATCHED_ROOTS_JSON" \
  | jq -e '
      .ok == true
      and ([.data.roots[].root_key] | sort) == ["loom_box__documents", "loom_box__notes"]
      and all(.data.roots[]; (.config.safe_root_key == "loom_box" or .config.safe_root_key == "project"))
      and all(.data.roots[]; (.config.root_relative_path == "Documents" or .config.root_relative_path == "Notes"))
    ' >/dev/null
pass "only LOOM Box Notes and Documents watched roots are configured"

for root_key in loom_box__notes loom_box__documents; do
  "$NODE_AGENT_BIN" --json watched-roots run "$root_key" --once --mode full --flush \
    | jq -e '
        .ok == true
        and .data.status == "healthy"
        and .data.main_report.status == "recorded"
        and ((.data.output_flush.pending_after // 0) == 0)
        and ((.data.backup_output_flush.pending_after // 0) == 0)
      ' >/dev/null
  pass "watched root $root_key reports and flushes to main"
done

DROPZONE_STATUS_JSON="$("$NODE_AGENT_BIN" --json dropzone status)"
printf '%s\n' "$DROPZONE_STATUS_JSON" \
  | jq -e '
      .ok == true
      and .data.health.status == "healthy"
      and (((.data.status.accepted // []) | map(select(.safe_to_delete != true)) | length) == 0)
      and (((.data.status.active // []) | length) == 0)
    ' >/dev/null
pass "Dropzone status is healthy and has no active transfer"

ssh "$MAIN_SSH" 'loom --json providers list' \
  | jq -e '
      .ok == true
      and ([.data[]? | select(.compact_address == "workspace/macbook@system" and .status == "active" and .health_status == "ok")] | length) == 1
      and ([.data[]? | select(.compact_address == "workspace/macbook@filesystem" and .status == "active" and .health_status == "ok")] | length) == 1
    ' >/dev/null
pass "main has MacBook provider advertisements"

ssh "$MAIN_SSH" 'loom --json capabilities list' \
  | jq -e '
      .ok == true
      and ([.data[]? | select(.compact_address == "workspace/macbook@system.echo")] | length) == 1
      and ([.data[]? | select(.compact_address == "workspace/macbook@filesystem.safe_list")] | length) == 1
    ' >/dev/null
pass "main has MacBook capabilities"

CALL_PAYLOAD="$(ssh "$MAIN_SSH" "loom --json capability call '$CAPABILITY_ADDRESS' --input '{\"text\":\"$CALL_TEXT\"}'")"
printf '%s\n' "$CALL_PAYLOAD" \
  | jq -e '.ok == true and .data.status == "dispatched" and (.data.capability_call.capability_call_id | length > 0)' >/dev/null
CALL_ID="$(printf '%s\n' "$CALL_PAYLOAD" | jq -r '.data.capability_call.capability_call_id')"

"$NODE_AGENT_BIN" --json poll --once \
  | jq -e '.ok == true' >/dev/null
"$NODE_AGENT_BIN" --json outbox flush \
  | jq -e '.ok == true' >/dev/null

ssh "$MAIN_SSH" "loom --json capability-call inspect '$CALL_ID'" \
  | jq -e '
      .ok == true
      and .data.status == "completed"
      and .data.result_json.node_key == "'"$NODE_KEY"'"
    ' >/dev/null
pass "routed MacBook capability call completed"

printf 'v0.5.3 slice 06 workspace usefulness passed.\n'

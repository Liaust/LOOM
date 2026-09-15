#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
WORKSPACE_SCRIPT="$REPO_ROOT/scripts/loom-workspace"

RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
NODE_KEY="v0-2-slice-09-watched-root-${RUN_ID}"
REMOTE_TMP="/tmp/loom-node-agent-v0-2-slice-09-${RUN_ID}"
REMOTE_CONFIG="${REMOTE_TMP}/config.json"
REMOTE_STATE="${REMOTE_TMP}/state.json"
REMOTE_DATA="${REMOTE_TMP}/data"
REMOTE_VAULT="${REMOTE_TMP}/vault"

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

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "missing required command: $1"
  fi
}

check_json_value() {
  local label="$1"
  local json="$2"
  local filter="$3"

  if ! printf '%s' "$json" | jq -e "$filter" >/dev/null; then
    printf '%s\n' "$json" >&2
    fail "$label"
  fi
  pass "$label"
}

agent_json() {
  "$WORKSPACE_SCRIPT" run -- \
    --config "$REMOTE_CONFIG" \
    --state "$REMOTE_STATE" \
    --data-dir "$REMOTE_DATA" \
    --json \
    "$@"
}

workspace_ssh() {
  "$WORKSPACE_SCRIPT" ssh "$@"
}

require_command jq
require_command ssh
require_command rsync

log "node key: $NODE_KEY"
log "remote temp: $REMOTE_TMP"

"$WORKSPACE_SCRIPT" sync
"$WORKSPACE_SCRIPT" run -- version >/dev/null
pass "loom-node-agent builds on workspace VM"

workspace_ssh "rm -rf '$REMOTE_TMP' && mkdir -p '$REMOTE_VAULT/Projects' '$REMOTE_VAULT/.obsidian' && printf '# Project\n\nSlice 09 initial.\n' > '$REMOTE_VAULT/Project.md' && printf '# Second\n\nSecond file.\n' > '$REMOTE_VAULT/Projects/Second.md' && printf 'temporary\n' > '$REMOTE_VAULT/Project.tmp' && printf '{}\n' > '$REMOTE_VAULT/.obsidian/workspace.json'"
pass "created remote watched-root fixture"

agent_json \
  init \
  --main-url "http://127.0.0.1:1" \
  --node-key "$NODE_KEY" \
  --display-name "v0.2 Slice 09 Watched Root ${RUN_ID}" \
  --kind workspace \
  --role workspace \
  --runtime-class workspace >/dev/null
pass "workspace node-agent initialized"

ROOTS_JSON="$(agent_json filesystem roots add --key slice09 --path "$REMOTE_VAULT")"
check_json_value "filesystem safe root configured" "$ROOTS_JSON" \
  '.ok == true and ([.data.safe_roots[] | select(.root_key == "slice09" and .allow_list == true)] | length) == 1'

ADD_JSON="$(agent_json watched-roots add notes --safe-root slice09 --path . --include '**/*.md' --exclude '.obsidian/**' --exclude '**/*.tmp')"
check_json_value "watched root configured as runtime worker" "$ADD_JSON" \
  '.ok == true
   and .data.config.root_key == "notes"
   and .data.worker.worker_key == "node-agent.watched_root.notes"
   and .data.worker.kind == "watched_root"
   and .data.config.scan.full_rescan_interval == "6h"'

LIST_JSON="$(agent_json workers list)"
check_json_value "generic worker list includes watched root" "$LIST_JSON" \
  '.ok == true and ([.data[] | select(.worker_key == "node-agent.watched_root.notes" and .kind == "watched_root" and .enabled == true)] | length) == 1'

RUN_INITIAL_JSON="$(agent_json watched-roots run notes --once --mode full --stability-window 0s)"
check_json_value "full watched-root scan captures included and excluded paths" "$RUN_INITIAL_JSON" \
  '.ok == true
   and .data.root_key == "notes"
   and .data.mode == "full"
   and .data.status == "healthy"
   and .data.counts.included == 2
   and .data.counts.excluded >= 1
   and .data.counts.hash_computed >= 2'

EXPLAIN_INCLUDED_JSON="$(agent_json watched-roots explain notes --path Project.md)"
check_json_value "explain reports included markdown with stored state" "$EXPLAIN_INCLUDED_JSON" \
  '.ok == true
   and .data.classification.included == true
   and .data.known_from_state == true
   and (.data.state.content_hash_uri | startswith("sha256:"))'

EXPLAIN_EXCLUDED_JSON="$(agent_json watched-roots explain notes --path Project.tmp)"
check_json_value "explain reports excluded temp file" "$EXPLAIN_EXCLUDED_JSON" \
  '.ok == true
   and .data.classification.included == false
   and .data.classification.reason_code == "excluded_by_pattern"'

workspace_ssh "printf '# Project\n\nSlice 09 changed content.\n' > '$REMOTE_VAULT/Project.md'"
RUN_CHANGED_JSON="$(agent_json watched-roots run notes --once --mode full --stability-window 0s)"
check_json_value "full scan detects modified markdown" "$RUN_CHANGED_JSON" \
  '.ok == true and .data.status == "healthy" and .data.counts.changed >= 1 and ([.data.changed_paths[] | select(.relative_path == "Project.md" and .change_kind == "modified")] | length) == 1'

workspace_ssh "rm -f '$REMOTE_VAULT/Projects/Second.md'"
RUN_DELETED_JSON="$(agent_json watched-roots run notes --once --mode full --stability-window 0s)"
check_json_value "full scan records deleted path state" "$RUN_DELETED_JSON" \
  '.ok == true
   and .data.status == "healthy"
   and .data.counts.deleted >= 1
   and ([.data.changed_paths[] | select(.relative_path == "Projects/Second.md" and .change_kind == "deleted")] | length) == 1'

WORKER_RUN_JSON="$(agent_json workers run node-agent.watched_root.notes --once)"
check_json_value "generic runtime can run watched-root worker" "$WORKER_RUN_JSON" \
  '.ok == true
   and .data.instance.worker_key == "node-agent.watched_root.notes"
   and .data.run.status == "succeeded"
   and .data.health.status == "healthy"
   and .data.health.queue_summary_json.root_key == "notes"'

STATUS_JSON="$(agent_json watched-roots status notes)"
check_json_value "watched-root status exposes scanner and runtime health" "$STATUS_JSON" \
  '.ok == true
   and (.data.roots | length) == 1
   and .data.roots[0].root_key == "notes"
   and .data.roots[0].health.status == "healthy"
   and .data.roots[0].summary.included == 1
   and .data.roots[0].path_counts.deleted >= 1'

WORKERS_STATUS_JSON="$(agent_json workers status)"
check_json_value "generic worker status includes watched-root checkpoint" "$WORKERS_STATUS_JSON" \
  '.ok == true
   and ([.data.workers[] | select(.instance.worker_key == "node-agent.watched_root.notes" and .health.status == "healthy" and .checkpoint.last_success_at != null)] | length) == 1'

log "completed $pass_count checks"

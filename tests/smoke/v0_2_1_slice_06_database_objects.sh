#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
TOKEN_ID="${RUN_ID//-/}"
SMOKE_SLUG="v0-2-1-slice-06-$RUN_ID"
SMOKE_NAME="v0.2.1 Slice 06 Smoke $RUN_ID"
INGEST_PATH="$REMOTE_DIR/tests/smoke/v0_2_1_slice_06_$RUN_ID.md"
OBJECT_NAME="v0-2-1-slice-06-database.md"
UNIQUE_TOKEN="v021slice06database$TOKEN_ID"
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

json_value() {
  local command="$1"
  local filter="$2"

  remote "$command | jq -r '$filter'"
}

require_command jq
require_command ssh

log "target: $HOST"
log "run: $RUN_ID"
log "unique token: $UNIQUE_TOKEN"

check_remote "loomd service active" 'systemctl is-active loomd'
check_json "main health ok" \
  'loom health --json' \
  '.ok == true and .data.status == "ok"'

check_remote "write database portal smoke object fixture" \
  "mkdir -p '$REMOTE_DIR/tests/smoke' && printf '# v0.2.1 Slice 06 Database\n\nPortal database object token: $UNIQUE_TOKEN.\n' > '$INGEST_PATH' && chmod 0644 '$INGEST_PATH' && sudo -n -u loom test -r '$INGEST_PATH'"

check_json "create smoke project" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --description 'v0.2.1 Slice 06 database portal smoke' --if-not-exists --json --correlation-id corr_smoke_v0_2_1_slice_06_project" \
  ".ok == true and .data.project.project.slug == \"$SMOKE_SLUG\""

remote "loom object ingest '$INGEST_PATH' --project '$SMOKE_SLUG' --name '$OBJECT_NAME' --json --correlation-id corr_smoke_v0_2_1_slice_06_ingest > /tmp/loom-v0-2-1-slice-06-ingest.json"
check_json "object ingest succeeds" \
  'cat /tmp/loom-v0-2-1-slice-06-ingest.json' \
  '.ok == true and (.data.object.object.object_id | startswith("object_")) and (.data.object.latest_version.object_version_id | startswith("version_"))'

object_id="$(json_value 'cat /tmp/loom-v0-2-1-slice-06-ingest.json' '.data.object.object.object_id')"
version_id="$(json_value 'cat /tmp/loom-v0-2-1-slice-06-ingest.json' '.data.object.latest_version.object_version_id')"
[[ "$object_id" == object_* ]] || fail "expected object id, got $object_id"
[[ "$version_id" == version_* ]] || fail "expected version id, got $version_id"
pass "captured object and version ids"

remote "loom indexes run text --once --json --correlation-id corr_smoke_v0_2_1_slice_06_indexer > /tmp/loom-v0-2-1-slice-06-indexer.json"
check_json "text indexer can process portal object" \
  'cat /tmp/loom-v0-2-1-slice-06-indexer.json' \
  '.ok == true and .data.run.run_status == "succeeded"'

check_remote "search sees indexed portal object" \
  "for attempt in 1 2 3 4 5 6 7 8; do
     if loom search '$UNIQUE_TOKEN' --project '$SMOKE_SLUG' --json --correlation-id corr_smoke_v0_2_1_slice_06_search_\$attempt | jq -e '.ok == true and .data.result_count >= 1 and ([.data.results[] | select(.object_id == \"$object_id\" and .object_version_id == \"$version_id\")] | length) >= 1' >/dev/null; then
       exit 0
     fi
     loom indexes run text --once --json --correlation-id corr_smoke_v0_2_1_slice_06_indexer_retry_\$attempt >/tmp/loom-v0-2-1-slice-06-indexer-retry.json || true
     sleep 1
   done
   loom search '$UNIQUE_TOKEN' --project '$SMOKE_SLUG' --json --correlation-id corr_smoke_v0_2_1_slice_06_search_final | jq -e '.ok == true and .data.result_count >= 1 and ([.data.results[] | select(.object_id == \"$object_id\" and .object_version_id == \"$version_id\")] | length) >= 1' >/dev/null"

check_remote "database portal one-shot renders live object and sync/index sections" \
  "loom enter --start database --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-06-database.out
   grep -q 'Database And Objects' /tmp/loom-v0-2-1-slice-06-database.out
   grep -q 'Recent Objects' /tmp/loom-v0-2-1-slice-06-database.out
   grep -q '$OBJECT_NAME' /tmp/loom-v0-2-1-slice-06-database.out
   grep -q 'Recent Index Statuses' /tmp/loom-v0-2-1-slice-06-database.out
   grep -q 'Sync Batches' /tmp/loom-v0-2-1-slice-06-database.out
   grep -q 'Private Backups' /tmp/loom-v0-2-1-slice-06-database.out
   grep -q 'Deletion Requests' /tmp/loom-v0-2-1-slice-06-database.out
   grep -q 'Retry Failed Index Work' /tmp/loom-v0-2-1-slice-06-database.out
   grep -q 'Run Text Indexer Once' /tmp/loom-v0-2-1-slice-06-database.out
   grep -q 's database search' /tmp/loom-v0-2-1-slice-06-database.out"

check_remote "database search one-shot runs through portal backend path" \
  "loom enter --database-search '$UNIQUE_TOKEN' --exit-after-render > /tmp/loom-v0-2-1-slice-06-search.out
   grep -q 'Database Search' /tmp/loom-v0-2-1-slice-06-search.out
   grep -q '$object_id' /tmp/loom-v0-2-1-slice-06-search.out
   grep -q 'freshness=' /tmp/loom-v0-2-1-slice-06-search.out"

check_remote "database portal no-color render contains no ANSI" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --start database --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-06-no-color.out
   grep -q 'Database And Objects' /tmp/loom-v0-2-1-slice-06-no-color.out
   ! grep -q \"\$(printf '\\033')\" /tmp/loom-v0-2-1-slice-06-no-color.out"

check_remote "object inspect action resolves from database surface" \
  "loom enter --start database --run-action 'database.object.${object_id}.inspect' --exit-after-render > /tmp/loom-v0-2-1-slice-06-object-action.out
   grep -q 'Action Complete' /tmp/loom-v0-2-1-slice-06-object-action.out
   grep -q 'Object detail loaded' /tmp/loom-v0-2-1-slice-06-object-action.out
   grep -q 'Version Count' /tmp/loom-v0-2-1-slice-06-object-action.out
   grep -q 'Index Statuses' /tmp/loom-v0-2-1-slice-06-object-action.out"

check_remote "object rebuild action previews and runs from database surface" \
  "loom enter --start database --preview-action 'database.object.${object_id}.rebuild_index' --exit-after-render > /tmp/loom-v0-2-1-slice-06-rebuild-preview.out
   grep -q 'Action Preview' /tmp/loom-v0-2-1-slice-06-rebuild-preview.out
   grep -q 'Rebuild Object Index' /tmp/loom-v0-2-1-slice-06-rebuild-preview.out
   loom enter --start database --run-action 'database.object.${object_id}.rebuild_index' --confirm --exit-after-render > /tmp/loom-v0-2-1-slice-06-rebuild-run.out
   grep -q 'Action Complete' /tmp/loom-v0-2-1-slice-06-rebuild-run.out
   grep -q 'Object index rebuild was queued' /tmp/loom-v0-2-1-slice-06-rebuild-run.out"

log "completed $pass_count checks"

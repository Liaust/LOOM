#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
TOKEN_ID="${RUN_ID//-/}"
SMOKE_SLUG="v0-2-slice-04-$RUN_ID"
SMOKE_NAME="v0.2 Slice 04 Smoke $RUN_ID"
INGEST_PATH="$REMOTE_DIR/tests/smoke/v0_2_slice_04_$RUN_ID.md"
UNSUPPORTED_PATH="$REMOTE_DIR/tests/smoke/v0_2_slice_04_$RUN_ID.bin"
UNIQUE_TOKEN="v02slice04indexing$TOKEN_ID"
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

log "target: $HOST"
log "run: $RUN_ID"
log "unique token: $UNIQUE_TOKEN"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports Slice 04 index queue migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 19'

remote "loom workers list --json --correlation-id corr_smoke_v0_2_slice_04_workers > /tmp/loom-v0-2-slice-04-workers.json"
check_json "worker list includes text indexer" \
  'cat /tmp/loom-v0-2-slice-04-workers.json' \
  '.ok == true and ([.data[] | select(.worker_key == "main.indexer_text" and .worker_kind == "indexer_text")] | length) == 1'

remote "loom worker inspect main.indexer_text --json --correlation-id corr_smoke_v0_2_slice_04_indexer_inspect > /tmp/loom-v0-2-slice-04-indexer-inspect.json"
check_json "text indexer is inspectable" \
  'cat /tmp/loom-v0-2-slice-04-indexer-inspect.json' \
  '.ok == true and .data.instance.worker_key == "main.indexer_text" and .data.instance.enabled == true'

remote "loom indexes run text --once --json --correlation-id corr_smoke_v0_2_slice_04_indexer_prerun > /tmp/loom-v0-2-slice-04-indexer-prerun.json"
check_json "text indexer domain run command works before ingest" \
  'cat /tmp/loom-v0-2-slice-04-indexer-prerun.json' \
  '.ok == true and .data.run.run_status == "succeeded" and .data.run.result_summary_json.schema_version == "indexer_text.result.v0.2"'

check_remote "write smoke markdown file visible to loomd" \
  "mkdir -p '$REMOTE_DIR/tests/smoke' && printf '# v0.2 Slice 04 Smoke\n\nCapability retrieval unique token: $UNIQUE_TOKEN.\n\nThis proves queue-backed indexing through main.indexer_text.\n' > '$INGEST_PATH' && chmod 0644 '$INGEST_PATH' && sudo -n -u loom test -r '$INGEST_PATH'"

check_json "create smoke project for indexing worker" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --description 'v0.2 Slice 04 indexing worker smoke' --if-not-exists --json --correlation-id corr_smoke_v0_2_slice_04_project_create" \
  ".ok == true and .data.project.project.slug == \"$SMOKE_SLUG\""

project_scope_id="$(json_value "loom project inspect '$SMOKE_SLUG' --json" '.data.project.project_scope_id')"
[[ "$project_scope_id" == scope_* ]] || fail "expected project scope id, got $project_scope_id"
pass "captured project scope id"

remote "loom object ingest '$INGEST_PATH' --project '$SMOKE_SLUG' --name v0-2-slice-04-indexing.md --json --correlation-id corr_smoke_v0_2_slice_04_ingest > /tmp/loom-v0-2-slice-04-ingest.json"
check_json "object ingest succeeds without inline indexing" \
  'cat /tmp/loom-v0-2-slice-04-ingest.json' \
  '.ok == true and (.data.object.object.object_id | startswith("object_")) and (.data.object.latest_version.object_version_id | startswith("version_")) and (.data.object.blob.blob_id | startswith("blob_"))'

object_id="$(json_value 'cat /tmp/loom-v0-2-slice-04-ingest.json' '.data.object.object.object_id')"
version_id="$(json_value 'cat /tmp/loom-v0-2-slice-04-ingest.json' '.data.object.latest_version.object_version_id')"
[[ "$object_id" == object_* ]] || fail "expected object id, got $object_id"
[[ "$version_id" == version_* ]] || fail "expected version id, got $version_id"
pass "captured queued object version ids"

remote "loom indexes queue list --object '$object_id' --json --correlation-id corr_smoke_v0_2_slice_04_queue_list > /tmp/loom-v0-2-slice-04-queue-list.json"
check_json "queue list shows ingested object as queued" \
  'cat /tmp/loom-v0-2-slice-04-queue-list.json' \
  ".ok == true and ([.data[] | select(.index_type == \"full_text\" and .status == \"queued\" and .object_id == \"$object_id\" and .object_version_id == \"$version_id\")] | length) == 1"

queue_status_id="$(json_value 'cat /tmp/loom-v0-2-slice-04-queue-list.json' '.data[] | select(.index_type == "full_text" and .object_id == "'"$object_id"'") | .index_status_id' | head -n 1)"
[[ "$queue_status_id" == index_status_* ]] || fail "expected index status id, got $queue_status_id"
pass "captured index queue status id"

check_json "queue show returns queued item" \
  "loom indexes queue show '$queue_status_id' --json --correlation-id corr_smoke_v0_2_slice_04_queue_show" \
  ".ok == true and .data.index_status_id == \"$queue_status_id\" and .data.status == \"queued\""

check_json "search does not find token before worker run" \
  "loom search '$UNIQUE_TOKEN' --project '$SMOKE_SLUG' --json --correlation-id corr_smoke_v0_2_slice_04_search_before" \
  '.ok == true and .data.result_count == 0'

remote "loom indexes run text --once --json --correlation-id corr_smoke_v0_2_slice_04_indexer_run > /tmp/loom-v0-2-slice-04-indexer-run.json"
check_json "text indexer processes queued item" \
  'cat /tmp/loom-v0-2-slice-04-indexer-run.json' \
  '.ok == true
   and .data.run.run_status == "succeeded"
   and .data.run.result_summary_json.schema_version == "indexer_text.result.v0.2"
   and .data.run.result_summary_json.claimed >= 1
   and .data.run.result_summary_json.indexed >= 1
   and .data.health.health_status == "healthy"'

check_json "index status includes full text indexed after worker run" \
  "loom indexes status --object '$object_id' --json --correlation-id corr_smoke_v0_2_slice_04_status_indexed" \
  ".ok == true and ([.data[] | select(.index_type == \"full_text\" and .status == \"indexed\" and .object_id == \"$object_id\" and .object_version_id == \"$version_id\")] | length) == 1"

check_json "index status includes extraction and chunking phases" \
  "loom indexes status --object '$object_id' --json --correlation-id corr_smoke_v0_2_slice_04_status_phases" \
  ".ok == true
   and ([.data[] | select(.index_type == \"text_extraction\" and .status == \"indexed\" and .object_id == \"$object_id\")] | length) == 1
   and ([.data[] | select(.index_type == \"chunking\" and .status == \"indexed\" and .object_id == \"$object_id\")] | length) == 1"

remote "loom search '$UNIQUE_TOKEN' --project '$SMOKE_SLUG' --json --correlation-id corr_smoke_v0_2_slice_04_search_after > /tmp/loom-v0-2-slice-04-search-after.json"
check_json "search returns worker-indexed content" \
  'cat /tmp/loom-v0-2-slice-04-search-after.json' \
  ".ok == true and .data.result_count >= 1 and .data.results[0].object_id == \"$object_id\" and .data.results[0].object_version_id == \"$version_id\" and (.data.results[0].document_chunk_id | startswith(\"document_chunk_\")) and .data.results[0].scope_id == \"$project_scope_id\""

check_json "index explain reports indexed queue status" \
  "loom indexes explain object '$object_id' --json --correlation-id corr_smoke_v0_2_slice_04_explain" \
  ".ok == true and .data.object_id == \"$object_id\" and .data.queue_status == \"indexed\""

check_json "failures command returns JSON array" \
  "loom indexes failures --json --correlation-id corr_smoke_v0_2_slice_04_failures" \
  '.ok == true and (.data | type) == "array"'

check_json "retry command can requeue indexed object for manual rebuild-style retry" \
  "loom indexes retry '$queue_status_id' --json --correlation-id corr_smoke_v0_2_slice_04_retry_one" \
  ".ok == true and .data.index_status_id == \"$queue_status_id\" and .data.status == \"queued\""

remote "loom indexes run text --once --json --correlation-id corr_smoke_v0_2_slice_04_indexer_retry_run > /tmp/loom-v0-2-slice-04-indexer-retry-run.json"
check_json "text indexer processes retried item" \
  'cat /tmp/loom-v0-2-slice-04-indexer-retry-run.json' \
  '.ok == true and .data.run.run_status == "succeeded" and .data.run.result_summary_json.indexed >= 1'

check_json "rebuild object queues latest version" \
  "loom indexes rebuild object '$object_id' --json --correlation-id corr_smoke_v0_2_slice_04_rebuild" \
  ".ok == true and .data.status == \"queued\" and .data.object_id == \"$object_id\" and .data.object_version_id == \"$version_id\""

remote "loom indexes run text --once --json --correlation-id corr_smoke_v0_2_slice_04_indexer_rebuild_run > /tmp/loom-v0-2-slice-04-indexer-rebuild-run.json"
check_json "text indexer processes rebuild item" \
  'cat /tmp/loom-v0-2-slice-04-indexer-rebuild-run.json' \
  '.ok == true and .data.run.run_status == "succeeded" and .data.run.result_summary_json.indexed >= 1'

check_json "retry-failed command is available and returns summary" \
  "loom indexes retry-failed --json --correlation-id corr_smoke_v0_2_slice_04_retry_failed" \
  '.ok == true and (.data.retried | type) == "number" and (.data.skipped | type) == "number"'

check_remote "write unsupported binary file visible to loomd" \
  "printf '\\000\\001\\002\\377' > '$UNSUPPORTED_PATH' && chmod 0644 '$UNSUPPORTED_PATH' && sudo -n -u loom test -r '$UNSUPPORTED_PATH'"

remote "loom object ingest '$UNSUPPORTED_PATH' --project '$SMOKE_SLUG' --name v0-2-slice-04-unsupported.bin --json --correlation-id corr_smoke_v0_2_slice_04_unsupported > /tmp/loom-v0-2-slice-04-unsupported.json"
unsupported_object_id="$(json_value 'cat /tmp/loom-v0-2-slice-04-unsupported.json' '.data.object.object.object_id')"
[[ "$unsupported_object_id" == object_* ]] || fail "expected unsupported object id, got $unsupported_object_id"
pass "captured unsupported object id"

check_json "unsupported object is marked skipped without worker failure" \
  "loom indexes status --object '$unsupported_object_id' --json --correlation-id corr_smoke_v0_2_slice_04_unsupported_status" \
  ".ok == true and ([.data[] | select(.index_type == \"full_text\" and .status == \"skipped_unsupported\" and .object_id == \"$unsupported_object_id\")] | length) == 1"

check_remote "index status human output works" \
  "loom indexes status --object '$object_id' | grep -q 'INDEX TYPE'"

check_remote "search human output works" \
  "loom search '$UNIQUE_TOKEN' --project '$SMOKE_SLUG' | grep -q 'document_chunk_'"

if grep -R "psql\|QueryContext\|QueryRowContext" "$REPO_ROOT/internal/loomcli" >/dev/null 2>&1; then
  fail "loom CLI contains direct PostgreSQL access"
fi
pass "CLI does not query PostgreSQL directly"

log "completed $pass_count checks"

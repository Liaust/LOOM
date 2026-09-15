#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
SMOKE_SLUG="${LOOM_SLICE_04_SMOKE_SLUG:-slice-04-smoke}"
SMOKE_NAME="${LOOM_SLICE_04_SMOKE_NAME:-Slice 4 Smoke}"
INGEST_PATH="$REMOTE_DIR/tests/smoke/slice_04_search.md"
UNSUPPORTED_PATH="$REMOTE_DIR/tests/smoke/slice_04_unsupported.bin"
UNIQUE_TOKEN="slicefoursearch$(date +%s)$RANDOM"

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
log "project slug: $SMOKE_SLUG"
log "ingest path: $INGEST_PATH"
log "unique token: $UNIQUE_TOKEN"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports migrations and bootstrap ok" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.status == "ok" and .data.checks.bootstrap.status == "ok"'

check_remote "write smoke search file visible to loomd" \
  "mkdir -p '$REMOTE_DIR/tests/smoke' && printf '# Slice 4 Smoke\n\nCapability retrieval unique token: $UNIQUE_TOKEN.\n\nThis verifies text extraction, chunk creation, full text indexing, and source-linked search results.\n' > '$INGEST_PATH' && chmod 0644 '$INGEST_PATH' && sudo -u loom test -r '$INGEST_PATH'"

check_json "project create succeeds" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --description 'Slice 4 text search smoke project' --if-not-exists --json --correlation-id corr_smoke_slice_04_project_create" \
  ".ok == true and .data.project.project.slug == \"$SMOKE_SLUG\" and (.data.project.project.project_id | startswith(\"project_\"))"

project_scope_id="$(json_value "loom project inspect '$SMOKE_SLUG' --json" '.data.project.project_scope_id')"
[[ "$project_scope_id" == scope_* ]] || fail "expected project scope id, got $project_scope_id"
pass "captured project scope id"

remote "loom object ingest '$INGEST_PATH' --project '$SMOKE_SLUG' --name slice-04-search.md --json --correlation-id corr_smoke_slice_04_object_ingest > /tmp/loom-slice-04-ingest.json"
check_json "object ingest succeeds" \
  'cat /tmp/loom-slice-04-ingest.json' \
  '.ok == true and (.data.object.object.object_id | startswith("object_")) and (.data.object.latest_version.object_version_id | startswith("version_")) and (.data.object.blob.blob_id | startswith("blob_"))'

object_id="$(json_value 'cat /tmp/loom-slice-04-ingest.json' '.data.object.object.object_id')"
version_id="$(json_value 'cat /tmp/loom-slice-04-ingest.json' '.data.object.latest_version.object_version_id')"
blob_id="$(json_value 'cat /tmp/loom-slice-04-ingest.json' '.data.object.blob.blob_id')"
[[ "$object_id" == object_* ]] || fail "expected object id, got $object_id"
[[ "$version_id" == version_* ]] || fail "expected version id, got $version_id"
[[ "$blob_id" == blob_* ]] || fail "expected blob id, got $blob_id"
pass "captured object version and blob ids"

remote "loom indexes run text --once --json --correlation-id corr_smoke_slice_04_indexer_run > /tmp/loom-slice-04-indexer-run.json"
check_json "text indexer processes queued object" \
  'cat /tmp/loom-slice-04-indexer-run.json' \
  '.ok == true and .data.run.run_status == "succeeded" and .data.run.result_summary_json.indexed >= 1'

check_json "index status includes text extraction indexed" \
  "loom index status --object '$object_id' --json" \
  ".ok == true and ([.data[] | select(.index_type == \"text_extraction\" and .status == \"indexed\" and .object_id == \"$object_id\" and .object_version_id == \"$version_id\")] | length) >= 1"

check_json "index status includes chunking indexed" \
  "loom index status --object '$object_id' --json" \
  ".ok == true and ([.data[] | select(.index_type == \"chunking\" and .status == \"indexed\" and .object_id == \"$object_id\" and .object_version_id == \"$version_id\")] | length) >= 1"

check_json "index status includes full text indexed" \
  "loom index status --object '$object_id' --json" \
  ".ok == true and ([.data[] | select(.index_type == \"full_text\" and .status == \"indexed\" and .object_id == \"$object_id\" and .object_version_id == \"$version_id\")] | length) >= 1"

check_remote "index status human output works" \
  "loom index status --object '$object_id' | grep -q 'INDEX TYPE'"

remote "loom search '$UNIQUE_TOKEN' --project '$SMOKE_SLUG' --json --correlation-id corr_smoke_slice_04_search > /tmp/loom-slice-04-search.json"
check_json "search returns source-linked result" \
  'cat /tmp/loom-slice-04-search.json' \
  ".ok == true and .data.result_count >= 1 and .data.results[0].object_id == \"$object_id\" and .data.results[0].object_version_id == \"$version_id\" and (.data.results[0].document_chunk_id | startswith(\"document_chunk_\"))"

check_json "search result has snippet scope freshness and indexed time" \
  'cat /tmp/loom-slice-04-search.json' \
  ".ok == true and (.data.results[0].snippet | contains(\"$UNIQUE_TOKEN\")) and .data.results[0].scope_id == \"$project_scope_id\" and (.data.results[0].freshness_state | length) > 0 and (.data.results[0].indexed_at | length) > 0"

check_remote "search human output works" \
  "loom search '$UNIQUE_TOKEN' --project '$SMOKE_SLUG' | grep -q 'document_chunk_'"

check_json "index rebuild queues work" \
  "loom index rebuild --object '$object_id' --json --correlation-id corr_smoke_slice_04_rebuild" \
  ".ok == true and .data.status == \"queued\" and .data.object_id == \"$object_id\" and .data.object_version_id == \"$version_id\""

remote "loom indexes run text --once --json --correlation-id corr_smoke_slice_04_rebuild_indexer_run > /tmp/loom-slice-04-rebuild-indexer-run.json"
check_json "text indexer processes rebuild work" \
  'cat /tmp/loom-slice-04-rebuild-indexer-run.json' \
  '.ok == true and .data.run.run_status == "succeeded" and .data.run.result_summary_json.indexed >= 1'

check_remote "write unsupported binary file visible to loomd" \
  "printf '\\000\\001\\002\\377' > '$UNSUPPORTED_PATH' && chmod 0644 '$UNSUPPORTED_PATH' && sudo -u loom test -r '$UNSUPPORTED_PATH'"

remote "loom object ingest '$UNSUPPORTED_PATH' --project '$SMOKE_SLUG' --name slice-04-unsupported.bin --json --correlation-id corr_smoke_slice_04_unsupported > /tmp/loom-slice-04-unsupported.json"
unsupported_object_id="$(json_value 'cat /tmp/loom-slice-04-unsupported.json' '.data.object.object.object_id')"
[[ "$unsupported_object_id" == object_* ]] || fail "expected unsupported object id, got $unsupported_object_id"
pass "captured unsupported object id"

check_json "unsupported indexing is visible as skipped" \
  "loom index status --object '$unsupported_object_id' --json" \
  ".ok == true and ([.data[] | select(.index_type == \"full_text\" and .status == \"skipped_unsupported\" and .object_id == \"$unsupported_object_id\")] | length) >= 1"

check_json "events include search indexing event" \
  "loom events list --object '$object_id' --json --limit 100" \
  '.ok == true and ([.data[].event_type] | index("text.extracted") and index("chunks.created") and index("search.indexed"))'

if grep -R "psql\|QueryContext\|QueryRowContext" "$REPO_ROOT/internal/loomcli" >/dev/null 2>&1; then
  fail "loom CLI contains direct PostgreSQL access"
fi
pass "CLI does not query PostgreSQL directly"

log "passed checks: $pass_count"

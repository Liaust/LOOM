#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
SMOKE_SLUG="${LOOM_SLICE_03_SMOKE_SLUG:-slice-03-smoke}"
SMOKE_NAME="${LOOM_SLICE_03_SMOKE_NAME:-Slice 3 Smoke}"
INGEST_PATH="$REMOTE_DIR/tests/smoke/slice_03_ingest.md"

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

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports migrations and bootstrap ok" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.status == "ok" and .data.checks.bootstrap.status == "ok"'

check_remote "write smoke ingest file visible to loomd" \
  "mkdir -p '$REMOTE_DIR/tests/smoke' && printf '# Slice 3 Smoke\n\nObject ingestion through the LOOM CLI.\n' > '$INGEST_PATH' && chmod 0644 '$INGEST_PATH' && sudo -u loom test -r '$INGEST_PATH'"

check_json "project create succeeds" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --description 'Slice 3 smoke project' --if-not-exists --json --correlation-id corr_smoke_slice_03_project_create" \
  ".ok == true and .data.project.project.slug == \"$SMOKE_SLUG\" and (.data.project.project.project_id | startswith(\"project_\"))"

check_remote "project create human output works" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --if-not-exists | grep -q \"Project existing:\\|Project created:\""

check_json "project inspect returns complete project detail" \
  "loom project inspect '$SMOKE_SLUG' --json" \
  ".ok == true and .data.project.slug == \"$SMOKE_SLUG\" and (.data.project.project_scope_id | startswith(\"scope_\")) and (.data.owner_membership.project_membership_id | startswith(\"project_membership_\")) and (.data.policy_profile.project_policy_profile_id | startswith(\"project_policy_\")) and (.data.workspace_view.workspace_view_id | startswith(\"workspace_view_\")) and .data.workspace_view.view_kind == \"metadata_only\""

check_remote "project inspect human output works" \
  "loom project inspect '$SMOKE_SLUG' | grep -q \"Project: $SMOKE_SLUG\""

project_scope_id="$(json_value "loom project inspect '$SMOKE_SLUG' --json" '.data.project.project_scope_id')"
[[ "$project_scope_id" == scope_* ]] || fail "expected project scope id, got $project_scope_id"
pass "captured project scope id"

check_json "object ingest succeeds" \
  "loom object ingest '$INGEST_PATH' --project '$SMOKE_SLUG' --name slice-03-ingest.md --json --correlation-id corr_smoke_slice_03_object_ingest" \
  '.ok == true and (.data.object.object.object_id | startswith("object_")) and (.data.object.latest_version.object_version_id | startswith("version_")) and (.data.object.blob.hash_uri | startswith("sha256:")) and (.data.object.scope_links | length) >= 1 and (.data.event_ids | length) == 3'

object_id="$(json_value "loom object ingest '$INGEST_PATH' --project '$SMOKE_SLUG' --name slice-03-ingest.md --json --correlation-id corr_smoke_slice_03_object_ingest_capture" '.data.object.object.object_id')"
[[ "$object_id" == object_* ]] || fail "expected object id, got $object_id"
pass "captured object id"

blob_id="$(json_value "loom object inspect '$object_id' --json" '.data.blob.blob_id')"
[[ "$blob_id" == blob_* ]] || fail "expected blob id, got $blob_id"
pass "captured blob id"

hash_uri="$(json_value "loom object inspect '$object_id' --json" '.data.blob.hash_uri')"
[[ "$hash_uri" == sha256:* ]] || fail "expected sha256 hash uri, got $hash_uri"
pass "captured blob hash uri"

blob_path="$(json_value "loom object inspect '$object_id' --json" '.data.blob.storage_path')"
check_remote "blob file exists at deterministic object-store path" "test -f '$blob_path'"

check_json "object inspect returns full object detail" \
  "loom object inspect '$object_id' --json" \
  ".ok == true and .data.object.object_id == \"$object_id\" and .data.latest_version.version_number == 1 and .data.file.logical_name == \"slice-03-ingest.md\" and .data.file.text_extractable == true and .data.locations[0].location_type == \"object_store\" and .data.scope_links[0].scope_id == \"$project_scope_id\""

check_remote "object inspect human output works" \
  "loom object inspect '$object_id' | grep -q \"Object: $object_id\""

check_json "object versions returns latest version" \
  "loom object versions '$object_id' --json" \
  ".ok == true and (.data | length) >= 1 and .data[0].object_id == \"$object_id\" and .data[0].version_number == 1"

check_remote "object versions human output works" \
  "loom object versions '$object_id' | grep -q \"VERSION ID\""

check_json "object list by project returns object" \
  "loom object list --project '$SMOKE_SLUG' --json" \
  ".ok == true and ([.data[].object_id] | index(\"$object_id\"))"

check_remote "object list human output works" \
  "loom object list --project '$SMOKE_SLUG' | grep -q \"OBJECT ID\""

check_json "events list by object returns object events" \
  "loom events list --object '$object_id' --json" \
  '.ok == true and (.data | length) >= 3 and ([.data[].event_type] | index("object.ingested") and index("object.version.created") and index("object.scope_linked"))'

check_json "events list by project returns object events" \
  "loom events list --project '$SMOKE_SLUG' --json --limit 100" \
  '.ok == true and (.data | length) >= 3 and ([.data[].event_type] | index("object.ingested"))'

check_json "events list filters by project create correlation id" \
  'loom events list --correlation corr_smoke_slice_03_project_create --json' \
  '.ok == true and (.data | length) >= 1 and ([.data[].event_type] | index("project.created"))'

second_blob_id="$(json_value "loom object ingest '$INGEST_PATH' --project '$SMOKE_SLUG' --name slice-03-ingest.md --json --correlation-id corr_smoke_slice_03_object_reingest" '.data.object.blob.blob_id')"
[[ "$second_blob_id" == "$blob_id" ]] || fail "expected repeated ingest to reuse blob $blob_id, got $second_blob_id"
pass "repeated ingest reuses blob row"

check_json "events list filters by object ingest correlation id" \
  'loom events list --correlation corr_smoke_slice_03_object_ingest --json' \
  '.ok == true and (.data | length) >= 3 and all(.data[]; .correlation_id == "corr_smoke_slice_03_object_ingest")'

if grep -R "psql\|QueryContext\|QueryRowContext" "$REPO_ROOT/internal/loomcli" >/dev/null 2>&1; then
  fail "loom CLI contains direct PostgreSQL access"
fi
pass "CLI does not query PostgreSQL directly"

log "passed checks: $pass_count"

#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
SMOKE_SLUG="${LOOM_SLICE_05_SMOKE_SLUG:-slice-05-smoke}"
SMOKE_NAME="${LOOM_SLICE_05_SMOKE_NAME:-Slice 5 Smoke}"
INGEST_PATH="$REMOTE_DIR/tests/smoke/slice_05_input.md"
MANIFEST_PATH="$REMOTE_DIR/tests/smoke/scripts/word_count/loom.script.yaml"
UNIQUE_TOKEN="slicefiveartifact$(date +%s)$RANDOM"

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
log "manifest path: $MANIFEST_PATH"
log "unique token: $UNIQUE_TOKEN"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports migrations and bootstrap ok" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.status == "ok" and .data.checks.bootstrap.status == "ok"'

check_remote "script package is visible to loomd" \
  "test -r '$MANIFEST_PATH' && test -x '$REMOTE_DIR/tests/smoke/scripts/word_count/word_count.sh' && sudo -u loom test -r '$MANIFEST_PATH'"

check_remote "write smoke input file visible to loomd" \
  "mkdir -p '$REMOTE_DIR/tests/smoke' && printf '# Slice 5 Smoke\n\nThis text object validates script jobs and artifact indexing.\n\nUnique artifact token: $UNIQUE_TOKEN.\n' > '$INGEST_PATH' && chmod 0644 '$INGEST_PATH' && sudo -u loom test -r '$INGEST_PATH'"

check_json "project create succeeds" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --description 'Slice 5 scripts jobs artifacts smoke project' --if-not-exists --json --correlation-id corr_smoke_slice_05_project_create" \
  ".ok == true and .data.project.project.slug == \"$SMOKE_SLUG\" and (.data.project.project.project_id | startswith(\"project_\"))"

remote "loom object ingest '$INGEST_PATH' --project '$SMOKE_SLUG' --name slice-05-input.md --json --correlation-id corr_smoke_slice_05_object_ingest > /tmp/loom-slice-05-ingest.json"
check_json "object ingest succeeds" \
  'cat /tmp/loom-slice-05-ingest.json' \
  '.ok == true and (.data.object.object.object_id | startswith("object_")) and (.data.object.latest_version.object_version_id | startswith("version_"))'

object_id="$(json_value 'cat /tmp/loom-slice-05-ingest.json' '.data.object.object.object_id')"
[[ "$object_id" == object_* ]] || fail "expected object id, got $object_id"
pass "captured source object id"

remote "loom script register '$MANIFEST_PATH' --project '$SMOKE_SLUG' --activate --json --correlation-id corr_smoke_slice_05_script_register > /tmp/loom-slice-05-register.json"
check_json "script register succeeds and activates version" \
  'cat /tmp/loom-slice-05-register.json' \
  '.ok == true and .data.script.slug == "word_count" and (.data.version.script_version_id | startswith("script_version_")) and .data.version.status == "active" and .data.activated == true'

script_id="$(json_value 'cat /tmp/loom-slice-05-register.json' '.data.script.script_id')"
script_version_id="$(json_value 'cat /tmp/loom-slice-05-register.json' '.data.version.script_version_id')"
[[ "$script_id" == script_* ]] || fail "expected script id, got $script_id"
[[ "$script_version_id" == script_version_* ]] || fail "expected script version id, got $script_version_id"
pass "captured script and active version ids"

check_json "script inspect returns active package detail" \
  'loom script inspect word_count --json' \
  ".ok == true and .data.script.script_id == \"$script_id\" and .data.active_version.script_version_id == \"$script_version_id\" and (.data.active_version.content_hash | startswith(\"sha256:\"))"

check_remote "script inspect human output works" \
  'loom script inspect word_count | grep -q "Script: word_count"'

check_json "scripts list by project returns script" \
  "loom scripts list --project '$SMOKE_SLUG' --json" \
  ".ok == true and ([.data[].script_id] | index(\"$script_id\"))"

check_remote "scripts list human output works" \
  "loom scripts list --project '$SMOKE_SLUG' | grep -q 'SLUG'"

remote "loom script run word_count --object '$object_id' --project '$SMOKE_SLUG' --json --correlation-id corr_smoke_slice_05_script_run > /tmp/loom-slice-05-run.json"
check_json "script run completes synchronously" \
  'cat /tmp/loom-slice-05-run.json' \
  ".ok == true and .data.job.job.status == \"completed\" and .data.job.job.script_id == \"$script_id\" and .data.job.job.source_object_id == \"$object_id\" and (.data.job.outputs | length) >= 1 and (.data.artifacts | length) >= 1"

job_id="$(json_value 'cat /tmp/loom-slice-05-run.json' '.data.job.job.job_id')"
artifact_id="$(json_value 'cat /tmp/loom-slice-05-run.json' '.data.artifacts[0].artifact.artifact_id')"
artifact_object_id="$(json_value 'cat /tmp/loom-slice-05-run.json' '.data.artifacts[0].artifact.object_id')"
[[ "$job_id" == job_* ]] || fail "expected job id, got $job_id"
[[ "$artifact_id" == artifact_* ]] || fail "expected artifact id, got $artifact_id"
[[ "$artifact_object_id" == object_* ]] || fail "expected artifact object id, got $artifact_object_id"
pass "captured job and artifact ids"

check_remote "script run human output works" \
  "loom script run word_count --object '$object_id' --project '$SMOKE_SLUG' | grep -q 'Status: completed'"

check_json "jobs list returns completed script job" \
  "loom jobs list --status completed --script word_count --json" \
  ".ok == true and ([.data[].job_id] | index(\"$job_id\"))"

check_remote "jobs list human output works" \
  "loom jobs list --status completed --script word_count | grep -q 'JOB ID'"

check_json "job inspect returns outputs logs and artifacts" \
  "loom job inspect '$job_id' --json" \
  ".ok == true and .data.job.status == \"completed\" and .data.job.source_object_id == \"$object_id\" and (.data.outputs | length) >= 1 and (.data.logs | length) >= 3 and ([.data.artifacts[].artifact_id] | index(\"$artifact_id\"))"

check_remote "job inspect human output works" \
  "loom job inspect '$job_id' | grep -q 'Job: $job_id'"

check_remote "job stdout logs are readable" \
  "loom job logs '$job_id' | grep -q 'counted'"

check_remote "job stderr logs are readable" \
  "loom job logs '$job_id' --stream stderr | grep -q 'diagnostic: stderr captured'"

check_remote "job runner logs are readable" \
  "loom job logs '$job_id' --stream runner | grep -q 'process finished'"

check_json "job events command returns lifecycle events" \
  "loom job events '$job_id' --json --limit 100" \
  '.ok == true and ([.data[].event_type] | index("job.created") and index("job.started") and index("job.completed") and index("script.run.started") and index("script.run.completed") and index("artifact.created"))'

check_json "artifact inspect returns linked object detail" \
  "loom artifact inspect '$artifact_id' --json" \
  ".ok == true and .data.artifact.artifact_id == \"$artifact_id\" and .data.artifact.object_id == \"$artifact_object_id\" and .data.artifact.job_id == \"$job_id\" and .data.object.object.object_id == \"$artifact_object_id\""

check_remote "artifact inspect human output works" \
  "loom artifact inspect '$artifact_id' | grep -q 'Artifact: $artifact_id'"

check_json "artifacts list by job returns artifact" \
  "loom artifacts list --job '$job_id' --json" \
  ".ok == true and ([.data[].artifact_id] | index(\"$artifact_id\"))"

check_remote "artifacts list human output works" \
  "loom artifacts list --job '$job_id' | grep -q 'ARTIFACT ID'"

check_json "artifact object is visible in project object list" \
  "loom object list --project '$SMOKE_SLUG' --type artifact --json" \
  ".ok == true and ([.data[].object_id] | index(\"$artifact_object_id\"))"

remote "loom search '$UNIQUE_TOKEN' --project '$SMOKE_SLUG' --json --correlation-id corr_smoke_slice_05_artifact_search > /tmp/loom-slice-05-search.json"
check_json "artifact report is indexed and searchable" \
  'cat /tmp/loom-slice-05-search.json' \
  ".ok == true and .data.result_count >= 1 and ([.data.results[].object_id] | index(\"$artifact_object_id\"))"

if grep -R "psql\|QueryContext\|QueryRowContext" "$REPO_ROOT/internal/loomcli" >/dev/null 2>&1; then
  fail "loom CLI contains direct PostgreSQL access"
fi
pass "CLI does not query PostgreSQL directly"

log "passed checks: $pass_count"

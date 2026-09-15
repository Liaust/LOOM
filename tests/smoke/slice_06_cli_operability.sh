#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="$(date +%s)$RANDOM"
SMOKE_SLUG="slice-06-smoke-$RUN_ID"
SMOKE_NAME="Slice 6 Smoke $RUN_ID"
AUTO_SLUG="slice-06-auto-$RUN_ID"
VERBOSE_SLUG="slice-06-verbose-$RUN_ID"
IDEM_SLUG="slice-06-idem-$RUN_ID"
CONFLICT_SLUG="slice-06-conflict-$RUN_ID"
IDEM_KEY="slice6-project-key-$RUN_ID"
INGEST_PATH="$REMOTE_DIR/tests/smoke/slice_06_input_$RUN_ID.md"
MANIFEST_PATH="$REMOTE_DIR/tests/smoke/scripts/word_count/loom.script.yaml"
UNIQUE_TOKEN="slicesixoperability$RUN_ID"

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
log "unique token: $UNIQUE_TOKEN"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health JSON returns envelope" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and (.meta.correlation_id | startswith("corr_"))'

check_remote "health plain returns one status token" \
  'status="$(loom health --plain)" && [[ "$status" =~ ^(ok|degraded|unhealthy)$ ]]'

check_remote "status human output works" \
  'loom status | grep -q "^LOOM: "'

check_json "status JSON returns summaries" \
  'loom status --json' \
  '.ok == true and .data.status == "ok" and (.data.jobs.running | type) == "number" and (.data.search.failed_recent | type) == "number" and (.data.events.recent | type) == "number" and (.meta.correlation_id | startswith("corr_"))'

check_remote "status plain returns one status token" \
  'status="$(loom status --plain)" && [[ "$status" =~ ^(ok|degraded|unhealthy)$ ]]'

check_remote "missing object JSON returns stable error envelope" \
  'set +e; loom object inspect object_missing --json --correlation-id corr_smoke_slice_06_missing_json > /tmp/loom-slice-06-missing.json 2> /tmp/loom-slice-06-missing.err; status=$?; set -e; test "$status" -ne 0 && test ! -s /tmp/loom-slice-06-missing.err && jq -e ".ok == false and .error.code == \"objects.not_found\" and .error.correlation_id == \"corr_smoke_slice_06_missing_json\" and .meta.correlation_id == \"corr_smoke_slice_06_missing_json\"" /tmp/loom-slice-06-missing.json'

check_remote "missing object human error includes code and correlation" \
  'set +e; loom object inspect object_missing --correlation-id corr_smoke_slice_06_missing_human > /tmp/loom-slice-06-missing-human.out 2> /tmp/loom-slice-06-missing-human.err; status=$?; set -e; test "$status" -ne 0 && grep -q "Error: objects.not_found: Object was not found." /tmp/loom-slice-06-missing-human.err && grep -q "Domain: objects" /tmp/loom-slice-06-missing-human.err && grep -q "Target: object_missing" /tmp/loom-slice-06-missing-human.err && grep -q "Correlation: corr_smoke_slice_06_missing_human" /tmp/loom-slice-06-missing-human.err'

check_json "events list default is bounded" \
  'loom events list --json' \
  '.ok == true and (.data | length) <= 50'

check_json "events list explicit limit works" \
  'loom events list --limit 5 --json' \
  '.ok == true and (.data | length) <= 5'

check_json "effectful command auto-generates idempotency key in JSON" \
  "loom project create 'Slice 6 Auto $RUN_ID' --slug '$AUTO_SLUG' --if-not-exists --json" \
  ".ok == true and .data.project.project.slug == \"$AUTO_SLUG\" and (.meta.idempotency_key | startswith(\"project_create_idempotency_\"))"

check_remote "verbose effectful output includes idempotency key and source metadata" \
  "loom project create 'Slice 6 Verbose $RUN_ID' --slug '$VERBOSE_SLUG' --if-not-exists --verbose | grep -q 'Idempotency key: project_create_idempotency_' && loom project inspect '$VERBOSE_SLUG' --verbose | grep -q 'Source: main-authoritative'"

remote "loom project create '$SMOKE_NAME' --slug '$IDEM_SLUG' --if-not-exists --idempotency-key '$IDEM_KEY' --json > /tmp/loom-slice-06-idem-first.json"
remote "loom project create '$SMOKE_NAME' --slug '$IDEM_SLUG' --if-not-exists --idempotency-key '$IDEM_KEY' --json > /tmp/loom-slice-06-idem-second.json"
first_project_id="$(json_value 'cat /tmp/loom-slice-06-idem-first.json' '.data.project.project.project_id')"
second_project_id="$(json_value 'cat /tmp/loom-slice-06-idem-second.json' '.data.project.project.project_id')"
[[ "$first_project_id" == project_* ]] || fail "expected first project id, got $first_project_id"
[[ "$first_project_id" == "$second_project_id" ]] || fail "expected idempotent replay to return $first_project_id, got $second_project_id"
check_json "idempotent replay exposes retry key" \
  'cat /tmp/loom-slice-06-idem-second.json' \
  ".ok == true and .meta.idempotency_key == \"$IDEM_KEY\""

check_remote "idempotent conflict returns stable code and key" \
  "set +e; loom project create 'Slice 6 Conflict $RUN_ID' --slug '$CONFLICT_SLUG' --idempotency-key '$IDEM_KEY' --json > /tmp/loom-slice-06-idem-conflict.json 2> /tmp/loom-slice-06-idem-conflict.err; status=\$?; set -e; test \"\$status\" -ne 0 && jq -e '.ok == false and .error.code == \"idempotency.conflict\" and .meta.idempotency_key == \"$IDEM_KEY\"' /tmp/loom-slice-06-idem-conflict.json >/dev/null"

check_remote "write smoke input file visible to loomd" \
  "mkdir -p '$REMOTE_DIR/tests/smoke' && printf '# Slice 6 Smoke\n\nThis validates CLI operability, idempotency, script jobs, artifact inspection, and event inspection.\n\nUnique token: $UNIQUE_TOKEN.\n' > '$INGEST_PATH' && chmod 0644 '$INGEST_PATH' && sudo -u loom test -r '$INGEST_PATH'"

check_json "project create for workflow succeeds" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --description 'Slice 6 CLI operability smoke project' --if-not-exists --json" \
  ".ok == true and .data.project.project.slug == \"$SMOKE_SLUG\""

remote "loom object ingest '$INGEST_PATH' --project '$SMOKE_SLUG' --name slice-06-input.md --json > /tmp/loom-slice-06-ingest.json"
check_json "object ingest for workflow succeeds" \
  'cat /tmp/loom-slice-06-ingest.json' \
  '.ok == true and (.data.object.object.object_id | startswith("object_"))'
object_id="$(json_value 'cat /tmp/loom-slice-06-ingest.json' '.data.object.object.object_id')"
[[ "$object_id" == object_* ]] || fail "expected object id, got $object_id"
pass "captured workflow object id"

check_remote "script package is visible to loomd" \
  "test -r '$MANIFEST_PATH' && test -x '$REMOTE_DIR/tests/smoke/scripts/word_count/word_count.sh' && sudo -u loom test -r '$MANIFEST_PATH'"

remote "loom script register '$MANIFEST_PATH' --activate --json > /tmp/loom-slice-06-register.json"
check_json "script register still works through CLI" \
  'cat /tmp/loom-slice-06-register.json' \
  '.ok == true and .data.script.slug == "word_count" and (.data.version.script_version_id | startswith("script_version_"))'

remote "loom script run word_count --object '$object_id' --project '$SMOKE_SLUG' --json > /tmp/loom-slice-06-run.json"
check_json "script run still works through CLI" \
  'cat /tmp/loom-slice-06-run.json' \
  '.ok == true and .data.job.job.status == "completed" and (.data.artifacts | length) >= 1'
job_id="$(json_value 'cat /tmp/loom-slice-06-run.json' '.data.job.job.job_id')"
artifact_id="$(json_value 'cat /tmp/loom-slice-06-run.json' '.data.artifacts[0].artifact.artifact_id')"
[[ "$job_id" == job_* ]] || fail "expected job id, got $job_id"
[[ "$artifact_id" == artifact_* ]] || fail "expected artifact id, got $artifact_id"
pass "captured job and artifact ids"

check_json "job inspect still works through CLI" \
  "loom job inspect '$job_id' --json" \
  ".ok == true and .data.job.job_id == \"$job_id\" and (.data.logs | length) >= 3"

check_json "artifact inspect still works through CLI" \
  "loom artifact inspect '$artifact_id' --json" \
  ".ok == true and .data.artifact.artifact_id == \"$artifact_id\" and .data.artifact.job_id == \"$job_id\""

check_json "job events still work through CLI" \
  "loom job events '$job_id' --json --limit 100" \
  '.ok == true and ([.data[].event_type] | index("job.created") and index("job.started") and index("job.completed"))'

if grep -R "psql\|QueryContext\|QueryRowContext" "$REPO_ROOT/internal/loomcli" >/dev/null 2>&1; then
  fail "loom CLI contains direct PostgreSQL access"
fi
pass "CLI does not query PostgreSQL directly"

log "passed checks: $pass_count"

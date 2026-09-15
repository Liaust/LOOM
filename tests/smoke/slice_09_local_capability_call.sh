#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
SMOKE_SLUG="${LOOM_SLICE_09_SMOKE_SLUG:-slice-09-smoke}"
SMOKE_NAME="${LOOM_SLICE_09_SMOKE_NAME:-Slice 9 Smoke}"
INGEST_PATH="$REMOTE_DIR/tests/smoke/slice_09_input.md"
MANIFEST_PATH="$REMOTE_DIR/tests/smoke/scripts/word_count/loom.script.yaml"
UNIQUE_TOKEN="slicenineartifact$(date +%s)$RANDOM"

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
log "remote dir: $REMOTE_DIR"
log "project slug: $SMOKE_SLUG"
log "manifest path: $MANIFEST_PATH"
log "unique token: $UNIQUE_TOKEN"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health JSON returns ok with Slice 9 migration applied" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 8'

check_remote "status read capability call completes" '
  loom capability call main@system.status.read --json > /tmp/loom-slice-09-status-call.json
  jq -e "
    .ok == true and
    .data.status == \"completed\" and
    (.data.route.route_id | startswith(\"route_\")) and
    (.data.capability_call.capability_call_id | startswith(\"capability_call_\")) and
    (.data.policy_decision_id | startswith(\"policy_decision_\")) and
    .data.result.status == \"ok\"
  " /tmp/loom-slice-09-status-call.json
'

ROUTE_ID="$(remote 'jq -r ".data.route.route_id" /tmp/loom-slice-09-status-call.json')"
CALL_ID="$(remote 'jq -r ".data.capability_call.capability_call_id" /tmp/loom-slice-09-status-call.json')"
CORRELATION_ID="$(remote 'jq -r ".meta.correlation_id" /tmp/loom-slice-09-status-call.json')"
[[ "$ROUTE_ID" == route_* ]] || fail "route id was not captured"
[[ "$CALL_ID" == capability_call_* ]] || fail "capability call id was not captured"
[[ "$CORRELATION_ID" == corr_* ]] || fail "correlation id was not captured"
pass "captured route, call, and correlation IDs"

check_json "route inspect returns completed route" \
  "loom route inspect '$ROUTE_ID' --json" \
  ".ok == true and .data.route_id == \"$ROUTE_ID\" and .data.status == \"completed\""

check_json "capability call inspect returns completed call" \
  "loom capability-call inspect '$CALL_ID' --json" \
  ".ok == true and .data.capability_call_id == \"$CALL_ID\" and .data.status == \"completed\""

check_json "events by correlation include policy route and capability call lifecycle" \
  "loom events list --json --correlation '$CORRELATION_ID' --limit 30" \
  ".ok == true and ([.data[].event_type] | index(\"policy.decision.created\") and index(\"route.created\") and index(\"route.completed\") and index(\"capability_call.created\") and index(\"capability_call.completed\"))"

IDEMPOTENCY_KEY="slice-09-replay-$(date +%s)"
check_remote "idempotent replay returns same capability call" "
  loom capability call main@system.health.read --idempotency-key '$IDEMPOTENCY_KEY' --json > /tmp/loom-slice-09-idem-first.json
  loom capability call main@system.health.read --idempotency-key '$IDEMPOTENCY_KEY' --json > /tmp/loom-slice-09-idem-second.json
  first_call=\"\$(jq -r '.data.capability_call.capability_call_id' /tmp/loom-slice-09-idem-first.json)\"
  second_call=\"\$(jq -r '.data.capability_call.capability_call_id' /tmp/loom-slice-09-idem-second.json)\"
  test \"\$first_call\" = \"\$second_call\"
"

check_remote "idempotency conflict returns stable error envelope" "
  set +e
  loom capability call main@system.status.read --idempotency-key '$IDEMPOTENCY_KEY' --json > /tmp/loom-slice-09-idem-conflict.json 2> /tmp/loom-slice-09-idem-conflict.err
  status=\$?
  set -e
  test \"\$status\" -ne 0
  test ! -s /tmp/loom-slice-09-idem-conflict.err
  jq -e '.ok == false and .error.code == \"idempotency.conflict\"' /tmp/loom-slice-09-idem-conflict.json
"

check_json "low actor can call level 1 system capability" \
  'loom capability call main@system.health.read --actor agent:dev-low --json' \
  '.ok == true and .data.status == "completed" and .data.capability_call.status == "completed"'

check_remote "reset active script grants before approval-required route check" '
  set -e
  loom grants list --json --status active --actor agent:dev-low --target-node main --capability main@script-runner.script.run |
    jq -r ".data[]?.grant_id" |
    while read -r grant; do
      if [ -n "$grant" ]; then
        loom grant revoke "$grant" --actor owner --reason "slice 9 smoke reset" --json >/dev/null
      fi
    done
'

check_remote "low actor high-level script-runner call requires approval and does not dispatch" '
  loom capability call main@script-runner.script.run \
    --actor agent:dev-low \
    --request-approval \
    --approval-reason "slice 9 smoke approval required" \
    --json > /tmp/loom-slice-09-approval-required.json
  jq -e "
    .ok == true and
    .data.status == \"approval_required\" and
    .data.route.status == \"waiting_for_approval\" and
    .data.capability_call.status == \"approval_required\" and
    (.data.policy_decision_id | startswith(\"policy_decision_\")) and
    (.data.approval_id | startswith(\"approval_\")) and
    (.data.job_id == \"\" or .data.job_id == null)
  " /tmp/loom-slice-09-approval-required.json
'

check_remote "script package is visible to loomd" \
  "test -r '$MANIFEST_PATH' && test -x '$REMOTE_DIR/tests/smoke/scripts/word_count/word_count.sh' && sudo -u loom test -r '$MANIFEST_PATH'"

check_remote "write routed script input file visible to loomd" \
  "mkdir -p '$REMOTE_DIR/tests/smoke' && printf '# Slice 9 Smoke\n\nThis text object validates routed script-runner capability calls.\n\nUnique artifact token: $UNIQUE_TOKEN.\n' > '$INGEST_PATH' && chmod 0644 '$INGEST_PATH' && sudo -u loom test -r '$INGEST_PATH'"

check_json "project create succeeds for routed script call" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --description 'Slice 9 routed capability smoke project' --if-not-exists --json --correlation-id corr_smoke_slice_09_project_create" \
  ".ok == true and .data.project.project.slug == \"$SMOKE_SLUG\" and (.data.project.project.project_id | startswith(\"project_\"))"

remote "loom object ingest '$INGEST_PATH' --project '$SMOKE_SLUG' --name slice-09-input.md --json --correlation-id corr_smoke_slice_09_object_ingest > /tmp/loom-slice-09-ingest.json"
check_json "object ingest succeeds for routed script call" \
  'cat /tmp/loom-slice-09-ingest.json' \
  '.ok == true and (.data.object.object.object_id | startswith("object_")) and (.data.object.latest_version.object_version_id | startswith("version_"))'

object_id="$(json_value 'cat /tmp/loom-slice-09-ingest.json' '.data.object.object.object_id')"
[[ "$object_id" == object_* ]] || fail "expected object id, got $object_id"
pass "captured routed script source object id"

remote "loom script register '$MANIFEST_PATH' --project '$SMOKE_SLUG' --activate --json --correlation-id corr_smoke_slice_09_script_register > /tmp/loom-slice-09-register.json"
check_json "script register succeeds for routed script call" \
  'cat /tmp/loom-slice-09-register.json' \
  '.ok == true and .data.script.slug == "word_count" and (.data.version.script_version_id | startswith("script_version_")) and .data.version.status == "active" and .data.activated == true'

check_remote "reset active routed script grants" '
  set -e
  loom grants list --json --status active --actor agent:dev-low --target-node main --capability main@script-runner.script.run |
    jq -r ".data[]?.grant_id" |
    while read -r grant; do
      if [ -n "$grant" ]; then
        loom grant revoke "$grant" --actor owner --reason "slice 9 smoke reset" --json >/dev/null
      fi
    done
'

check_remote "low actor requests script-runner approval for routed execution" '
  loom policy explain capability:main@script-runner.script.run \
    --actor agent:dev-low \
    --request-approval \
    --approval-reason "slice 9 routed script run" \
    --json > /tmp/loom-slice-09-script-approval.json
  jq -e ".ok == true and .data.decision.decision == \"approval_required\" and .data.approval.status == \"pending\"" /tmp/loom-slice-09-script-approval.json
'

SCRIPT_APPROVAL="$(remote 'jq -r ".data.approval.approval_id" /tmp/loom-slice-09-script-approval.json')"
[[ "$SCRIPT_APPROVAL" == approval_* ]] || fail "script approval id was not captured"
pass "captured routed script approval ID"

check_remote "owner approval issues one-shot routed script grant" "
  loom approval decide '$SCRIPT_APPROVAL' \
    --approve \
    --actor owner \
    --reason 'slice 9 approve routed script run' \
    --grant-ttl 15m \
    --json > /tmp/loom-slice-09-script-grant.json
  jq -e '.ok == true and .data.approval.status == \"approved\" and .data.grant.status == \"active\" and .data.grant.grant_type == \"one_shot\" and .data.grant.max_uses == 1 and .data.grant.uses_count == 0' /tmp/loom-slice-09-script-grant.json
"

SCRIPT_GRANT="$(remote 'jq -r ".data.grant.grant_id" /tmp/loom-slice-09-script-grant.json')"
[[ "$SCRIPT_GRANT" == grant_* ]] || fail "script grant id was not captured"
pass "captured routed script grant ID"

check_json "active routed script grant allows policy explain" \
  'loom policy explain capability:main@script-runner.script.run --actor agent:dev-low --json' \
  ".ok == true and .data.decision.decision == \"allow\" and .data.decision.reason_code == \"actor_allowed_by_grant\" and .data.grant.grant_id == \"$SCRIPT_GRANT\""

check_remote "low actor routed script-runner call completes as job" "
  cat > /tmp/loom-slice-09-script-call-input.json <<EOF
{\"script_ref\":\"word_count\",\"object_ref\":\"$object_id\",\"project_ref\":\"$SMOKE_SLUG\"}
EOF
  loom capability call main@script-runner.script.run \
    --actor agent:dev-low \
    --input-file /tmp/loom-slice-09-script-call-input.json \
    --json > /tmp/loom-slice-09-routed-script-run.json
  jq -e '
    .ok == true and
    .data.status == \"completed\" and
    .data.route.status == \"completed\" and
    .data.capability_call.status == \"completed\" and
    (.data.route.route_id | startswith(\"route_\")) and
    (.data.capability_call.capability_call_id | startswith(\"capability_call_\")) and
    (.data.job_id | startswith(\"job_\")) and
    .data.grant_id == \"$SCRIPT_GRANT\" and
    .data.result.job.job.status == \"completed\" and
    .data.result.job.job.source_object_id == \"$object_id\" and
    (.data.result.job.outputs | length) >= 1 and
    (.data.result.artifacts | length) >= 1 and
    .data.result_refs.job_id == .data.job_id
  ' /tmp/loom-slice-09-routed-script-run.json
"

SCRIPT_ROUTE_ID="$(json_value 'cat /tmp/loom-slice-09-routed-script-run.json' '.data.route.route_id')"
SCRIPT_CALL_ID="$(json_value 'cat /tmp/loom-slice-09-routed-script-run.json' '.data.capability_call.capability_call_id')"
SCRIPT_JOB_ID="$(json_value 'cat /tmp/loom-slice-09-routed-script-run.json' '.data.job_id')"
SCRIPT_CORRELATION_ID="$(json_value 'cat /tmp/loom-slice-09-routed-script-run.json' '.meta.correlation_id')"
SCRIPT_ARTIFACT_ID="$(json_value 'cat /tmp/loom-slice-09-routed-script-run.json' '.data.result.artifacts[0].artifact.artifact_id')"
[[ "$SCRIPT_ROUTE_ID" == route_* ]] || fail "script route id was not captured"
[[ "$SCRIPT_CALL_ID" == capability_call_* ]] || fail "script capability call id was not captured"
[[ "$SCRIPT_JOB_ID" == job_* ]] || fail "script job id was not captured"
[[ "$SCRIPT_CORRELATION_ID" == corr_* ]] || fail "script correlation id was not captured"
[[ "$SCRIPT_ARTIFACT_ID" == artifact_* ]] || fail "script artifact id was not captured"
pass "captured routed script route, call, job, and artifact IDs"

check_json "routed script route links completed job" \
  "loom route inspect '$SCRIPT_ROUTE_ID' --json" \
  ".ok == true and .data.route_id == \"$SCRIPT_ROUTE_ID\" and .data.status == \"completed\" and .data.job_id == \"$SCRIPT_JOB_ID\" and .data.grant_id == \"$SCRIPT_GRANT\""

check_json "routed script capability call links completed job" \
  "loom capability-call inspect '$SCRIPT_CALL_ID' --json" \
  ".ok == true and .data.capability_call_id == \"$SCRIPT_CALL_ID\" and .data.status == \"completed\" and .data.job_id == \"$SCRIPT_JOB_ID\" and .data.grant_id == \"$SCRIPT_GRANT\" and .data.result_refs_json.job_id == \"$SCRIPT_JOB_ID\""

check_json "routed script job inspect returns outputs logs and artifact" \
  "loom job inspect '$SCRIPT_JOB_ID' --json" \
  ".ok == true and .data.job.job_id == \"$SCRIPT_JOB_ID\" and .data.job.status == \"completed\" and .data.job.source_object_id == \"$object_id\" and (.data.outputs | length) >= 1 and (.data.logs | length) >= 3 and ([.data.artifacts[].artifact_id] | index(\"$SCRIPT_ARTIFACT_ID\"))"

check_remote "routed script job logs are readable" \
  "loom job logs '$SCRIPT_JOB_ID' | grep -q 'counted'"

check_json "routed script events include route call job and grant lifecycle" \
  "loom events list --json --correlation '$SCRIPT_CORRELATION_ID' --limit 100" \
  '.ok == true and ([.data[].event_type] | index("route.dispatched") and index("capability_call.dispatched") and index("job.completed") and index("script.run.completed") and index("grant.used") and index("route.completed") and index("capability_call.completed"))'

check_json "routed script grant is consumed after execution" \
  "loom grant inspect '$SCRIPT_GRANT' --json" \
  ".ok == true and .data.grant_id == \"$SCRIPT_GRANT\" and .data.status == \"consumed\" and .data.uses_count == 1"

check_json "consumed routed script grant no longer allows policy explain" \
  'loom policy explain capability:main@script-runner.script.run --actor agent:dev-low --json' \
  '.ok == true and .data.decision.decision == "approval_required" and .data.grant == null'

check_remote "second low actor routed script call requires approval and creates no job" "
  loom capability call main@script-runner.script.run \
    --actor agent:dev-low \
    --input-file /tmp/loom-slice-09-script-call-input.json \
    --request-approval \
    --approval-reason 'slice 9 second routed script call' \
    --json > /tmp/loom-slice-09-second-script-call.json
  jq -e '
    .ok == true and
    .data.status == \"approval_required\" and
    .data.route.status == \"waiting_for_approval\" and
    .data.capability_call.status == \"approval_required\" and
    (.data.job_id == \"\" or .data.job_id == null)
  ' /tmp/loom-slice-09-second-script-call.json
"

check_remote "missing capability returns stable error envelope" '
  set +e
  loom capability call main@system.missing --json > /tmp/loom-slice-09-missing.json 2> /tmp/loom-slice-09-missing.err
  status=$?
  set -e
  test "$status" -ne 0
  test ! -s /tmp/loom-slice-09-missing.err
  jq -e ".ok == false and .error.code == \"capability_call.failed\" and .error.domain == \"routing\"" /tmp/loom-slice-09-missing.json
'

check_remote "CLI does not query PostgreSQL directly" \
  "if grep -R \"psql\\|QueryContext\\|QueryRowContext\" '$REMOTE_DIR/internal/loomcli' >/dev/null 2>&1; then exit 1; fi"

if grep -R "psql\|QueryContext\|QueryRowContext" "$REPO_ROOT/internal/loomcli" >/dev/null 2>&1; then
  fail "local loom CLI contains direct PostgreSQL access"
fi
pass "local CLI does not query PostgreSQL directly"

log "passed checks: $pass_count"

#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-3-1-workflow-$(date +%s)-$RANDOM"
SLUG="smoke-workflow-runtime-$RUN_ID"
CAPABILITY="main@$SLUG.example_workflow"
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
log "run id: $RUN_ID"
log "project slug: $SLUG"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports v0.3.1 workflow migrations" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 34'

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-3-1-workflow-runtime.XXXXXX'")"
trap 'remote "rm -rf '\''$TMP_DIR'\''"' EXIT
PROJECT_ROOT="$TMP_DIR/$SLUG"
CALL_JSON="$TMP_DIR/workflow-call.json"
log "project root: $PROJECT_ROOT"
log "capability: $CAPABILITY"

check_remote "scaffold executable workflow project" "
  loom project scaffold 'Smoke Workflow Runtime $RUN_ID' \
    --owner-node main \
    --facets scripts,workflows \
    --slug '$SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/scaffold.out'
  chmod a+rx '$TMP_DIR'
  chmod -R a+rX '$PROJECT_ROOT'
  grep -q 'Project scaffold: created' '$TMP_DIR/scaffold.out'
"

check_remote "workflow package emits outputs logs and artifact" "
  perl -0pi -e 's/artifacts: \\[\\]/artifacts:\\n  - key: workflow_report\\n    path: artifacts\\/workflow-report.txt\\n    type: text\\/plain\\n    title: Workflow Runtime Smoke Report/' '$PROJECT_ROOT/workflows/example_workflow/loom.workflow.yaml'
  cat > '$PROJECT_ROOT/workflows/example_workflow/run.sh' <<'SH'
#!/usr/bin/env bash
set -euo pipefail

result_file=\"\${LOOM_RESULT_FILE:-}\"
artifact_dir=\"\${LOOM_ARTIFACT_DIR:-}\"
input_json=\"\${LOOM_INPUT_JSON:-{}}\"
message=\"missing\"
if command -v jq >/dev/null 2>&1; then
  message=\"\$(printf '%s' \"\$input_json\" | jq -r '.input.message // .message // \"missing\"' 2>/dev/null || printf 'invalid')\"
fi

echo \"workflow runtime smoke: \$message\"
echo \"workflow diagnostic: stderr captured\" >&2

if [ -n \"\$artifact_dir\" ]; then
  mkdir -p \"\$artifact_dir/artifacts\"
  {
    printf 'Workflow runtime smoke report\n'
    printf 'job_id=%s\n' \"\${LOOM_JOB_ID:-unknown}\"
    printf 'workflow_id=%s\n' \"\${LOOM_WORKFLOW_ID:-unknown}\"
    printf 'message=%s\n' \"\$message\"
  } > \"\$artifact_dir/artifacts/workflow-report.txt\"
fi

if [ -z \"\$result_file\" ]; then
  printf '{\"status\":\"ok\",\"outputs\":{\"message\":\"%s\",\"summary\":\"workflow runtime smoke\"},\"artifacts\":[]}\n' \"\$message\"
  exit 0
fi

jq -n \
  --arg message \"\$message\" \
  --arg report 'artifacts/workflow-report.txt' \
  '{
    status: \"ok\",
    outputs: {
      message: \$message,
      summary: \"workflow runtime smoke\",
      report_path: \$report
    },
    artifacts: [
      {
        key: \"workflow_report\",
        path: \$report,
        type: \"text/plain\",
        title: \"Workflow Runtime Smoke Report\"
      }
    ]
  }' > \"\$result_file\"
SH
  chmod +x '$PROJECT_ROOT/workflows/example_workflow/run.sh'
"

check_remote "workflow project validates as executable" "
  loom project validate '$PROJECT_ROOT' > '$TMP_DIR/validate.out'
  grep -q 'Project contract: ok' '$TMP_DIR/validate.out'
  grep -q 'Workflows: 1 discovered, 1 executable' '$TMP_DIR/validate.out'
  grep -q '$CAPABILITY' '$TMP_DIR/validate.out'
"

check_json "workflow plan records executable workflow package" \
  "loom project plan '$PROJECT_ROOT' --json" \
  ".registerable == true and
   (.workflows | length) == 1 and
   .workflows[0].schema_version == \"workflow.contract.v0.3.1\" and
   .workflows[0].implementation_kind == \"workflow\" and
   .workflows[0].executable == true and
   .workflows[0].capability_address == \"$CAPABILITY\" and
   ([.actions[] | select(.action == \"would_register_workflow_package\" and .target_ref == \"example_workflow\")] | length) == 1"

check_json "register workflow project with workflow plan" \
  "loom project register '$PROJECT_ROOT' --json" \
  ".ok == true and
   .data.detail.registration.activation_status == \"inactive\" and
   (.data.detail.registration.registration_plan.workflows | length) == 1 and
   .data.detail.registration.registration_plan.workflows[0].implementation_kind == \"workflow\""

check_remote "activate executable workflows facet" "
  loom project activate '$SLUG' --facet workflows --project-root '$PROJECT_ROOT' > '$TMP_DIR/activate.out'
"

check_json "project status records workflow runtime registration" \
  "loom project status '$SLUG' --json" \
  ".ok == true and
   ([.data.workflow_registrations[] | select(.workflow_key == \"example_workflow\" and .activation_status == \"active\" and .runtime_kind == \"workflow\" and .capability_address == \"$CAPABILITY\")] | length) == 1"

check_json "workflow inspect shows callable runtime surface" \
  "loom project workflows inspect '$SLUG' example_workflow --json" \
  ".workflow_id == \"example_workflow\" and
   .registration_status == \"active\" and
   .registered_runtime_kind == \"workflow\" and
   .capability_address == \"$CAPABILITY\" and
   (.registered_workflow_id | startswith(\"workflow_\")) and
   (.registered_workflow_version_id | startswith(\"workflow_version_\")) and
   (.runtime_binding_id | startswith(\"runtime_binding_\"))"

check_json "workflow capability has active workflow runtime binding" \
  "loom capability runtime-binding inspect '$CAPABILITY' --json" \
  ".ok == true and
   .data.binding.runtime_kind == \"workflow\" and
   .data.binding.status == \"active\" and
   .data.endpoint.compact_address == \"$CAPABILITY\""

check_json "workflow capability is inspectable" \
  "loom capability inspect '$CAPABILITY' --json" \
  ".ok == true and
   .data.endpoint.compact_address == \"$CAPABILITY\" and
   .data.runtime_binding.runtime_kind == \"workflow\""

check_remote "call executable workflow capability" "
  loom capability call '$CAPABILITY' --input '{\"message\":\"hello workflow\"}' --wait --timeout-seconds 60 --json > '$CALL_JSON'
"

check_json "workflow capability call completes with workflow run job" \
  "cat '$CALL_JSON'" \
  ".ok == true and
   .data.status == \"completed\" and
   .data.route.status == \"completed\" and
   .data.capability_call.status == \"completed\" and
   (.data.job_id | startswith(\"job_\")) and
   .data.result.job.job.job_type == \"workflow_run\" and
   .data.result.job.job.status == \"completed\" and
   (.data.result.job.outputs | length) >= 1 and
   (.data.result.artifacts | length) >= 1 and
   .data.result_refs.job_id == .data.job_id"

JOB_ID="$(json_value "cat '$CALL_JSON'" '.data.job_id')"
[[ "$JOB_ID" == job_* ]] || fail "expected workflow job id"
pass "captured workflow job id"

ARTIFACT_ID="$(json_value "loom job inspect '$JOB_ID' --json" '.data.artifacts[0].artifact_id')"
ARTIFACT_OBJECT_ID="$(json_value "loom job inspect '$JOB_ID' --json" '.data.artifacts[0].object_id')"
WORKFLOW_RUNTIME_ID="$(json_value "loom project workflows inspect '$SLUG' example_workflow --json" '.registered_workflow_id')"
[[ "$ARTIFACT_ID" == artifact_* ]] || fail "expected workflow artifact id"
[[ "$ARTIFACT_OBJECT_ID" == object_* ]] || fail "expected workflow artifact object id"
[[ "$WORKFLOW_RUNTIME_ID" == workflow_* ]] || fail "expected registered workflow id"
pass "captured workflow artifact and runtime ids"

check_json "workflow job inspect returns outputs logs and artifacts" \
  "loom job inspect '$JOB_ID' --json" \
  ".ok == true and
   .data.job.status == \"completed\" and
   .data.job.job_type == \"workflow_run\" and
   (.data.outputs | length) >= 2 and
   (.data.logs | length) >= 3 and
   ([.data.artifacts[].artifact_id] | index(\"$ARTIFACT_ID\"))"

check_remote "workflow stdout logs are readable" \
  "loom job logs '$JOB_ID' | grep -q 'workflow runtime smoke:'"

check_remote "workflow stderr logs are readable" \
  "loom job logs '$JOB_ID' --stream stderr | grep -q 'workflow diagnostic: stderr captured'"

check_remote "workflow runner logs are readable" \
  "loom job logs '$JOB_ID' --stream runner | grep -q 'process finished'"

check_json "workflow job outputs command returns result fields and artifact output" \
  "loom job outputs '$JOB_ID' --json" \
  ".ok == true and
   ([.data[] | select(.output_key == \"message\" and .status == \"created\")] | length) == 1 and
   ([.data[] | select(.output_key == \"workflow_report\" and .output_type == \"artifact\")] | length) == 1"

check_json "workflow artifact inspect returns linked object detail" \
  "loom artifact inspect '$ARTIFACT_ID' --json" \
  ".ok == true and
   .data.artifact.artifact_id == \"$ARTIFACT_ID\" and
   .data.artifact.object_id == \"$ARTIFACT_OBJECT_ID\" and
   .data.artifact.job_id == \"$JOB_ID\" and
   .data.object.object.object_id == \"$ARTIFACT_OBJECT_ID\""

check_json "workflow artifacts list by job returns artifact" \
  "loom artifacts list --job '$JOB_ID' --json" \
  ".ok == true and ([.data[].artifact_id] | index(\"$ARTIFACT_ID\"))"

check_json "jobs list by project includes workflow run" \
  "loom jobs list --project '$SLUG' --type workflow_run --json" \
  ".ok == true and ([.data[] | select(.job_id == \"$JOB_ID\" and .job_type == \"workflow_run\" and .status == \"completed\")] | length) == 1"

check_json "jobs list by workflow includes workflow run" \
  "loom jobs list --project '$SLUG' --type workflow_run --workflow '$WORKFLOW_RUNTIME_ID' --json" \
  ".ok == true and ([.data[] | select(.job_id == \"$JOB_ID\" and .workflow_id == \"$WORKFLOW_RUNTIME_ID\" and .status == \"completed\")] | length) == 1"

check_json "workflow inspect shows recent job and capability call" \
  "loom project workflows inspect '$SLUG' example_workflow --json" \
  ".registered_workflow_id == \"$WORKFLOW_RUNTIME_ID\" and
   ([.recent_jobs[]? | select(.job_id == \"$JOB_ID\" and .status == \"completed\")] | length) == 1 and
   ([.recent_capability_calls[]? | select(.job_id == \"$JOB_ID\" and .status == \"completed\")] | length) == 1"

check_json "doctor recognizes active workflow runtime rows" \
  "loom project doctor '$SLUG' --project-root '$PROJECT_ROOT' --json" \
  "([.checks[] | select(.key == \"workflows.example_workflow\" and .status == \"ok\")] | length) == 1"

log "completed $pass_count checks"

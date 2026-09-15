#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-3-slice-14-$(date +%s)-$RANDOM"
AUTO_SLUG="smoke-hardening-auto-$RUN_ID"
RESEARCH_SLUG="smoke-hardening-research-$RUN_ID"
CONN_SLUG="smoke-hardening-connector-$RUN_ID"
SAFE_AUTO_SLUG="$(printf '%s' "$AUTO_SLUG" | sed -E 's/[^[:alnum:]]+/_/g; s/^_//; s/_$//')"
TOKEN_ID="${RUN_ID//-/}"
CONN_PROVIDER_KEY="hardening_${TOKEN_ID:0:24}"
CONN_PROVIDER="main@$CONN_PROVIDER_KEY"
CONN_CAPABILITY="$CONN_PROVIDER.ping"
AUTO_CAPABILITY="main@$AUTO_SLUG.hello_world"
AUTO_ENDPOINT="${AUTO_SLUG//-/_}__example_event"
JOB_RUNNER_LOOP_PID=""
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

drive_direct_event_succeeded() {
  local direct_event_id="$1"
  local output_file="$2"
  local key_prefix="$3"

  remote "for i in \$(seq 1 80); do
    loom worker run main.direct_event_ingest --once --json --idempotency-key '${key_prefix}_ingest_'\$i > '/tmp/${key_prefix}_ingest_'\$i'.json' 2> '/tmp/${key_prefix}_ingest_'\$i'.err' || true
    loom direct-event inspect '$direct_event_id' --json > '$output_file'
    status=\$(jq -r '.data.direct_event.status' '$output_file')
    case \"\$status\" in
      completed)
        invocation_id=\$(jq -r '.data.direct_event.invocation_id // empty' '$output_file')
        if [ -n \"\$invocation_id\" ]; then
          loom worker run main.automation_dispatcher --once --json --idempotency-key '${key_prefix}_dispatcher_final_'\$i > '/tmp/${key_prefix}_dispatcher_final_'\$i'.json' 2> '/tmp/${key_prefix}_dispatcher_final_'\$i'.err' || true
          exit 0
        fi
        ;;
      invocation_created|mapped|accepted|authenticated)
        loom worker run main.automation_dispatcher --once --json --idempotency-key '${key_prefix}_dispatcher_'\$i > '/tmp/${key_prefix}_dispatcher_'\$i'.json' 2> '/tmp/${key_prefix}_dispatcher_'\$i'.err' || true
        ;;
      rejected|mapping_failed|failed|timed_out)
        cat '$output_file' >&2
        exit 1
        ;;
    esac
    sleep 1
  done
  cat '$output_file' >&2
  exit 1"
  pass "direct event reached completed status"
}

start_job_runner_loop() {
  local key_prefix="$1"

  JOB_RUNNER_LOOP_PID="$(remote "(for i in \$(seq 1 80); do loom worker run main.job_runner --once --json --idempotency-key '${key_prefix}_job_runner_'\$i > '/tmp/${key_prefix}_job_runner_'\$i'.json' 2> '/tmp/${key_prefix}_job_runner_'\$i'.err' || true; sleep 1; done) > '/tmp/${key_prefix}_job_runner_loop.log' 2>&1 & echo \$!")"
  pass "started temporary job runner loop"
}

stop_job_runner_loop() {
  if [[ -n "$JOB_RUNNER_LOOP_PID" ]]; then
    remote "kill '$JOB_RUNNER_LOOP_PID' >/dev/null 2>&1 || true" || true
    JOB_RUNNER_LOOP_PID=""
  fi
}

log "target: $HOST"
log "remote dir: $REMOTE_DIR"
log "run id: $RUN_ID"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports v0.3 project substrate migrations" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 33'

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-3-slice-14.XXXXXX'")"
cleanup() {
  stop_job_runner_loop
  remote "rm -rf '$TMP_DIR'" || true
}
trap cleanup EXIT
AUTO_ROOT="$TMP_DIR/$AUTO_SLUG"
RESEARCH_ROOT="$TMP_DIR/$RESEARCH_SLUG"
CONN_ROOT="$TMP_DIR/$CONN_SLUG"

log "automation project: $AUTO_ROOT"
log "research project: $RESEARCH_ROOT"
log "connector project: $CONN_ROOT"

check_remote "scaffold automation hardening project" "
  loom project scaffold 'Smoke Hardening Auto $RUN_ID' \
    --owner-node main \
    --preset automation \
    --slug '$AUTO_SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/auto-scaffold.out'
  chmod a+rx '$TMP_DIR'
  chmod -R a+rX '$AUTO_ROOT'
  perl -0pi -e 's/enabled: false/enabled: true/' '$AUTO_ROOT/scripts/hello_world/loom.exposure.yaml'
  grep -q 'Project scaffold: created' '$TMP_DIR/auto-scaffold.out'
"

check_remote "automation project validates with cross-facet targets" "
  loom project validate '$AUTO_ROOT' > '$TMP_DIR/auto-validate.out'
  grep -q 'Project contract: ok' '$TMP_DIR/auto-validate.out'
  grep -q '$AUTO_CAPABILITY' '$TMP_DIR/auto-validate.out'
  grep -q 'example_event' '$TMP_DIR/auto-validate.out'
"

check_remote "automation project registers idempotently" "
  loom project register '$AUTO_ROOT' > '$TMP_DIR/auto-register-1.out'
  loom project register '$AUTO_ROOT' > '$TMP_DIR/auto-register-2.out'
  grep -Eq 'Project registration: (created|updated|unchanged)' '$TMP_DIR/auto-register-1.out'
  grep -Eq 'Project registration: (created|updated|unchanged)' '$TMP_DIR/auto-register-2.out'
"

check_remote "project diff reports local expansion state against backend" "
  loom project diff '$AUTO_ROOT' --project '$AUTO_SLUG' --include-unchanged > '$TMP_DIR/auto-diff.out'
  grep -q 'Project diff:' '$TMP_DIR/auto-diff.out'
  grep -q 'contract' '$TMP_DIR/auto-diff.out'
"

check_remote "activate automation scripts and direct events" "
  loom project activate '$AUTO_SLUG' --facet scripts --project-root '$AUTO_ROOT' > '$TMP_DIR/auto-activate-scripts.out'
  loom project activate '$AUTO_SLUG' --facet direct-events --project-root '$AUTO_ROOT' > '$TMP_DIR/auto-activate-events.out'
  grep -q 'Facet: scripts activated' '$TMP_DIR/auto-activate-scripts.out'
  grep -q 'Facet: direct_events activated' '$TMP_DIR/auto-activate-events.out'
"

check_json "doctor sees registered automation project" \
  "loom project doctor '$AUTO_SLUG' --project-root '$AUTO_ROOT' --json" \
  '.project_ref == "'"$AUTO_SLUG"'" and .summary.errors == 0 and .summary.blocked == 0'

check_remote "resume automation endpoint for acceptance ingest" "
  loom direct-event endpoint resume '$AUTO_ENDPOINT' --reason 'slice 14 smoke local ingest' > '$TMP_DIR/auto-resume-endpoint.out'
  grep -q 'Status: active' '$TMP_DIR/auto-resume-endpoint.out'
"

AUTO_INGEST_JSON="$TMP_DIR/auto-ingest.json"
AUTO_DIRECT_EVENT_JSON="$TMP_DIR/auto-direct-event.json"
check_remote "automation direct-event ingest accepts mapped payload" "
  loom direct-event ingest '$AUTO_ENDPOINT' --body-json '{\"id\":\"msg-$RUN_ID\",\"message\":\"hello from slice 14\"}' --json > '$AUTO_INGEST_JSON'
  jq -e '.ok == true and .data.status == \"accepted\" and (.data.direct_event.direct_event_id | startswith(\"direct_event_\"))' '$AUTO_INGEST_JSON' >/dev/null
"

AUTO_DIRECT_EVENT_ID="$(json_value "cat '$AUTO_INGEST_JSON'" '.data.direct_event.direct_event_id')"
[[ "$AUTO_DIRECT_EVENT_ID" == direct_event_* ]] || fail "expected automation direct event id"
pass "captured automation direct event id"

start_job_runner_loop "smoke_v0_3_slice_14_$RUN_ID"
drive_direct_event_succeeded "$AUTO_DIRECT_EVENT_ID" "$AUTO_DIRECT_EVENT_JSON" "smoke_v0_3_slice_14_drive_$RUN_ID"
stop_job_runner_loop

check_json "completed automation direct event keeps project target context" \
  "loom direct-event inspect '$AUTO_DIRECT_EVENT_ID' --json" \
  ".ok == true and
   .data.direct_event.status == \"completed\" and
   .data.automation.source_kind == \"direct_event\" and
   .data.automation.project_id != null and
   (.data.direct_event.invocation_id | startswith(\"invocation_\"))"

check_remote "deactivate automation direct events and scripts idempotently" "
  loom project deactivate '$AUTO_SLUG' --facet direct-events --reason 'slice 14 smoke' > '$TMP_DIR/auto-deactivate-events.out'
  loom project deactivate '$AUTO_SLUG' --facet scripts --reason 'slice 14 smoke' > '$TMP_DIR/auto-deactivate-scripts-1.out'
  loom project deactivate '$AUTO_SLUG' --facet scripts --reason 'slice 14 smoke repeat' > '$TMP_DIR/auto-deactivate-scripts-2.out'
  grep -q 'Project deactivation: deactivated' '$TMP_DIR/auto-deactivate-events.out'
  grep -q 'Facet: direct_events' '$TMP_DIR/auto-deactivate-events.out'
  grep -q 'Project deactivation:' '$TMP_DIR/auto-deactivate-scripts-2.out'
"

check_json "automation project status records disabled script and direct-event rows" \
  "loom project status '$AUTO_SLUG' --json" \
  ".ok == true and
   ([.data.script_exposures[] | select(.script_key == \"hello_world\" and .activation_status == \"disabled\")] | length) == 1 and
   ([.data.direct_event_registrations[] | select(.event_key == \"example_event\" and .activation_status == \"disabled\")] | length) == 1"

check_remote "scaffold research hardening project" "
  loom project scaffold 'Smoke Hardening Research $RUN_ID' \
    --owner-node main \
    --preset research \
    --slug '$RESEARCH_SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/research-scaffold.out'
  chmod -R a+rX '$RESEARCH_ROOT'
  grep -q 'Project scaffold: created' '$TMP_DIR/research-scaffold.out'
"

check_remote "research project watch lifecycle is inspectable" "
  loom project validate '$RESEARCH_ROOT' > '$TMP_DIR/research-validate.out'
  loom project register '$RESEARCH_ROOT' > '$TMP_DIR/research-register.out'
  loom project watch-plan '$RESEARCH_ROOT' > '$TMP_DIR/research-watch-local.out'
  loom project apply-watch-policy '$RESEARCH_SLUG' --dry-run > '$TMP_DIR/research-apply-dry-run.out'
  loom project sync-status '$RESEARCH_SLUG' > '$TMP_DIR/research-sync-status.out'
  loom project backup-status '$RESEARCH_SLUG' > '$TMP_DIR/research-backup-status.out'
  grep -q 'Project contract: ok' '$TMP_DIR/research-validate.out'
  grep -q 'Watched roots' '$TMP_DIR/research-watch-local.out'
  grep -q 'Project sync status' '$TMP_DIR/research-sync-status.out'
  grep -q 'Project backup status' '$TMP_DIR/research-backup-status.out'
"

check_json "doctor sees research project without blocked checks" \
  "loom project doctor '$RESEARCH_SLUG' --project-root '$RESEARCH_ROOT' --json" \
  '.project_ref == "'"$RESEARCH_SLUG"'" and .summary.errors == 0 and .summary.blocked == 0'

check_remote "scaffold connector hardening project" "
  loom project scaffold 'Smoke Hardening Connector $RUN_ID' \
    --owner-node main \
    --preset connector \
    --slug '$CONN_SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/connector-scaffold.out'
  cat > '$CONN_ROOT/connectors/example_connector/loom.connector.yaml' <<YAML
kind: loom.connector
schema_version: connector.contract.v0.3

provider:
  key: $CONN_PROVIDER_KEY
  display_name: Hardening Command Connector
  description: Slice 14 deterministic command connector.
  type: connector
  version: 0.1.0
  status: draft

runtime:
  kind: command

capabilities:
  - endpoint: ping
    display_name: Ping
    description: Command-backed connector capability.
    form: job
    risk_level: low
    side_effects: []
    input_schema:
      type: object
    output_schema:
      type: object
    runtime:
      kind: command
      config:
        argv:
          - printf
          - '{\"message\":\"pong\",\"runtime\":\"command\",\"token\":\"$TOKEN_ID\"}'
        output:
          mode: json

usage_documents:
  - path: README.md
    target: provider
YAML
  chmod -R a+rX '$CONN_ROOT'
  grep -q 'Project scaffold: created' '$TMP_DIR/connector-scaffold.out'
"

check_remote "connector project activates provider surfaces" "
  loom project validate '$CONN_ROOT' > '$TMP_DIR/connector-validate.out'
  loom project register '$CONN_ROOT' > '$TMP_DIR/connector-register.out'
  loom project activate '$CONN_SLUG' --facet connectors --project-root '$CONN_ROOT' > '$TMP_DIR/connector-activate.out'
  loom project doctor '$CONN_SLUG' --project-root '$CONN_ROOT' > '$TMP_DIR/connector-doctor.out'
  grep -q 'Project contract: ok' '$TMP_DIR/connector-validate.out'
  grep -q '$CONN_PROVIDER' '$TMP_DIR/connector-activate.out'
  grep -q '$CONN_CAPABILITY' '$TMP_DIR/connector-activate.out'
  grep -q 'Project doctor:' '$TMP_DIR/connector-doctor.out'
"

check_json "connector command capability executes before deactivation" \
  "loom capability call '$CONN_CAPABILITY' --input '{}' --wait --timeout-seconds 20 --json" \
  '.ok == true and .data.status == "completed" and .data.result.runtime == "command"'

check_remote "connector project deactivates provider surfaces" "
  loom project deactivate '$CONN_SLUG' --facet connectors --reason 'slice 14 smoke' > '$TMP_DIR/connector-deactivate.out'
  grep -q 'Facet: connectors' '$TMP_DIR/connector-deactivate.out'
"

check_json "connector registration is disabled but provider identity remains queryable" \
  "loom project status '$CONN_SLUG' --json" \
  ".ok == true and
   ([.data.connector_registrations[] | select(.connector_key == \"$CONN_PROVIDER_KEY\" and .activation_status == \"disabled\")] | length) == 1"

check_remote "portal projects surface renders hardening project data" "
  loom enter --start projects --no-boot-animation --exit-after-render > '$TMP_DIR/portal-projects.out'
  grep -q '$AUTO_SLUG' '$TMP_DIR/portal-projects.out'
  grep -q '$RESEARCH_SLUG' '$TMP_DIR/portal-projects.out'
  grep -q '$CONN_SLUG' '$TMP_DIR/portal-projects.out'
"

check_remote "portal database surface renders without regressions" "
  loom enter --start database --no-boot-animation --exit-after-render > '$TMP_DIR/portal-database.out'
  grep -q 'Database And Objects' '$TMP_DIR/portal-database.out'
"

check_remote "portal exposes project doctor and diff hardening actions" "
  loom enter \
    --start projects \
    --preview-action 'project.${SAFE_AUTO_SLUG}.doctor' \
    --no-boot-animation \
    --exit-after-render > '$TMP_DIR/portal-doctor-preview.out'
  loom enter \
    --start projects \
    --preview-action 'project.${SAFE_AUTO_SLUG}.diff' \
    --no-boot-animation \
    --exit-after-render > '$TMP_DIR/portal-diff-preview.out'
  grep -q 'Run Project Doctor' '$TMP_DIR/portal-doctor-preview.out'
  grep -q 'loom project doctor $AUTO_SLUG' '$TMP_DIR/portal-doctor-preview.out'
  grep -q 'Show Project Diff' '$TMP_DIR/portal-diff-preview.out'
  grep -q 'loom project diff' '$TMP_DIR/portal-diff-preview.out'
"

pass "v0.3 Slice 14 integration hardening smoke completed with $pass_count checks"

#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
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

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports Slice 17 module migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 16'

MODULE_PATH="$REMOTE_DIR/modules/examples/loom-project-cockpit-minimal"
check_remote "example module package exists on remote" "test -f '$MODULE_PATH/module.json' && test -f '$MODULE_PATH/usage-documents/status-read.md'"

remote "loom module register '$MODULE_PATH' --json --correlation-id corr_smoke_slice_17_module_register > /tmp/loom-slice-17-register.json"
module_version_id="$(json_value 'cat /tmp/loom-slice-17-register.json' '.data.version.module_version_id')"
module_package_id="$(json_value 'cat /tmp/loom-slice-17-register.json' '.data.package.module_package_id')"
[[ "$module_version_id" == module_version_* ]] || fail "expected module version id"
[[ "$module_package_id" == module_package_* ]] || fail "expected module package id"
pass "registered example module package"

check_json "registration stores declarations without installation side effects" \
  'cat /tmp/loom-slice-17-register.json' \
  '.ok == true
   and .data.version.module_id == "loom.project-cockpit"
   and .data.version.version == "0.1.0"
   and (.data.registered == true or .data.idempotent == true)
   and (.data.requirements | length) >= 3
   and (.data.providers | length) == 1
   and (.data.capabilities | length) == 1
   and (.data.usage_documents | length) == 1
   and (.data.backup_hooks | length) == 1'

check_json "modules list includes project cockpit" \
  'loom modules list --json --limit 20' \
  '.ok == true
   and ([.data[] | select(.module_id == "loom.project-cockpit" and .latest_module_version_id == "'"$module_version_id"'")] | length) == 1'

check_json "module inspect returns latest version declarations" \
  'loom module inspect loom.project-cockpit --json' \
  '.ok == true
   and .data.module.module_id == "loom.project-cockpit"
   and .data.latest_version.module_version_id == "'"$module_version_id"'"
   and (.data.providers | length) == 1
   and (.data.capabilities | length) == 1
   and (.data.usage_documents | length) == 1'

check_json "module version inspect returns package and manifest metadata" \
  "loom module version inspect '$module_version_id' --json" \
  '.ok == true
   and .data.version.module_version_id == "'"$module_version_id"'"
   and .data.package.module_package_id == "'"$module_package_id"'"
   and (.data.version.manifest_hash | startswith("sha256:"))'

remote "loom module register '$MODULE_PATH' --json --correlation-id corr_smoke_slice_17_module_register_idempotent > /tmp/loom-slice-17-register-idempotent.json"
check_json "second identical registration is idempotent" \
  'cat /tmp/loom-slice-17-register-idempotent.json' \
  '.ok == true
   and .data.idempotent == true
   and .data.version.module_version_id == "'"$module_version_id"'"'

check_remote "module registration provider side-effect check is repeatable" '
  set +e
  loom provider inspect main@loom-project-cockpit --json > /tmp/loom-slice-17-provider-check.json
  status=$?
  set -e
  if [ "$status" -eq 0 ]; then
    jq -e ".ok == true and .data.provider.provider_type == \"module\"" /tmp/loom-slice-17-provider-check.json >/dev/null
  else
    jq -e ".ok == false and .error.code == \"capabilities.not_found\"" /tmp/loom-slice-17-provider-check.json >/dev/null
  fi
'

check_remote "module registration capability side-effect check is repeatable" '
  set +e
  loom capability inspect main@loom-project-cockpit.status.read --json > /tmp/loom-slice-17-capability-check.json
  status=$?
  set -e
  if [ "$status" -eq 0 ]; then
    jq -e ".ok == true and .data.provider.provider_type == \"module\" and (.data.endpoint.status == \"disabled\" or .data.endpoint.status == \"active\")" /tmp/loom-slice-17-capability-check.json >/dev/null
  else
    jq -e ".ok == false and .error.code == \"capabilities.not_found\"" /tmp/loom-slice-17-capability-check.json >/dev/null
  fi
'

remote "loom module install '$module_version_id' --node main --json --correlation-id corr_smoke_slice_17_module_install > /tmp/loom-slice-17-install.json"
installation_id="$(json_value 'cat /tmp/loom-slice-17-install.json' '.data.installation.module_installation_id')"
[[ "$installation_id" == module_installation_* ]] || fail "expected module installation id"
pass "installed example module package"

check_json "module install returns provider and capability rows repeatably" \
  'cat /tmp/loom-slice-17-install.json' \
  '.ok == true
   and (.data.installation.status == "installed" or .data.installation.status == "enabled" or .data.installation.status == "disabled")
   and (.data.installation.target_node_id | startswith("node_"))
   and (.data.providers | length) == 1
   and (.data.capabilities | length) == 1
   and .data.providers[0].compact_address == "main@loom-project-cockpit"
   and .data.capabilities[0].compact_address == "main@loom-project-cockpit.status.read"
   and (.data.capabilities[0].status == "disabled" or .data.capabilities[0].status == "active")'

remote "loom module disable '$installation_id' --json --correlation-id corr_smoke_slice_17_module_disable > /tmp/loom-slice-17-disable.json"
check_json "module disable keeps provider and capability non-callable" \
  'cat /tmp/loom-slice-17-disable.json' \
  '.ok == true
   and .data.installation.status == "disabled"
   and .data.providers[0].status == "disabled"
   and .data.providers[0].exposure_status == "disabled"
   and .data.capabilities[0].status == "disabled"
   and .data.capabilities[0].version_status == "disabled"
   and .data.capabilities[0].exposure_status == "disabled"'

check_json "module installation inspect returns lifecycle detail" \
  "loom module installation inspect '$installation_id' --json" \
  '.ok == true
   and .data.installation.module_installation_id == "'"$installation_id"'"
   and .data.version.module_version_id == "'"$module_version_id"'"
   and (.data.namespaces | length) >= 4
   and (.data.providers | length) == 1
   and (.data.capabilities | length) == 1'

check_json "module health is derived and recorded" \
  "loom module health '$installation_id' --json" \
  '.ok == true
   and .data.installation.module_installation_id == "'"$installation_id"'"
   and (.data.health.module_health_id | startswith("module_health_"))
   and .data.health.provider_count == 1
   and .data.health.capability_count == 1
   and (.data.health.health_status == "ok" or .data.health.health_status == "degraded")'

check_json "module providers command lists installed disabled provider" \
  "loom module providers '$installation_id' --json" \
  '.ok == true
   and (.data | length) == 1
   and .data[0].compact_address == "main@loom-project-cockpit"
   and .data[0].status == "disabled"'

check_json "module capabilities command lists disabled endpoint" \
  "loom module capabilities '$installation_id' --json" \
  '.ok == true
   and (.data | length) == 1
   and .data[0].compact_address == "main@loom-project-cockpit.status.read"
   and .data[0].status == "disabled"
   and .data[0].version_status == "disabled"
   and (.data[0].usage_document_ids | length) == 1'

check_json "provider inspect sees disabled module provider after installation" \
  'loom provider inspect main@loom-project-cockpit --json' \
  '.ok == true
   and .data.provider.provider_type == "module"
   and .data.provider.status == "disabled"
   and .data.health.health_status == "unknown"'

check_json "capability inspect sees disabled module endpoint after installation" \
  'loom capability inspect main@loom-project-cockpit.status.read --json' \
  '.ok == true
   and .data.endpoint.status == "disabled"
   and .data.provider.status == "disabled"
   and .data.active_version == null
   and (.data.usage_documents | length) == 1
   and .data.usage_documents[0].review_status == "pending_review"'

check_remote "disabled module capability is not callable through routing" '
  set +e
  loom capability call main@loom-project-cockpit.status.read --input "{}" --dry-run --json > /tmp/loom-slice-17-call-disabled.json
  status=$?
  set -e
  test "$status" -ne 0
  jq -e ".ok == false and (.error.code == \"transport.unavailable\" or .error.code == \"routing.route_failed\" or .error.code == \"capability_call.failed\")" /tmp/loom-slice-17-call-disabled.json >/dev/null
'

check_json "disabled module endpoint is excluded from default capability search" \
  'loom capabilities search "project cockpit status" --json --limit 20' \
  '.ok == true
   and ([((.data // [])[]) | select(.compact_address == "main@loom-project-cockpit.status.read")] | length) == 0'

remote "loom module enable '$installation_id' --json --correlation-id corr_smoke_slice_17_module_enable > /tmp/loom-slice-17-enable.json"
check_json "module enable activates provider without exposing endpoint" \
  'cat /tmp/loom-slice-17-enable.json' \
  '.ok == true
   and .data.installation.status == "enabled"
   and .data.providers[0].status == "active"
   and .data.providers[0].exposure_status == "installed_disabled"
   and .data.capabilities[0].status == "disabled"
   and .data.capabilities[0].version_status == "disabled"
   and .data.capabilities[0].exposure_status == "installed_disabled"'

check_remote "enabled module still does not expose callable endpoint" '
  set +e
  loom capability call main@loom-project-cockpit.status.read --input "{}" --dry-run --json > /tmp/loom-slice-17-call-enabled-provider.json
  status=$?
  set -e
  test "$status" -ne 0
  jq -e ".ok == false and (.error.code == \"transport.unavailable\" or .error.code == \"routing.route_failed\" or .error.code == \"capability_call.failed\")" /tmp/loom-slice-17-call-enabled-provider.json >/dev/null
'

remote "loom module capability expose '$installation_id' main@loom-project-cockpit.status.read --json --correlation-id corr_smoke_slice_17_capability_expose > /tmp/loom-slice-17-expose.json"
check_json "module capability exposure activates endpoint and usage docs" \
  'cat /tmp/loom-slice-17-expose.json' \
  '.ok == true
   and .data.installation.module_installation_id == "'"$installation_id"'"
   and .data.provider.compact_address == "main@loom-project-cockpit"
   and .data.provider.status == "active"
   and .data.provider.exposure_status == "exposed"
   and .data.provider.health_status == "ok"
   and .data.provider.availability_status == "available"
   and .data.capability.compact_address == "main@loom-project-cockpit.status.read"
   and .data.capability.status == "active"
   and .data.capability.version_status == "active"
   and .data.capability.exposure_status == "exposed"'

check_json "provider inspect sees routable module provider after exposure" \
  'loom provider inspect main@loom-project-cockpit --json' \
  '.ok == true
   and .data.provider.status == "active"
   and .data.health.health_status == "ok"
   and .data.health.availability_status == "available"'

check_json "capability inspect sees active module endpoint after exposure" \
  'loom capability inspect main@loom-project-cockpit.status.read --json' \
  '.ok == true
   and .data.endpoint.status == "active"
   and .data.provider.status == "active"
   and .data.active_version.status == "active"
   and (.data.usage_documents | length) == 1
   and .data.usage_documents[0].review_status == "approved"'

remote "loom capability call main@loom-project-cockpit.status.read --input '{\"project_ref\":\"slice-17-smoke\"}' --wait --json --correlation-id corr_smoke_slice_17_module_call > /tmp/loom-slice-17-call-exposed.json"
check_json "exposed module capability calls through normal routing" \
  'cat /tmp/loom-slice-17-call-exposed.json' \
  '.ok == true
   and .data.status == "completed"
   and (.data.route.route_id | startswith("route_"))
   and (.data.capability_call.capability_call_id | startswith("capability_call_"))
   and (.data.policy_decision_id | startswith("policy_decision_"))
   and .data.result.module_id == "loom.project-cockpit"
   and .data.result.installation_id == "'"$installation_id"'"
   and .data.result.version == "0.1.0"
   and .data.result.status == "enabled"
   and .data.result.requested_project_ref == "slice-17-smoke"'

route_id="$(json_value 'cat /tmp/loom-slice-17-call-exposed.json' '.data.route.route_id')"
capability_call_id="$(json_value 'cat /tmp/loom-slice-17-call-exposed.json' '.data.capability_call.capability_call_id')"
policy_decision_id="$(json_value 'cat /tmp/loom-slice-17-call-exposed.json' '.data.policy_decision_id')"
[[ "$route_id" == route_* ]] || fail "expected route id"
[[ "$capability_call_id" == capability_call_* ]] || fail "expected capability call id"
[[ "$policy_decision_id" == policy_decision_* ]] || fail "expected policy decision id"
pass "captured routing audit identifiers"

check_json "route inspect links completed module capability call" \
  "loom route inspect '$route_id' --json" \
  '.ok == true
   and .data.route_id == "'"$route_id"'"
   and .data.status == "completed"'

check_json "capability-call inspect stores module adapter result" \
  "loom capability-call inspect '$capability_call_id' --json" \
  '.ok == true
   and .data.capability_call_id == "'"$capability_call_id"'"
   and .data.status == "completed"
   and .data.result_json.module_id == "loom.project-cockpit"'

check_json "policy decision inspect shows normal allow path" \
  "loom policy decision inspect '$policy_decision_id' --json" \
  '.ok == true
   and .data.policy_decision_id == "'"$policy_decision_id"'"
   and .data.decision == "allow"'

check_json "route events are durable for exposed module call" \
  "loom events list --target-kind route --target-id '$route_id' --json --limit 20" \
  '.ok == true
   and ([.data[] | select(.event_type == "route.created")] | length) >= 1
   and ([.data[] | select(.event_type == "route.completed")] | length) >= 1'

check_json "capability call events are durable for exposed module call" \
  "loom events list --target-kind capability_call --target-id '$capability_call_id' --json --limit 20" \
  '.ok == true
   and ([.data[] | select(.event_type == "capability_call.created")] | length) >= 1
   and ([.data[] | select(.event_type == "capability_call.completed")] | length) >= 1'

check_json "exposed module endpoint appears in capability search" \
  'loom capabilities search "workflow" --json --limit 20' \
  '.ok == true
   and ([((.data // [])[]) | select(.compact_address == "main@loom-project-cockpit.status.read" and (.match_reasons | index("usage_document")))] | length) >= 1'

remote "loom agent access-session create --actor agent:dev-low --runtime-node main --home-node main --scope system --json --correlation-id corr_smoke_slice_17_agent_access > /tmp/loom-slice-17-agent-access.json"
agent_access_session_id="$(json_value 'cat /tmp/loom-slice-17-agent-access.json' '.data.agent_access_session_id')"
[[ "$agent_access_session_id" == agent_access_session_* ]] || fail "expected agent access session id"
pass "created agent access session for exposed module capability"

remote "loom agent work-context create --access-session '$agent_access_session_id' --objective 'Slice 17 module capability exposure $RUN_ID' --json --correlation-id corr_smoke_slice_17_agent_context > /tmp/loom-slice-17-agent-work-context.json"
agent_work_context_id="$(json_value 'cat /tmp/loom-slice-17-agent-work-context.json' '.data.work_context.agent_work_context_id')"
[[ "$agent_work_context_id" == agent_work_context_* ]] || fail "expected agent work context id"
pass "created agent work context for module tool visibility"

remote "loom agent tools search --work-context '$agent_work_context_id' --limit 10 'workflow' --json --correlation-id corr_smoke_slice_17_agent_search > /tmp/loom-slice-17-agent-tool-search.json"
check_json "agent tool search sees exposed module capability compactly" \
  'cat /tmp/loom-slice-17-agent-tool-search.json' \
  '.ok == true
   and ([.data.candidates[] | select(.capability_address == "main@loom-project-cockpit.status.read" and .visibility_state == "visible")] | length) == 1
   and ([.data.candidates[] | has("tool_schema") or has("input_schema_json") or has("output_schema_json")] | any) == false'

remote "loom agent tool inspect --work-context '$agent_work_context_id' main@loom-project-cockpit.status.read --query 'workflow status' --json --correlation-id corr_smoke_slice_17_agent_inspect > /tmp/loom-slice-17-agent-tool-inspect.json"
check_json "agent tool inspect returns module capability tool schema and usage" \
  'cat /tmp/loom-slice-17-agent-tool-inspect.json' \
  '.ok == true
   and .data.tool_schema.loom.capability_address == "main@loom-project-cockpit.status.read"
   and .data.tool_schema.loom.call_via == "loom.capability.call"
   and (.data.usage_sections | length) >= 1'

remote "loom agent call --work-context '$agent_work_context_id' main@loom-project-cockpit.status.read --input '{\"project_ref\":\"agent-slice-17\"}' --json --correlation-id corr_smoke_slice_17_agent_call > /tmp/loom-slice-17-agent-call.json"
check_json "agent gateway calls exposed module capability through routing" \
  'cat /tmp/loom-slice-17-agent-call.json' \
  '.ok == true
   and .data.status == "completed"
   and (.data.agent_tool_call_id | startswith("agent_tool_call_"))
   and (.data.route_id | startswith("route_"))
   and (.data.capability_call_id | startswith("capability_call_"))
   and (.data.policy_decision_id | startswith("policy_decision_"))
   and .data.routing_outcome.result.module_id == "loom.project-cockpit"
   and .data.routing_outcome.result.requested_project_ref == "agent-slice-17"'

remote "loom module backup-export '$installation_id' --kind manifest --json --correlation-id corr_smoke_slice_17_backup_export > /tmp/loom-slice-17-backup-export.json"
backup_export_id="$(json_value 'cat /tmp/loom-slice-17-backup-export.json' '.data.export.module_backup_export_id')"
[[ "$backup_export_id" == module_backup_export_* ]] || fail "expected module backup export id"
pass "exported module manifest backup"

check_json "manifest backup export stores bounded module payload" \
  'cat /tmp/loom-slice-17-backup-export.json' \
  '.ok == true
   and .data.export.module_backup_export_id == "'"$backup_export_id"'"
   and .data.export.export_kind == "manifest"
   and .data.export.export_status == "completed"
   and (.data.export.payload_hash | startswith("sha256:"))
   and .data.export.payload_json.module.module_id == "loom.project-cockpit"
   and .data.export.payload_json.installation.module_installation_id == "'"$installation_id"'"
   and (.data.export.payload_json.declarations.capabilities | length) == 1'

check_json "module backups list returns exported manifest payload" \
  "loom module backups '$installation_id' --json --kind manifest" \
  '.ok == true
   and ([.data[] | select(.module_backup_export_id == "'"$backup_export_id"'" and .export_kind == "manifest")] | length) == 1'

check_json "module backup inspect returns payload and hook declaration" \
  "loom module backup inspect '$backup_export_id' --json" \
  '.ok == true
   and .data.export.module_backup_export_id == "'"$backup_export_id"'"
   and .data.version.module_id == "loom.project-cockpit"
   and .data.hook.hook_kind == "manifest"
   and .data.export.payload_json.module.module_id == "loom.project-cockpit"'

remote "loom module capability disable '$installation_id' main@loom-project-cockpit.status.read --json --correlation-id corr_smoke_slice_17_capability_disable > /tmp/loom-slice-17-disable-capability.json"
check_json "module capability disable removes endpoint from callable surface" \
  'cat /tmp/loom-slice-17-disable-capability.json' \
  '.ok == true
   and .data.capability.compact_address == "main@loom-project-cockpit.status.read"
   and .data.capability.status == "disabled"
   and .data.capability.version_status == "disabled"
   and .data.capability.exposure_status == "disabled"'

check_remote "disabled module capability fails routing again" '
  set +e
  loom capability call main@loom-project-cockpit.status.read --input "{}" --dry-run --json > /tmp/loom-slice-17-call-after-capability-disable.json
  status=$?
  set -e
  test "$status" -ne 0
  jq -e ".ok == false and (.error.code == \"transport.unavailable\" or .error.code == \"routing.route_failed\" or .error.code == \"capability_call.failed\")" /tmp/loom-slice-17-call-after-capability-disable.json >/dev/null
'

check_json "disabled module endpoint leaves default capability search again" \
  'loom capabilities search "workflow" --json --limit 20' \
  '.ok == true
   and ([((.data // [])[]) | select(.compact_address == "main@loom-project-cockpit.status.read")] | length) == 0'

check_json "module registration events are durable and queryable" \
  "loom events list --target-kind module_version --target-id '$module_version_id' --json --limit 20" \
  '.ok == true
   and ([.data[] | select(.event_type == "module.registered")] | length) >= 1
   and ([.data[] | select(.event_type == "module.manifest.validated")] | length) >= 1'

check_json "module lifecycle events are durable and queryable" \
  "loom events list --target-kind module_installation --target-id '$installation_id' --json --limit 50" \
  '.ok == true
   and ([.data[] | select(.event_type == "module.install_started")] | length) >= 1
   and ([.data[] | select(.event_type == "module.installed")] | length) >= 1
   and ([.data[] | select(.event_type == "module.disabled")] | length) >= 1
   and ([.data[] | select(.event_type == "module.enabled")] | length) >= 1
   and ([.data[] | select(.event_type == "module.capability_exposed")] | length) >= 1
   and ([.data[] | select(.event_type == "module.capability_disabled")] | length) >= 1
   and ([.data[] | select(.event_type == "module.backup_exported")] | length) >= 1'

log "passed: $pass_count checks"

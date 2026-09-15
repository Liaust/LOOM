#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
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

check_json "health reports Slice 16 agent migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 13'

remote "loom agent access-session create --actor agent:dev-low --runtime-node main --home-node main --scope system --json --correlation-id corr_smoke_slice_16_access_session > /tmp/loom-slice-16-access-session.json"
access_session_id="$(json_value 'cat /tmp/loom-slice-16-access-session.json' '.data.agent_access_session_id')"
[[ "$access_session_id" == agent_access_session_* ]] || fail "expected agent access session id"
pass "created agent access session"

check_json "access session inspect succeeds" \
  "loom agent access-session inspect '$access_session_id' --json" \
  ".ok == true and .data.agent_access_session_id == \"$access_session_id\" and .data.status == \"active\""

remote "loom agent work-context create --access-session '$access_session_id' --objective 'Slice 16 smoke work context $RUN_ID' --json --correlation-id corr_smoke_slice_16_work_context > /tmp/loom-slice-16-work-context.json"
work_context_id="$(json_value 'cat /tmp/loom-slice-16-work-context.json' '.data.work_context.agent_work_context_id')"
tool_view_id="$(json_value 'cat /tmp/loom-slice-16-work-context.json' '.data.work_context.active_tool_view_id')"
[[ "$work_context_id" == agent_work_context_* ]] || fail "expected agent work context id"
[[ "$tool_view_id" == tool_view_* ]] || fail "expected tool view id"
pass "created agent work context with active tool view"

check_json "work context inspect returns active tool view" \
  "loom agent work-context inspect '$work_context_id' --json" \
  ".ok == true and .data.work_context.agent_work_context_id == \"$work_context_id\" and .data.tool_view.tool_view.tool_view_id == \"$tool_view_id\""

check_json "tool view contains operating tools" \
  "loom agent tool-view inspect '$tool_view_id' --json" \
  '.ok == true
   and ([.data.entries[].tool_name] | index("loom.capability.search"))
   and ([.data.entries[].tool_name] | index("loom.capability.inspect"))
   and ([.data.entries[].tool_name] | index("loom.capability.call"))
   and ([.data.entries[].tool_name] | index("loom.worklog.write"))'

check_json "tool view contains visible safe system capability" \
  "loom agent tool-view inspect '$tool_view_id' --json" \
  '.ok == true
   and ([.data.entries[] | select(.capability_address == "main@system.status.read" and .visibility_state == "visible")] | length) == 1'

check_json "tool view contains requestable higher-auth capability" \
  "loom agent tool-view inspect '$tool_view_id' --json" \
  '.ok == true
   and ([.data.entries[] | select(.capability_address == "main@script-runner.script.run" and .visibility_state == "requestable" and .approval_hint == "approval_required")] | length) == 1'

check_json "tool view entries remain compact" \
  "loom agent tool-view inspect '$tool_view_id' --json" \
  '.ok == true
   and ([.data.entries[] | has("input_schema_json") or has("output_schema_json")] | any) == false'

remote "loom agent tools search --work-context '$work_context_id' --limit 10 'status' --json --correlation-id corr_smoke_slice_16_tool_search > /tmp/loom-slice-16-tool-search.json"
check_json "agent tool search returns compact visible status candidate" \
  'cat /tmp/loom-slice-16-tool-search.json' \
  '.ok == true
   and .data.agent_work_context_id == "'"$work_context_id"'"
   and ([.data.candidates[] | select(.capability_address == "main@system.status.read" and .visibility_state == "visible")] | length) == 1
   and ([.data.candidates[] | has("tool_schema") or has("input_schema_json") or has("output_schema_json")] | any) == false'

remote "loom agent tools search --work-context '$work_context_id' --limit 10 'script' --json --correlation-id corr_smoke_slice_16_tool_search_requestable > /tmp/loom-slice-16-tool-search-requestable.json"
check_json "agent tool search returns requestable high-auth candidate" \
  'cat /tmp/loom-slice-16-tool-search-requestable.json' \
  '.ok == true
   and ([.data.candidates[] | select(.capability_address == "main@script-runner.script.run" and .visibility_state == "requestable" and .approval_hint == "approval_required")] | length) == 1'

remote "loom agent tool inspect --work-context '$work_context_id' 'main@system.status.read' --query 'status health' --json --correlation-id corr_smoke_slice_16_tool_inspect > /tmp/loom-slice-16-tool-inspect.json"
check_json "agent tool inspect returns exact model-facing schema" \
  'cat /tmp/loom-slice-16-tool-inspect.json' \
  '.ok == true
   and .data.agent_work_context_id == "'"$work_context_id"'"
   and .data.tool_schema.loom.capability_address == "main@system.status.read"
   and .data.tool_schema.loom.call_via == "loom.capability.call"
   and (.data.tool_schema.input_schema | type) == "object"
   and (.data.tool_schema.output_schema | type) == "object"'

check_json "agent tool inspect includes selected usage sections only after inspection" \
  'cat /tmp/loom-slice-16-tool-inspect.json' \
  '.ok == true
   and (.data.usage_sections | type) == "array"
   and ([.data.usage_sections[]? | has("excerpt") and (has("body") | not)] | all)'

check_json "agent operating tool inspect returns internal schema" \
  "loom agent tool inspect --work-context '$work_context_id' 'loom.capability.search' --json" \
  '.ok == true
   and .data.entry.entry_kind == "operating_tool"
   and .data.tool_schema.name == "loom.capability.search"
   and .data.tool_schema.loom.call_via == "loom.capability.search"
   and (.data.tool_schema.input_schema | type) == "object"'

check_json "agent tool inspect allows requestable capability documentation" \
  "loom agent tool inspect --work-context '$work_context_id' 'main@script-runner.script.run' --query 'word count script' --json" \
  '.ok == true
   and .data.entry.visibility_state == "requestable"
   and .data.policy_hint.approval_hint == "approval_required"
   and (.data.usage_sections | length) >= 1'

remote "loom agent call --work-context '$work_context_id' 'main@system.status.read' --input '{}' --json --correlation-id corr_smoke_slice_16_agent_safe_call > /tmp/loom-slice-16-agent-call.json"
check_json "agent gateway calls visible safe capability through routing" \
  'cat /tmp/loom-slice-16-agent-call.json' \
  '.ok == true
   and .data.status == "completed"
   and (.data.agent_tool_call_id | startswith("agent_tool_call_"))
   and (.data.route_id | startswith("route_"))
   and (.data.capability_call_id | startswith("capability_call_"))
   and (.data.policy_decision_id | startswith("policy_decision_"))
   and .data.routing_outcome.capability_call.status == "completed"
   and .data.agent_tool_call.status == "completed"'

agent_tool_call_id="$(json_value 'cat /tmp/loom-slice-16-agent-call.json' '.data.agent_tool_call_id')"
[[ "$agent_tool_call_id" == agent_tool_call_* ]] || fail "expected agent tool call id"
check_json "agent tool-call inspect links routing audit records" \
  "loom agent tool-call inspect '$agent_tool_call_id' --json" \
  ".ok == true
   and .data.agent_tool_call_id == \"$agent_tool_call_id\"
   and .data.status == \"completed\"
   and (.data.route_id | startswith(\"route_\"))
   and (.data.capability_call_id | startswith(\"capability_call_\"))
   and (.data.policy_decision_id | startswith(\"policy_decision_\"))"

remote "loom agent call --work-context '$work_context_id' 'loom.capability.call' --input '{\"capability_ref\":\"main@system.status.read\",\"input\":{}}' --json --correlation-id corr_smoke_slice_16_operating_capability_call > /tmp/loom-slice-16-operating-capability-call.json"
check_json "operating capability.call delegates to normal routing path" \
  'cat /tmp/loom-slice-16-operating-capability-call.json' \
  '.ok == true
   and .data.status == "completed"
   and .data.operating_result.status == "completed"
   and (.data.operating_result.capability_call_id | startswith("capability_call_"))
   and (.data.operating_result.route_id | startswith("route_"))
   and (.data.operating_result.policy_decision_id | startswith("policy_decision_"))'

check_remote "reset active script grants before agent approval-required check" '
  set -e
  loom grants list --json --status active --actor agent:dev-low --target-node main --capability main@script-runner.script.run |
    jq -r ".data[]?.grant_id" |
    while read -r grant; do
      if [ -n "$grant" ]; then
        loom grant revoke "$grant" --actor owner --reason "slice 16 smoke reset" --json >/dev/null
      fi
    done
'

remote "loom agent call --work-context '$work_context_id' 'main@script-runner.script.run' --request-approval --approval-reason 'slice 16 agent approval required' --json --correlation-id corr_smoke_slice_16_agent_approval > /tmp/loom-slice-16-agent-approval.json"
check_json "agent gateway returns stable approval-required response" \
  'cat /tmp/loom-slice-16-agent-approval.json' \
  '.ok == true
   and .data.status == "approval_required"
   and .data.next_action_hint == "wait_for_approval_or_owner_decision"
   and (.data.approval_id | startswith("approval_"))
   and (.data.route_id | startswith("route_"))
   and (.data.capability_call_id | startswith("capability_call_"))
   and (.data.policy_decision_id | startswith("policy_decision_"))
   and .data.routing_outcome.route.status == "waiting_for_approval"
   and .data.routing_outcome.capability_call.status == "approval_required"
   and .data.agent_tool_call.status == "approval_required"'

approval_id="$(json_value 'cat /tmp/loom-slice-16-agent-approval.json' '.data.approval_id')"
[[ "$approval_id" == approval_* ]] || fail "expected agent approval id"
check_json "agent approval-required response creates approval notification" \
  "loom realtime notifications list --approval '$approval_id' --json" \
  ".ok == true and ([.data[] | select(.approval_id == \"$approval_id\" and .category == \"approval\")] | length) >= 1"

remote "loom agent worklog write --work-context '$work_context_id' --kind result --summary 'Slice 16 result $RUN_ID' --body 'Agent gateway smoke completed a safe capability call and approval-required call.' --json --correlation-id corr_smoke_slice_16_worklog_write > /tmp/loom-slice-16-worklog-write.json"
worklog_entry_id="$(json_value 'cat /tmp/loom-slice-16-worklog-write.json' '.data.worklog_entry_id')"
[[ "$worklog_entry_id" == worklog_entry_* ]] || fail "expected worklog entry id"
check_json "agent worklog write is inspectable" \
  'cat /tmp/loom-slice-16-worklog-write.json' \
  '.ok == true
   and (.data.worklog_entry_id | startswith("worklog_entry_"))
   and .data.entry_kind == "result"
   and .data.summary != ""'

check_json "agent worklog list returns written result" \
  "loom agent worklog list --work-context '$work_context_id' --json" \
  ".ok == true and ([.data[] | select(.worklog_entry_id == \"$worklog_entry_id\" and .entry_kind == \"result\")] | length) == 1"

remote "loom agent call --work-context '$work_context_id' 'loom.worklog.write' --input '{\"entry_kind\":\"observation\",\"summary\":\"Slice 16 operating worklog $RUN_ID\",\"body\":\"Written through loom.worklog.write.\"}' --json --correlation-id corr_smoke_slice_16_operating_worklog > /tmp/loom-slice-16-operating-worklog.json"
check_json "operating worklog.write writes inspectable entry" \
  'cat /tmp/loom-slice-16-operating-worklog.json' \
  '.ok == true
   and .data.status == "completed"
   and (.data.worklog_entry_id | startswith("worklog_entry_"))
   and (.data.operating_result.worklog_entry_id | startswith("worklog_entry_"))
   and .data.operating_result.entry_kind == "observation"'

log "completed with $pass_count checks"

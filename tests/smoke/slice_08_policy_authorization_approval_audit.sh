#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"

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

log "target: $HOST"
log "remote dir: $REMOTE_DIR"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health JSON returns ok" \
  'loom health --json' \
  '.ok == true and .data.status == "ok"'

check_json "admin provider is seeded" \
  'loom provider inspect main@admin --json' \
  '.ok == true and .data.provider.compact_address == "main@admin" and ([.data.endpoints[].compact_address] | index("main@admin.policy.explain") and index("main@admin.approval.decide") and index("main@admin.grant.revoke"))'

check_json "admin capabilities are discoverable" \
  'loom capabilities list --json --provider main@admin --limit 20' \
  '.ok == true and ([.data[].compact_address] | index("main@admin.approval.list") and index("main@admin.grant.list") and index("main@admin.security.audit.read"))'

check_json "admin approval usage docs are attached" \
  'loom capability usage-docs main@admin.approval.decide --json' \
  '.ok == true and (.data | length) >= 1 and .data[0].review_status == "approved" and (.data[0].body | contains("bounded grant"))'

check_remote "reset active smoke grants" '
  set -e
  for capability in main@script-runner.script.run main@object-store.object.ingest; do
    loom grants list --json --status active --actor agent:dev-low --target-node main --capability "$capability" |
      jq -r ".data[]?.grant_id" |
      while read -r grant; do
        if [ -n "$grant" ]; then
          loom grant revoke "$grant" --actor owner --reason "slice 8 smoke reset" --json >/dev/null
        fi
      done
  done
'

check_remote "low actor script run requests approval" '
  loom policy explain capability:main@script-runner.script.run \
    --actor agent:dev-low \
    --request-approval \
    --approval-reason "slice 8 smoke script run" \
    --json > /tmp/loom-slice-08-script-approval.json
  jq -e ".ok == true and .data.decision.decision == \"approval_required\" and .data.approval.status == \"pending\"" /tmp/loom-slice-08-script-approval.json
'

SCRIPT_APPROVAL="$(remote 'jq -r ".data.approval.approval_id" /tmp/loom-slice-08-script-approval.json')"
[[ "$SCRIPT_APPROVAL" == approval_* ]] || fail "script approval id was not captured"
pass "captured script approval ID"

check_json "approval inspect returns pending request" \
  "loom approval inspect '$SCRIPT_APPROVAL' --json" \
  ".ok == true and .data.approval_id == \"$SCRIPT_APPROVAL\" and .data.status == \"pending\""

check_json "approvals list filters by actor and capability" \
  'loom approvals list --json --status pending --actor agent:dev-low --target-node main --capability main@script-runner.script.run --limit 20' \
  ".ok == true and ([.data[].approval_id] | index(\"$SCRIPT_APPROVAL\"))"

check_remote "owner approval issues bounded grant" "
  loom approval decide '$SCRIPT_APPROVAL' \
    --approve \
    --actor owner \
    --reason 'slice 8 smoke approve' \
    --grant-ttl 15m \
    --json > /tmp/loom-slice-08-script-grant.json
  jq -e '.ok == true and .data.approval.status == \"approved\" and .data.grant.status == \"active\" and .data.grant.grant_type == \"one_shot\" and .data.grant.max_authorization_level == 4 and .data.grant.max_risk_level == \"high\"' /tmp/loom-slice-08-script-grant.json
"

SCRIPT_GRANT="$(remote 'jq -r ".data.grant.grant_id" /tmp/loom-slice-08-script-grant.json')"
[[ "$SCRIPT_GRANT" == grant_* ]] || fail "script grant id was not captured"
pass "captured script grant ID"

check_json "grant inspect returns active grant" \
  "loom grant inspect '$SCRIPT_GRANT' --json" \
  ".ok == true and .data.grant_id == \"$SCRIPT_GRANT\" and .data.status == \"active\""

check_json "grants list filters by actor and capability" \
  'loom grants list --json --status active --actor agent:dev-low --target-node main --capability main@script-runner.script.run --limit 20' \
  ".ok == true and ([.data[].grant_id] | index(\"$SCRIPT_GRANT\"))"

check_json "active grant allows later policy explain" \
  'loom policy explain capability:main@script-runner.script.run --actor agent:dev-low --json' \
  ".ok == true and .data.decision.decision == \"allow\" and .data.decision.reason_code == \"actor_allowed_by_grant\" and .data.grant.grant_id == \"$SCRIPT_GRANT\""

check_json "approved approval event is auditable" \
  "loom security audit --json --type approval.approved --target-id '$SCRIPT_APPROVAL' --limit 20" \
  ".ok == true and ([.data[].target_id] | index(\"$SCRIPT_APPROVAL\"))"

check_remote "owner can revoke active grant" "
  loom grant revoke '$SCRIPT_GRANT' --actor owner --reason 'slice 8 smoke revoke' --json > /tmp/loom-slice-08-revoked-grant.json
  jq -e '.ok == true and .data.status == \"revoked\" and .data.grant_id == \"$SCRIPT_GRANT\"' /tmp/loom-slice-08-revoked-grant.json
"

check_json "revoked grant event is visible in security audit" \
  "loom security audit --json --type grant.revoked --target-id '$SCRIPT_GRANT' --limit 20" \
  ".ok == true and ([.data[].target_id] | index(\"$SCRIPT_GRANT\"))"

check_json "revoked grant no longer covers policy explain" \
  'loom policy explain capability:main@script-runner.script.run --actor agent:dev-low --json' \
  '.ok == true and .data.decision.decision == "approval_required" and .data.grant == null'

check_remote "denied approval creates no grant" '
  loom policy explain capability:main@object-store.object.ingest \
    --actor agent:dev-low \
    --request-approval \
    --approval-reason "slice 8 smoke deny" \
    --json > /tmp/loom-slice-08-object-approval.json
  object_approval="$(jq -r ".data.approval.approval_id" /tmp/loom-slice-08-object-approval.json)"
  loom approval decide "$object_approval" --deny --actor owner --reason "slice 8 smoke deny" --json > /tmp/loom-slice-08-denied-approval.json
	jq -e ".ok == true and .data.approval.status == \"denied\" and (.data.grant == null)" /tmp/loom-slice-08-denied-approval.json
	test "$(loom grants list --json --approval "$object_approval" | jq ".data | length")" = "0"
'

check_remote "pending approvals expire before list" '
  loom policy explain capability:main@object-store.object.ingest \
    --actor agent:dev-low \
    --request-approval \
    --approval-reason "slice 8 smoke expire approval" \
    --approval-ttl-seconds 1 \
    --json > /tmp/loom-slice-08-expiring-approval.json
  expiring_approval="$(jq -r ".data.approval.approval_id" /tmp/loom-slice-08-expiring-approval.json)"
  sleep 2
	loom approvals list --json --status expired --actor agent:dev-low --capability main@object-store.object.ingest --limit 20 > /tmp/loom-slice-08-expired-approvals.json
	jq -e --arg approval "$expiring_approval" ".ok == true and ([.data[].approval_id] | index(\$approval))" /tmp/loom-slice-08-expired-approvals.json
'

check_remote "active grants expire before list" '
  loom policy explain capability:main@script-runner.script.run \
    --actor agent:dev-low \
    --request-approval \
    --approval-reason "slice 8 smoke expire grant" \
    --json > /tmp/loom-slice-08-expiring-grant-approval.json
  grant_approval="$(jq -r ".data.approval.approval_id" /tmp/loom-slice-08-expiring-grant-approval.json)"
  loom approval decide "$grant_approval" --approve --actor owner --reason "slice 8 smoke short grant" --grant-ttl 1s --json > /tmp/loom-slice-08-expiring-grant.json
  expiring_grant="$(jq -r ".data.grant.grant_id" /tmp/loom-slice-08-expiring-grant.json)"
  sleep 2
	loom grants list --json --status expired --actor agent:dev-low --capability main@script-runner.script.run --limit 20 > /tmp/loom-slice-08-expired-grants.json
	jq -e --arg grant "$expiring_grant" ".ok == true and ([.data[].grant_id] | index(\$grant))" /tmp/loom-slice-08-expired-grants.json
'

check_remote "unsupported policy operation returns error envelope" \
  'set +e; loom policy explain unsupported.operation --json > /tmp/loom-slice-08-unsupported.json 2> /tmp/loom-slice-08-unsupported.err; status=$?; set -e; test "$status" -ne 0 && test ! -s /tmp/loom-slice-08-unsupported.err && jq -e ".ok == false and .error.code == \"policy.unsupported_operation\" and .meta.correlation_id == .error.correlation_id" /tmp/loom-slice-08-unsupported.json'

check_remote "CLI does not query PostgreSQL directly" \
  "if grep -R \"psql\\|QueryContext\\|QueryRowContext\" '$REMOTE_DIR/internal/loomcli' >/dev/null 2>&1; then exit 1; fi"

if grep -R "psql\|QueryContext\|QueryRowContext" "$REPO_ROOT/internal/loomcli" >/dev/null 2>&1; then
  fail "local loom CLI contains direct PostgreSQL access"
fi
pass "local CLI does not query PostgreSQL directly"

log "passed checks: $pass_count"

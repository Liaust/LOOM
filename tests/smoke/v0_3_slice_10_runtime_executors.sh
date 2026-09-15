#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-3-slice-10-$(date +%s)-$RANDOM"
TOKEN_ID="${RUN_ID//-/}"
CMD_SLUG="smoke-command-$RUN_ID"
HTTP_SLUG="smoke-http-$RUN_ID"
CMD_PROVIDER_KEY="cmd_${TOKEN_ID:0:24}"
HTTP_PROVIDER_KEY="http_${TOKEN_ID:0:24}"
CMD_PROVIDER="main@$CMD_PROVIDER_KEY"
HTTP_PROVIDER="main@$HTTP_PROVIDER_KEY"
CMD_CAPABILITY="$CMD_PROVIDER.ping"
HTTP_CAPABILITY="$HTTP_PROVIDER.ping"
HTTP_PORT=$((18000 + RANDOM % 1000))
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
log "command capability: $CMD_CAPABILITY"
log "http capability: $HTTP_CAPABILITY"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'
check_remote "nc available for local HTTP runtime smoke" 'command -v nc'
check_remote "curl available for local HTTP runtime smoke" 'command -v curl'

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-3-slice-10.XXXXXX'")"
trap 'remote "if [ -f \"$TMP_DIR/http.pid\" ]; then kill \$(cat \"$TMP_DIR/http.pid\") >/dev/null 2>&1 || true; fi; rm -rf \"$TMP_DIR\""' EXIT

write_command_connector() {
  local project_root="$1"
  remote "cat > '$project_root/connectors/example_connector/loom.connector.yaml' <<YAML
kind: loom.connector
schema_version: connector.contract.v0.3

provider:
  key: $CMD_PROVIDER_KEY
  display_name: Command Runtime Connector
  description: Command runtime smoke connector.
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
YAML"
}

write_http_connector() {
  local project_root="$1"
  remote "cat > '$project_root/connectors/example_connector/loom.connector.yaml' <<YAML
kind: loom.connector
schema_version: connector.contract.v0.3

provider:
  key: $HTTP_PROVIDER_KEY
  display_name: HTTP Runtime Connector
  description: HTTP runtime smoke connector.
  type: connector
  version: 0.1.0
  status: draft

runtime:
  kind: http

capabilities:
  - endpoint: ping
    display_name: Ping
    description: HTTP-backed connector capability.
    form: job
    risk_level: low
    side_effects: []
    input_schema:
      type: object
    output_schema:
      type: object
    runtime:
      kind: http
      config:
        method: POST
        url: http://127.0.0.1:$HTTP_PORT/run
        response:
          mode: json
        network:
          allow_public: false
          allowed_hosts:
            - 127.0.0.1
            - localhost

usage_documents:
  - path: README.md
    target: provider
YAML"
}

start_http_server() {
  remote "cat > '$TMP_DIR/http_server.sh' <<'SH'
#!/usr/bin/env bash
set -euo pipefail

port=\"\$1\"
body='{\"message\":\"pong\",\"runtime\":\"http\"}'
length=\"\${#body}\"

while true; do
  {
    printf 'HTTP/1.1 200 OK\r\n'
    printf 'Content-Type: application/json\r\n'
    printf 'Content-Length: %s\r\n' \"\$length\"
    printf 'Connection: close\r\n'
    printf '\r\n'
    printf '%s' \"\$body\"
  } | nc -l -N 127.0.0.1 \"\$port\" >/dev/null 2>&1 || sleep 0.1
done
SH
chmod +x '$TMP_DIR/http_server.sh'
'$TMP_DIR/http_server.sh' '$HTTP_PORT' > '$TMP_DIR/http.log' 2>&1 &
echo \$! > '$TMP_DIR/http.pid'
for _ in 1 2 3 4 5; do
  if curl -fsS --max-time 2 'http://127.0.0.1:$HTTP_PORT/run' >/dev/null 2>&1; then
    exit 0
  fi
  sleep 0.5
done
cat '$TMP_DIR/http.log' >&2 || true
exit 1"
}

PROJECT_CMD="$TMP_DIR/$CMD_SLUG"
PROJECT_HTTP="$TMP_DIR/$HTTP_SLUG"
log "command project: $PROJECT_CMD"
log "http project: $PROJECT_HTTP"

check_remote "scaffold command connector project" "
  loom project scaffold 'Smoke Command $RUN_ID' \
    --owner-node main \
    --preset connector \
    --slug '$CMD_SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/scaffold-command.out'
  chmod a+rx '$TMP_DIR'
  chmod -R a+rX '$PROJECT_CMD'
"
write_command_connector "$PROJECT_CMD"

check_remote "command connector validates" "
  loom project validate '$PROJECT_CMD' > '$TMP_DIR/validate-command.out'
  grep -q 'Project contract: ok' '$TMP_DIR/validate-command.out'
  grep -q '$CMD_CAPABILITY' '$TMP_DIR/validate-command.out'
"

check_remote "register and activate command connector" "
  loom project register '$PROJECT_CMD' > '$TMP_DIR/register-command.out'
  loom project activate '$CMD_SLUG' --facet connectors > '$TMP_DIR/activate-command.out'
  grep -q '$CMD_CAPABILITY' '$TMP_DIR/activate-command.out'
"

check_json "command connector runtime binding is active" \
  "loom capability inspect '$CMD_CAPABILITY' --json" \
  ".ok == true and .data.runtime_binding.runtime_kind == \"command\" and .data.runtime_binding.status == \"active\""

check_json "command runtime binding validates" \
  "loom capability runtime-binding validate '$CMD_CAPABILITY' --json" \
  ".ok == true and .data.validation.valid == true and .data.binding.binding.runtime_kind == \"command\""

check_json "command runtime binding test executes" \
  "loom capability runtime-binding test '$CMD_CAPABILITY' --input-file /dev/null --json" \
  ".ok == true and .data.status == \"completed\" and .data.result.runtime == \"command\""

CMD_CALL_JSON="$TMP_DIR/command-call.json"
check_remote "command runtime capability call completes" "
  loom capability call '$CMD_CAPABILITY' --input '{}' --wait --timeout-seconds 20 --json > '$CMD_CALL_JSON'
  jq -e '.ok == true and
    .data.status == \"completed\" and
    .data.route.status == \"completed\" and
    .data.capability_call.status == \"completed\" and
    .data.result.runtime == \"command\" and
    .data.result_refs.runtime_kind == \"command\"' '$CMD_CALL_JSON' >/dev/null
"

check_remote "scaffold http connector project" "
  loom project scaffold 'Smoke HTTP $RUN_ID' \
    --owner-node main \
    --preset connector \
    --slug '$HTTP_SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/scaffold-http.out'
  chmod a+rx '$TMP_DIR'
  chmod -R a+rX '$PROJECT_HTTP'
"
write_http_connector "$PROJECT_HTTP"
start_http_server

check_remote "http connector validates" "
  loom project validate '$PROJECT_HTTP' > '$TMP_DIR/validate-http.out'
  grep -q 'Project contract: ok' '$TMP_DIR/validate-http.out'
  grep -q '$HTTP_CAPABILITY' '$TMP_DIR/validate-http.out'
"

check_remote "register and activate http connector" "
  loom project register '$PROJECT_HTTP' > '$TMP_DIR/register-http.out'
  loom project activate '$HTTP_SLUG' --facet connectors > '$TMP_DIR/activate-http.out'
  grep -q '$HTTP_CAPABILITY' '$TMP_DIR/activate-http.out'
"

check_json "http connector runtime binding is active" \
  "loom capability inspect '$HTTP_CAPABILITY' --json" \
  ".ok == true and .data.runtime_binding.runtime_kind == \"http\" and .data.runtime_binding.status == \"active\""

check_json "http runtime binding validates" \
  "loom capability runtime-binding validate '$HTTP_CAPABILITY' --json" \
  ".ok == true and .data.validation.valid == true and .data.binding.binding.runtime_kind == \"http\""

check_json "http runtime binding test executes" \
  "loom capability runtime-binding test '$HTTP_CAPABILITY' --input-file /dev/null --json" \
  ".ok == true and .data.status == \"completed\" and .data.result.runtime == \"http\""

HTTP_CALL_JSON="$TMP_DIR/http-call.json"
check_remote "http runtime capability call completes" "
  loom capability call '$HTTP_CAPABILITY' --input '{}' --wait --timeout-seconds 20 --json > '$HTTP_CALL_JSON'
  jq -e '.ok == true and
    .data.status == \"completed\" and
    .data.route.status == \"completed\" and
    .data.capability_call.status == \"completed\" and
    .data.result.runtime == \"http\" and
    .data.result_refs.runtime_kind == \"http\"' '$HTTP_CALL_JSON' >/dev/null
"

pass "slice 10 runtime executor smoke completed with $pass_count checks"

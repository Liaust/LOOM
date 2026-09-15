#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"

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

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loomd binary installed" 'command -v loomd && loomd version'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "loomd Unix socket exists" 'test -S /run/loom/loomd.sock'
check_remote "loomd Unix socket permissions are restricted" 'test "$(stat -c "%U:%G %a" /run/loom/loomd.sock)" = "loom:loom 660"'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "direct health route returns ok envelope" \
  'curl --fail --silent --show-error --unix-socket /run/loom/loomd.sock http://loom/v1/health' \
  '.ok == true'

check_json "direct health includes correlation id" \
  'curl --fail --silent --show-error --unix-socket /run/loom/loomd.sock http://loom/v1/health' \
  '.meta.correlation_id | startswith("corr_")'

check_json "direct health reports daemon ok" \
  'curl --fail --silent --show-error --unix-socket /run/loom/loomd.sock http://loom/v1/health' \
  '.data.service == "loomd" and .data.status == "ok"'

check_remote "loom health exits zero" 'loom health'
check_remote "loom health has human daemon line" 'loom health | grep -q "LOOM daemon: ok"'
check_remote "loom health has human database line" 'loom health | grep -q "Database: ok"'
check_remote "loom health has human migration line" 'loom health | grep -q "Migrations:"'

check_json "CLI health JSON returns ok envelope" \
  'loom health --json' \
  '.ok == true'

check_json "CLI health JSON reports daemon ok" \
  'loom health --json' \
  '.data.service == "loomd" and .data.status == "ok"'

check_json "CLI health JSON reports database ok" \
  'loom health --json' \
  '.data.checks.database.status == "ok" and .data.checks.database.database == "loom_main"'

check_json "CLI health JSON reports storage ok" \
  'loom health --json' \
  '.data.checks.storage.status == "ok" and .data.checks.storage.data_dir == "/var/lib/loom" and .data.checks.storage.object_store == "/var/lib/loom/object-store"'

check_json "CLI health JSON reports migrations current" \
  'loom health --json' \
  '.data.checks.migrations.status == "ok" and .data.checks.migrations.current_version >= 1 and .data.checks.migrations.latest_version >= 1'

check_json "CLI health JSON includes correlation id" \
  'loom health --json' \
  '.meta.correlation_id | startswith("corr_")'

check_json "CLI accepts caller correlation id" \
  'loom health --json --correlation-id corr_smoke_slice_01' \
  '.meta.correlation_id == "corr_smoke_slice_01"'

check_remote "loomd structured logs visible" 'journalctl -u loomd -n 80 --no-pager | grep -q "\"component\":\"httpapi\""'
check_remote "PostgreSQL remains local-only" "! ss -ltn | awk '{print \$4}' | grep -qE '^(0\\.0\\.0\\.0:5432|\\[::\\]:5432|\\*:5432)$'"

log "passed checks: $pass_count"

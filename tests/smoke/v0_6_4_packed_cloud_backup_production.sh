#!/usr/bin/env bash
set -euo pipefail

if [[ "${LOOM_ACCEPTANCE_CONFIRM_PRODUCTION:-}" != "1" ]]; then
  printf '[skip] set LOOM_ACCEPTANCE_CONFIRM_PRODUCTION=1 to run production packed cloud backup acceptance\n'
  exit 0
fi

LOOM_BIN="${LOOM_BIN:-loom}"
CLOUD_CONFIG="${LOOM_CLOUD_CONFIG_PATH:-/etc/loom/cloud/config.json}"
ACCEPTANCE_ROOT="${LOOM_ACCEPTANCE_ROOT:-/home/loomadmin/loom-box/Documents/.loom-acceptance}"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
FETCH_DIR="$ACCEPTANCE_ROOT/cloud-fetch-borg-$RUN_ID"

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

pass() {
  printf '[ok] %s\n' "$*"
}

require_command() {
  local name="$1"
  command -v "$name" >/dev/null 2>&1 || fail "required command is missing: $name"
}

run_json() {
  local label="$1"
  shift
  local output
  printf '[run] %s\n' "$label" >&2
  if ! output="$("$LOOM_BIN" --json "$@" 2>&1)"; then
    printf '%s\n' "$output" >&2
    fail "$label failed"
  fi
  printf '%s\n' "$output" | jq -e '.ok == true' >/dev/null || {
    printf '%s\n' "$output" >&2
    fail "$label returned non-ok JSON"
  }
  printf '%s\n' "$output"
}

require_command jq
require_command "$LOOM_BIN"
require_command borg

mkdir -p "$ACCEPTANCE_ROOT"

setup_status="$(run_json 'setup status' setup status)"
printf '%s\n' "$setup_status" | jq -e '.data.summary.status != "not_installed"' >/dev/null \
  || fail "setup status says LOOM is not installed"
pass "setup status reports installed node"

setup_doctor="$(run_json 'setup doctor' setup doctor)"
printf '%s\n' "$setup_doctor" | jq -e '(.data.findings // []) | all(.severity != "blocking")' >/dev/null \
  || fail "setup doctor reports blocking findings"
pass "setup doctor has no blocking findings"

cloud_status="$(run_json 'cloud status' cloud status --cloud-config "$CLOUD_CONFIG")"
printf '%s\n' "$cloud_status" | jq -e '.data.config.snapshot_backend == "borg"' >/dev/null \
  || fail "cloud snapshot backend is not borg"
pass "cloud status reports Borg backend"

cloud_doctor="$(run_json 'cloud doctor' cloud doctor --cloud-config "$CLOUD_CONFIG")"
printf '%s\n' "$cloud_doctor" | jq -e '(.data.findings // []) | all(.severity != "critical")' >/dev/null \
  || fail "cloud doctor reports critical findings"
pass "cloud doctor has no critical findings"

backend_doctor="$(run_json 'cloud snapshot backend doctor' cloud snapshot backend doctor --cloud-config "$CLOUD_CONFIG")"
printf '%s\n' "$backend_doctor" | jq -e '(.data.findings // []) | all(.severity != "critical")' >/dev/null \
  || fail "snapshot backend doctor reports critical findings"
pass "snapshot backend doctor has no critical findings"

backend_status="$(run_json 'cloud snapshot backend status' cloud snapshot backend status --cloud-config "$CLOUD_CONFIG")"
printf '%s\n' "$backend_status" | jq -e '.data.backend == "borg" and .data.initialized == true' >/dev/null \
  || fail "Borg backend is not initialized"
pass "Borg backend is initialized"

if command -v systemctl >/dev/null 2>&1; then
  systemctl is-active --quiet loomd || fail "loomd is not active"
  pass "loomd is active"
fi

backup_dry_run="$(run_json 'backup create dry-run' backup create --production --dry-run)"
printf '%s\n' "$backup_dry_run" | jq -e '.data.status == "planned"' >/dev/null \
  || fail "backup dry-run did not plan"
pass "backup dry-run plans production capture"

backup_create="$(run_json 'backup create production' backup create --production --reason "v0.6.4 production packed cloud backup smoke")"
printf '%s\n' "$backup_create" | jq -e '.data.run.worker_run_id != ""' >/dev/null \
  || fail "backup create did not return a worker run"
pass "production backup created"

snapshot_push="$(run_json 'cloud snapshot push latest' cloud snapshot push --cloud-config "$CLOUD_CONFIG" --backup latest)"
printf '%s\n' "$snapshot_push" | jq -e '.data.status == "succeeded" and .data.backend == "borg"' >/dev/null \
  || fail "cloud snapshot push latest did not succeed through Borg"
pass "latest backup pushed to Borg cloud snapshot backend"

snapshot_list="$(run_json 'cloud snapshot list' cloud snapshot list --cloud-config "$CLOUD_CONFIG")"
printf '%s\n' "$snapshot_list" | jq -e '.data.status == "ok" and .data.latest.backend == "borg"' >/dev/null \
  || fail "cloud snapshot list did not show Borg latest snapshot"
pass "cloud snapshot list shows Borg latest snapshot"

snapshot_verify="$(run_json 'cloud snapshot verify latest' cloud snapshot verify --cloud-config "$CLOUD_CONFIG" latest)"
printf '%s\n' "$snapshot_verify" | jq -e '.data.status == "succeeded" and .data.backend == "borg"' >/dev/null \
  || fail "cloud snapshot verify latest failed"
pass "cloud snapshot verify latest succeeded"

snapshot_fetch="$(run_json 'cloud snapshot fetch latest' cloud snapshot fetch --cloud-config "$CLOUD_CONFIG" latest --to "$FETCH_DIR")"
printf '%s\n' "$snapshot_fetch" | jq -e '.data.status == "succeeded" and .data.verification.status == "succeeded"' >/dev/null \
  || fail "cloud snapshot fetch latest did not extract and verify"
pass "cloud snapshot fetch latest extracted into .loom-acceptance"

restore_drill="$(run_json 'cloud snapshot restore-drill latest dry-run' cloud snapshot restore-drill --cloud-config "$CLOUD_CONFIG" latest --dry-run)"
printf '%s\n' "$restore_drill" | jq -e '.data.status == "planned" and .data.backend == "borg"' >/dev/null \
  || fail "cloud snapshot restore-drill dry-run did not plan"
pass "cloud restore drill dry-run plans from Borg snapshot"

retention_plan="$(run_json 'cloud snapshot retention plan' cloud snapshot retention plan --cloud-config "$CLOUD_CONFIG" --keep-latest "${LOOM_CLOUD_KEEP_LATEST:-7}")"
printf '%s\n' "$retention_plan" | jq -e '.data.backend == "borg" and .data.status == "planned"' >/dev/null \
  || fail "cloud snapshot retention plan did not report Borg plan"
pass "cloud retention plan reports Borg decisions without applying deletion"

printf '[ok] v0.6.4 packed cloud backup production smoke passed\n'

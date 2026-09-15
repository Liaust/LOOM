#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

source "$REPO_ROOT/tests/lib/production_guard.sh"

log() {
  printf '[smoke] %s\n' "$*"
}

fail() {
  printf 'v0.5.1 production guardrails smoke: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

require_command jq

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-5-1-guardrails.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

cat >"$TMP_DIR/production-health.json" <<'JSON'
{
  "ok": true,
  "data": {
    "environment": "production",
    "node": {"id": "main", "role": "main"},
    "status": "ok"
  }
}
JSON

cat >"$TMP_DIR/dev-health.json" <<'JSON'
{
  "ok": true,
  "data": {
    "environment": "dev",
    "node": {"id": "dev-main", "role": "main"},
    "status": "ok"
  }
}
JSON

log "production health payload is blocked"
if loom_guard_refuse_production_health_file "$TMP_DIR/production-health.json" "dev smoke"; then
  fail "production guard allowed production health payload"
fi

log "dev health payload is allowed"
loom_guard_refuse_production_health_file "$TMP_DIR/dev-health.json" "dev smoke"

log "main wrapper exposes safe-check and disables generic smoke alias"
help_output="$("$REPO_ROOT/scripts/loom-main" help)"
grep -q "safe-check" <<<"$help_output" || fail "scripts/loom-main help is missing safe-check"
grep -q "v0-5-1-safe-check" <<<"$help_output" || fail "scripts/loom-main help is missing v0-5-1-safe-check"
grep -q "staging-only" <<<"$help_output" || fail "scripts/loom-main help does not mark hardware smoke as staging-only"
grep -q -- '--storage-root "$storage_root"' "$REPO_ROOT/scripts/loom-main" || fail "safe-check does not pass the configured canonical storage root"
grep -q -- '--box-state-root "$box_state_root"' "$REPO_ROOT/scripts/loom-main" || fail "safe-check does not pass the configured external Box state root"
if grep -q -- '--storage-export-root' "$REPO_ROOT/scripts/loom-main"; then
  fail "safe-check still passes the retired storage export root"
fi

set +e
LOOM_MAIN_HOST=example.invalid "$REPO_ROOT/scripts/loom-main" smoke >"$TMP_DIR/smoke.out" 2>"$TMP_DIR/smoke.err"
smoke_status=$?
set -e
[[ "$smoke_status" -ne 0 ]] || fail "generic smoke alias should be disabled"
grep -q "generic smoke alias is disabled" "$TMP_DIR/smoke.err" || fail "generic smoke alias error was not clear"

set +e
LOOM_MAIN_HOST=example.invalid "$REPO_ROOT/scripts/loom-main" slice-18-smoke >"$TMP_DIR/slice18.out" 2>"$TMP_DIR/slice18.err"
slice18_status=$?
set -e
[[ "$slice18_status" -ne 0 ]] || fail "slice-18 smoke should require explicit staging opt-in"
grep -q "LOOM_ALLOW_STAGING_SMOKE=1" "$TMP_DIR/slice18.err" || fail "slice-18 opt-in error was not clear"

log "v0.5.1 production guardrails smoke passed"

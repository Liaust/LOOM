#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HELPER="${LOOM_MACBOOK_HELPER:-$ROOT_DIR/scripts/loom-macbook}"
pass_count=0

fail() {
  printf 'v0.6.3 slice 01 auth credentials: %s\n' "$*" >&2
  exit 1
}

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$*"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

require_command bash

bash -n "$HELPER"
pass "MacBook helper parses"

help="$("$HELPER" --help)"
printf '%s\n' "$help" | grep -q 'auth-status' || fail "auth-status is not advertised"
printf '%s\n' "$help" | grep -q 'LOOM_CLOUD_STORAGE_HOST' || fail "cloud storage env vars are not documented"
printf '%s\n' "$help" | grep -q 'LOOM_CLOUD_STORAGE_KEY' || fail "cloud storage key env var is not documented"
pass "auth-status and cloud credential settings are documented in helper help"

if [[ "${LOOM_RUN_PRODUCTION_V0_6_3_AUTH_SMOKE:-}" != "1" ]]; then
  pass "production auth checks skipped; set LOOM_RUN_PRODUCTION_V0_6_3_AUTH_SMOKE=1 to run them"
  printf '[smoke] completed %d checks\n' "$pass_count"
  exit 0
fi

[[ "$(uname -s)" == "Darwin" ]] || fail "production MacBook auth smoke must run on macOS"
require_command ssh
require_command ssh-add
require_command sftp

"$HELPER" auth-status
pass "MacBook auth-status passed"

ssh -o BatchMode=yes loom-main true
pass "BatchMode SSH to loom-main works"

sftp -P "${LOOM_CLOUD_STORAGE_PORT:-23}" \
  -o BatchMode=yes \
  -i "${LOOM_CLOUD_STORAGE_KEY:-$HOME/.ssh/loom_hetzner_storage_box_mac_ed25519}" \
  -b /dev/null \
  "${LOOM_CLOUD_STORAGE_USER:-u613836}@${LOOM_CLOUD_STORAGE_HOST:-u613836.your-storagebox.de}" >/dev/null
pass "BatchMode SFTP to Hetzner Storage Box works"

printf '[smoke] completed %d checks\n' "$pass_count"

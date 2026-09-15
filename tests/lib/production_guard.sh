#!/usr/bin/env bash

loom_guard_fail() {
  printf 'production-guard: %s\n' "$*" >&2
  return 1
}

loom_guard_require_jq() {
  command -v jq >/dev/null 2>&1 || loom_guard_fail "jq is required"
}

loom_guard_environment_from_health_file() {
  local health_file="${1:-}"
  [[ -n "$health_file" ]] || loom_guard_fail "health file is required"
  [[ -f "$health_file" ]] || loom_guard_fail "health file does not exist: $health_file"
  loom_guard_require_jq || return 1
  jq -r '.data.environment // "unknown"' "$health_file"
}

loom_guard_refuse_production_health_file() {
  local health_file="${1:-}"
  local operation="${2:-smoke test}"
  local environment
  environment="$(loom_guard_environment_from_health_file "$health_file")" || return 1
  if [[ "$environment" == "production" ]]; then
    loom_guard_fail "refusing to run ${operation} against production"
  fi
}

loom_guard_refuse_production_remote() {
  local host="${1:-}"
  local operation="${2:-smoke test}"
  [[ -n "$host" ]] || loom_guard_fail "host is required"
  local health_file
  health_file="$(mktemp "${TMPDIR:-/tmp}/loom-health.XXXXXX")" || return 1
  if ! ssh -o BatchMode=yes "$host" 'loom health --json' >"$health_file"; then
    rm -f "$health_file"
    loom_guard_fail "could not read LOOM health from $host"
  fi
  loom_guard_refuse_production_health_file "$health_file" "$operation"
  local status=$?
  rm -f "$health_file"
  return "$status"
}


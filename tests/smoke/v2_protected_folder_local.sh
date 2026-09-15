#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d "/tmp/loom-pf.XXXXXX")"
DB_CONTAINER=""
SMOKE_DB_URL="${LOOM_TEST_DB_URL:-}"
pass_count=0

cleanup() {
  local exit_status=$?
  trap - EXIT INT TERM HUP
  set +e
  if [[ -n "$DB_CONTAINER" ]]; then
    docker stop "$DB_CONTAINER" >/dev/null 2>&1 || true
  fi
  case "$TMP_DIR" in
    /tmp/loom-pf.*)
      find "$TMP_DIR" -xdev -depth -delete >/dev/null 2>&1 || true
      ;;
  esac
  exit "$exit_status"
}
trap cleanup EXIT INT TERM HUP

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

run_go_test() {
  (cd "$ROOT" && TMPDIR="$TMP_DIR" LOOM_TEST_DB_URL="$SMOKE_DB_URL" "$@")
}

start_database() {
  local port_output port
  if [[ -n "$SMOKE_DB_URL" ]]; then
    return
  fi
  command -v docker >/dev/null 2>&1 || fail 'set LOOM_TEST_DB_URL or install Docker for the isolated PostgreSQL fixture'
  docker info >/dev/null 2>&1 || fail 'set LOOM_TEST_DB_URL or start Docker for the isolated PostgreSQL fixture'
  DB_CONTAINER="loom-v2-protected-folder-${PPID}-$$"
  docker run --rm -d \
    --name "$DB_CONTAINER" \
    -e POSTGRES_PASSWORD=loom-smoke \
    -e POSTGRES_DB=loomtest \
    -p 127.0.0.1::5432 \
    pgvector/pgvector:pg16 >/dev/null
  for _ in $(seq 1 120); do
    if docker exec "$DB_CONTAINER" pg_isready -U postgres -d loomtest >/dev/null 2>&1; then
      break
    fi
    sleep 0.5
  done
  if ! docker exec "$DB_CONTAINER" pg_isready -U postgres -d loomtest >/dev/null 2>&1; then
    docker logs --tail 40 "$DB_CONTAINER" >&2 || true
    fail 'isolated PostgreSQL did not become ready'
  fi
  port_output="$(docker port "$DB_CONTAINER" 5432/tcp | sed -n '1p')"
  port="${port_output##*:}"
  [[ "$port" =~ ^[0-9]+$ ]] || fail 'could not resolve the isolated PostgreSQL port'
  SMOKE_DB_URL="postgres://postgres:loom-smoke@127.0.0.1:${port}/loomtest?sslmode=disable"
}

start_database

log 'applying migrations to the isolated PostgreSQL fixture'
(cd "$ROOT" && TMPDIR="$TMP_DIR" LOOM_BOOTSTRAP_TEST_DB_URL="$SMOKE_DB_URL" go test ./internal/bootstrap -run '^TestEnsureProductionBootstrapIntegration$' -count=1)
pass 'fresh migrations apply through 00057 without touching production'

log 'checking durable preflight, reconciliation, lifecycle, and owner routing'
run_go_test go test ./internal/backupcontracts -run 'PreflightService|ReconcileService|ProjectProtectedFolder|ProjectRecord' -count=1
run_go_test go test ./internal/box -run 'ApplyWatchPolicyRoutesMixedOwners|ApplyWatchPolicyRejectsUnknownOwner|CorrelateWatchRootReports' -count=1
pass 'desired revisions, owner routing, stale evidence, and accepted-backup projection are safe'

log 'checking durable messages and exactly-once node handling'
run_go_test go test ./internal/communication -count=1
run_go_test go test ./internal/nodeagent -run 'ProtectedFolder|PollExecutesProtectedFolder|PollReconcilesProtectedFolder' -count=1
pass 'offline/reconnect, duplicate, stale, rollback, delete, and authenticated acknowledgement paths pass'

log 'checking HTTP, typed client, CLI, and Portal surfaces'
run_go_test go test ./internal/httpapi -run 'BackupContract' -count=1
run_go_test go test ./internal/localclient -run 'BackupContract' -count=1
run_go_test go test ./internal/loomcli -run 'BackupContract|Portal' -count=1
run_go_test go test ./internal/loomcli/portal -run 'ProtectedFolder|ProtectFolder' -count=1
pass 'API and human-facing workflows preserve queued/applied and offline truth'

printf '[smoke] v2 protected-folder local smoke passed (%d groups)\n' "$pass_count"

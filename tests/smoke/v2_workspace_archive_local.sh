#!/usr/bin/env bash
set -euo pipefail

# Match Main's UTC process timezone; Local UTC and JSON's UTC time locations
# must not cause structurally false manifest conflicts after PostgreSQL reads.
export TZ=UTC

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_BASE="${TMPDIR:-/tmp}"
TMP_BASE="${TMP_BASE%/}"
TMP_ROOT="$(mktemp -d "$TMP_BASE/loom-workspace-archive.XXXXXX")"
PG_SOCKET="$(mktemp -d /tmp/loom-workspace-archive-pg.XXXXXX)"
SOCKET_ROOT="$(mktemp -d /tmp/loom-workspace-archive-daemon.XXXXXX)"
PG_DATA="$TMP_ROOT/postgres"
RUNTIME_ROOT="$TMP_ROOT/runtime"
BOX_ROOT="$RUNTIME_ROOT/box"
STORAGE_ROOT="$RUNTIME_ROOT/storage"
CREDENTIALS_ROOT="$RUNTIME_ROOT/credentials"
SOCKET_PATH="$SOCKET_ROOT/loomd.sock"
LOOM_BIN="$TMP_ROOT/bin/loom"
LOOMD_BIN="$TMP_ROOT/bin/loomd"
ACCEPTANCE_DB="loom_workspace_archive_acceptance_$$"
RUNTIME_DB="loom_workspace_archive_runtime_$$"
PROVENANCE_DB="loom_provenance"
ACCEPTANCE_DB_URL="postgres://postgres@/$ACCEPTANCE_DB?host=$PG_SOCKET&sslmode=disable"
RUNTIME_DB_URL="postgres://postgres@/$RUNTIME_DB?host=$PG_SOCKET&sslmode=disable"
PROVENANCE_DB_URL="postgres://loom_provenance@/$PROVENANCE_DB?host=$PG_SOCKET&sslmode=disable"
DAEMON_PID=""
POSTGRES_STARTED=false
PASS_COUNT=0

log() { printf '[workspace-archive] %s\n' "$*"; }
pass() { PASS_COUNT=$((PASS_COUNT + 1)); printf '[ok] %s\n' "$*"; }
fail() { printf '[fail] %s\n' "$*" >&2; exit 1; }

cleanup() {
  local exit_status=$?
  trap - EXIT INT TERM HUP
  set +e
  stop_daemon
  if [[ "$POSTGRES_STARTED" == true ]]; then
    "$PG_BIN/pg_ctl" -D "$PG_DATA" -m fast -w stop >/dev/null 2>&1 || true
  fi
  case "$TMP_ROOT" in
    */loom-workspace-archive.*) find "$TMP_ROOT" -xdev -depth -delete >/dev/null 2>&1 || true ;;
  esac
  case "$PG_SOCKET" in
    /tmp/loom-workspace-archive-pg.*) find "$PG_SOCKET" -xdev -depth -delete >/dev/null 2>&1 || true ;;
  esac
  case "$SOCKET_ROOT" in
    /tmp/loom-workspace-archive-daemon.*) find "$SOCKET_ROOT" -xdev -depth -delete >/dev/null 2>&1 || true ;;
  esac
  exit "$exit_status"
}
trap cleanup EXIT INT TERM HUP

require_command() { command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"; }

resolve_nix() {
  if command -v nix >/dev/null 2>&1; then command -v nix; return; fi
  if [[ -x /nix/var/nix/profiles/default/bin/nix ]]; then printf '%s\n' /nix/var/nix/profiles/default/bin/nix; return; fi
  fail 'Nix is required to obtain the pinned disposable PostgreSQL 17 toolchain'
}

resolve_postgres() {
  local nix_command postgres_root
  nix_command="$(resolve_nix)"
  postgres_root="$(
    cd "$ROOT"
    "$nix_command" --extra-experimental-features 'nix-command flakes' \
      build --no-link --print-out-paths --impure --expr \
      'let flake = (import ./tests/nix/source-flake.nix {}); in flake.inputs.nixpkgs.legacyPackages.${builtins.currentSystem}.postgresql_17.withPackages (p: [ p.pgvector ])' \
      | tail -n 1
  )"
  [[ -x "$postgres_root/bin/initdb" ]] || fail 'Nix did not return a PostgreSQL 17 server toolchain'
  PG_BIN="$postgres_root/bin"
  export PATH="$PG_BIN:$PATH"
}

start_postgres() {
  "$PG_BIN/initdb" -D "$PG_DATA" --username=postgres --auth-local=trust --auth-host=reject --encoding=UTF8 --no-locale >/dev/null
  printf "\nlisten_addresses = ''\nunix_socket_directories = '%s'\nunix_socket_permissions = 0700\n" "$PG_SOCKET" >>"$PG_DATA/postgresql.conf"
  if ! "$PG_BIN/pg_ctl" -D "$PG_DATA" -l "$TMP_ROOT/postgres.log" -w start >/dev/null; then
    sed -n '1,200p' "$TMP_ROOT/postgres.log" >&2
    fail 'smoke-owned PostgreSQL 17 did not start'
  fi
  POSTGRES_STARTED=true
  "$PG_BIN/createdb" -h "$PG_SOCKET" -U postgres "$ACCEPTANCE_DB"
  "$PG_BIN/createdb" -h "$PG_SOCKET" -U postgres "$RUNTIME_DB"
  "$PG_BIN/psql" -X -v ON_ERROR_STOP=1 -h "$PG_SOCKET" -U postgres -d postgres \
    -c 'CREATE ROLE loom_provenance LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;' >/dev/null
  "$PG_BIN/createdb" -h "$PG_SOCKET" -U postgres -O loom_provenance "$PROVENANCE_DB"
}

stop_daemon() {
  if [[ -n "$DAEMON_PID" ]] && kill -0 "$DAEMON_PID" >/dev/null 2>&1; then
    kill -TERM "$DAEMON_PID" >/dev/null 2>&1 || true
    wait "$DAEMON_PID" >/dev/null 2>&1 || true
  fi
  DAEMON_PID=""
  if [[ -S "$SOCKET_PATH" ]]; then rm -f "$SOCKET_PATH"; fi
}

start_daemon() {
  local enabled="$1" log_name="$2" remaining=300
  env \
    HOME="$RUNTIME_ROOT/home" \
    XDG_CONFIG_HOME="$RUNTIME_ROOT/config" \
    CREDENTIALS_DIRECTORY="$CREDENTIALS_ROOT" \
    LOOM_ENV=test \
    LOOM_NODE_ID=main \
    LOOM_NODE_KIND=main \
    LOOM_NODE_ROLE=main \
    LOOM_RUNTIME_CLASS=main_full \
    LOOM_DATA_DIR="$RUNTIME_ROOT/data" \
    LOOM_OBJECT_STORE="$RUNTIME_ROOT/data/object-store" \
    LOOM_SERVICE_ROOT="$RUNTIME_ROOT/service" \
    LOOM_STORAGE_ROOT="$STORAGE_ROOT" \
    LOOM_CANONICAL_USER_BACKUPS_ROOT="$STORAGE_ROOT/backups" \
    LOOM_IMPORTS_ROOT="$STORAGE_ROOT/imports" \
    LOOM_USER_BACKUPS_ROOT="$STORAGE_ROOT/backups" \
    LOOM_ARCHIVE_ROOT="$STORAGE_ROOT/archive" \
    LOOM_GENERATED_ROOT="$RUNTIME_ROOT/data/generated" \
    LOOM_BOX_STATE_ROOT="$RUNTIME_ROOT/data/box-state" \
    LOOM_STORAGE_RETENTION_ROOT="$RUNTIME_ROOT/data/storage-retention" \
    LOOM_BOX_PATH="$BOX_ROOT" \
    LOOM_SOCKET_PATH="$SOCKET_PATH" \
    LOOM_DB_URL="$RUNTIME_DB_URL" \
    LOOM_PROVENANCE_DB_URL="$PROVENANCE_DB_URL" \
    LOOM_MIGRATIONS_DIR="$ROOT/migrations" \
    LOOM_AUTO_MIGRATE=true \
    LOOM_BOOTSTRAP_MODE=dev \
    LOOM_WORKSPACE_ARCHIVE_ENABLED="$enabled" \
    LOOM_WORKSPACE_ARCHIVE_MANIFEST_KEY_ID=workspace-archive-smoke-v1 \
    "$LOOMD_BIN" serve >"$TMP_ROOT/$log_name" 2>&1 &
  DAEMON_PID=$!
  while [[ "$remaining" -gt 0 ]]; do
    [[ -S "$SOCKET_PATH" ]] && return
    if ! kill -0 "$DAEMON_PID" >/dev/null 2>&1; then
      sed -n '1,240p' "$TMP_ROOT/$log_name" >&2
      fail 'disposable loomd exited before creating its Unix socket'
    fi
    sleep 0.1
    remaining=$((remaining - 1))
  done
  sed -n '1,240p' "$TMP_ROOT/$log_name" >&2
  fail 'disposable loomd did not create its Unix socket within thirty seconds'
}

loom() {
  "$LOOM_BIN" --socket "$SOCKET_PATH" --json "$@"
}

require_command go
require_command curl
require_command jq
require_command openssl
resolve_postgres

mkdir -p "$TMP_ROOT/bin" "$RUNTIME_ROOT/home" "$RUNTIME_ROOT/config" \
  "$RUNTIME_ROOT/data/object-store" "$RUNTIME_ROOT/data/storage-retention" \
  "$BOX_ROOT/Topics" "$BOX_ROOT/Projects" "$BOX_ROOT/Library" "$BOX_ROOT/Documents" "$BOX_ROOT/Notes" \
  "$STORAGE_ROOT/archive/topics" "$STORAGE_ROOT/archive/projects" "$STORAGE_ROOT/archive/library" \
  "$STORAGE_ROOT/backups" "$STORAGE_ROOT/imports" "$CREDENTIALS_ROOT"
CREDENTIALS_ROOT="$(cd "$CREDENTIALS_ROOT" && pwd -P)"
openssl rand -hex 32 | tr -d '\n' >"$CREDENTIALS_ROOT/workspace-archive-manifest-key"
chmod 0400 "$CREDENTIALS_ROOT/workspace-archive-manifest-key"

log 'starting smoke-owned PostgreSQL 17 and running the independent crash matrix'
start_postgres
(cd "$ROOT" && LOOM_WORKSPACE_ARCHIVE_TEST_DB_URL="$ACCEPTANCE_DB_URL" go test -count=1 ./internal/storagearchive -run '^TestWorkspacePostgresCrashAcceptance$')
pass 'real PostgreSQL journal/catalog/event and crash/restart matrix passed'

log 'building disposable loom and loomd binaries'
(cd "$ROOT" && go build -o "$LOOM_BIN" ./cmd/loom && go build -o "$LOOMD_BIN" ./cmd/loomd)

log 'proving disabled-by-default runtime returns not-ready before database work'
start_daemon false loomd-disabled.log
if ! disabled_response="$(curl --silent --show-error --unix-socket "$SOCKET_PATH" -H 'content-type: application/json' -d '{"kind":"topic","object_id":"topic_runtime","slug":"runtime-topic","reason":"disabled proof"}' -w '\n%{http_code}' http://loom/v1/storage/workspace-archive/plan)"; then
  sed -n '1,240p' "$TMP_ROOT/loomd-disabled.log" >&2
  fail 'disabled workspace archive request did not receive an HTTP response'
fi
[[ "$(printf '%s\n' "$disabled_response" | tail -n 1)" == 503 ]] || fail 'disabled workspace archive route did not return HTTP 503'
printf '%s\n' "$disabled_response" | sed '$d' | jq -e '.error.code == "workspace_archive.not_ready"' >/dev/null || fail 'disabled workspace archive route did not return workspace_archive.not_ready'
operation_count="$($PG_BIN/psql -X -At -h "$PG_SOCKET" -U postgres -d "$RUNTIME_DB" -c 'SELECT count(*) FROM storage.workspace_archive_operations')"
[[ "$operation_count" == 0 ]] || fail 'disabled workspace archive route mutated its PostgreSQL journal'
stop_daemon
pass 'disabled runtime is fail-closed with zero journal mutation'

mkdir -p "$BOX_ROOT/Topics/runtime-topic"
printf 'runtime payload\n' >"$BOX_ROOT/Topics/runtime-topic/payload.txt"

log 'driving enabled archive and restore through CLI, local client, HTTP, and a daemon restart'
start_daemon true loomd-enabled-first.log
loom storage workspace plan topic topic_runtime runtime-topic --reason 'disposable runtime acceptance' >"$TMP_ROOT/archive-plan.json"
archive_operation="$(jq -r '.operation_id' "$TMP_ROOT/archive-plan.json")"
archive_digest="$(jq -r '.plan_digest' "$TMP_ROOT/archive-plan.json")"
loom storage workspace apply "$TMP_ROOT/archive-plan.json" --plan-digest "$archive_digest" --yes >"$TMP_ROOT/archive-result.json"
jq -e '.phase == "archive_complete" and .custody == "archived"' "$TMP_ROOT/archive-result.json" >/dev/null || fail 'archive CLI did not report complete archived custody'
[[ ! -e "$BOX_ROOT/Topics/runtime-topic" && -f "$STORAGE_ROOT/archive/topics/runtime-topic/content/payload.txt" ]] || fail 'archive runtime did not leave exactly one archived payload'
stop_daemon

start_daemon true loomd-enabled-restart.log
loom storage workspace inspect "$archive_operation" >"$TMP_ROOT/restart-inspect.json"
jq -e '.phase == "archive_complete" and .custody == "archived"' "$TMP_ROOT/restart-inspect.json" >/dev/null || fail 'restarted daemon did not inspect completed archive'
loom storage workspace restore-plan "$archive_operation" --reason 'disposable runtime restore' >"$TMP_ROOT/restore-plan.json"
restore_digest="$(jq -r '.plan_digest' "$TMP_ROOT/restore-plan.json")"
loom storage workspace restore-apply "$TMP_ROOT/restore-plan.json" --plan-digest "$restore_digest" --yes >"$TMP_ROOT/restore-result.json"
jq -e '.phase == "restore_complete" and .custody == "active" and .activation_state == "inactive"' "$TMP_ROOT/restore-result.json" >/dev/null || fail 'restore CLI did not report complete inactive active custody'
[[ -f "$BOX_ROOT/Topics/runtime-topic/payload.txt" && ! -e "$STORAGE_ROOT/archive/topics/runtime-topic/content" ]] || fail 'restore runtime did not leave exactly one active payload'
pass 'supported surfaces and daemon restart preserve exact one-payload custody'

for evidence in "$TMP_ROOT"/*.json "$TMP_ROOT"/loomd-*.log "$STORAGE_ROOT/archive"; do
  if grep -R -F -f "$CREDENTIALS_ROOT/workspace-archive-manifest-key" "$evidence" >/dev/null 2>&1; then
    fail "manifest key bytes leaked into disposable evidence: $evidence"
  fi
done
if ps -p "$DAEMON_PID" -o command= | grep -F -f "$CREDENTIALS_ROOT/workspace-archive-manifest-key" >/dev/null 2>&1; then
  fail 'manifest key bytes leaked into loomd process arguments'
fi
pass 'manifest key bytes are absent from logs, outputs, manifests, and process arguments'

printf '[workspace-archive] PASS (%d checks)\n' "$PASS_COUNT"

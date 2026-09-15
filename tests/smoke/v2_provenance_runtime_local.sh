#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_BASE="${TMPDIR:-/tmp}"
TMP_BASE="${TMP_BASE%/}"
TMP_ROOT="$(mktemp -d "$TMP_BASE/loom-provenance-runtime.XXXXXX")"
PG_DATA="$TMP_ROOT/postgres"
PG_SOCKET="$(mktemp -d "/tmp/loom-prov-pg.XXXXXX")"
RUNTIME_ROOT="$TMP_ROOT/runtime"
LOOMD_BIN="$TMP_ROOT/bin/loomd"
SOCKET_PATH="$RUNTIME_ROOT/run/loomd.sock"
DAEMON_PID=""
POSTGRES_STARTED=false
PASS_COUNT=0

log() {
  printf '[provenance-runtime] %s\n' "$*"
}

pass() {
  PASS_COUNT=$((PASS_COUNT + 1))
  printf '[ok] %s\n' "$*"
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

stop_daemon() {
  local status=0
  local remaining=100
  if [[ -z "$DAEMON_PID" ]]; then
    return
  fi
  if kill -0 "$DAEMON_PID" >/dev/null 2>&1; then
    kill -TERM "$DAEMON_PID"
    while kill -0 "$DAEMON_PID" >/dev/null 2>&1 && [[ "$remaining" -gt 0 ]]; do
      sleep 0.1
      remaining=$((remaining - 1))
    done
    if kill -0 "$DAEMON_PID" >/dev/null 2>&1; then
      fail "loomd did not stop within ten seconds"
    fi
  fi
  wait "$DAEMON_PID" || status=$?
  DAEMON_PID=""
  [[ "$status" -eq 0 ]] || fail "loomd exited with status $status"
}

cleanup() {
  local exit_status=$?
  trap - EXIT INT TERM HUP
  set +e
  if [[ -n "$DAEMON_PID" ]] && kill -0 "$DAEMON_PID" >/dev/null 2>&1; then
    kill -TERM "$DAEMON_PID" >/dev/null 2>&1 || true
    wait "$DAEMON_PID" >/dev/null 2>&1 || true
  fi
  if [[ "$POSTGRES_STARTED" == true ]]; then
    "$PG_BIN/pg_ctl" -D "$PG_DATA" -m fast -w stop >/dev/null 2>&1 || true
  fi
  case "$TMP_ROOT" in
    */loom-provenance-runtime.*)
      find "$TMP_ROOT" -xdev -depth -delete >/dev/null 2>&1 || true
      ;;
  esac
  case "$PG_SOCKET" in
    /tmp/loom-prov-pg.*)
      find "$PG_SOCKET" -xdev -depth -delete >/dev/null 2>&1 || true
      ;;
  esac
  exit "$exit_status"
}
trap cleanup EXIT INT TERM HUP

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

resolve_nix() {
  if command -v nix >/dev/null 2>&1; then
    command -v nix
    return
  fi
  if [[ -x /nix/var/nix/profiles/default/bin/nix ]]; then
    printf '%s\n' /nix/var/nix/profiles/default/bin/nix
    return
  fi
  fail 'Nix is required to obtain the pinned disposable PostgreSQL 17 + pgvector toolchain'
}

resolve_postgres() {
  local nix_command
  local postgres_root
  nix_command="$(resolve_nix)"
  postgres_root="$(
    cd "$ROOT"
    "$nix_command" --extra-experimental-features 'nix-command flakes' \
      build --no-link --print-out-paths --impure --expr \
      'let flake = (import ./tests/nix/source-flake.nix {}); in flake.inputs.nixpkgs.legacyPackages.${builtins.currentSystem}.postgresql_17.withPackages (p: [ p.pgvector ])' \
      | tail -n 1
  )"
  [[ -x "$postgres_root/bin/initdb" ]] || fail 'Nix did not return a PostgreSQL server toolchain'
  [[ -x "$postgres_root/bin/pg_dump" ]] || fail 'Nix PostgreSQL toolchain is missing pg_dump'
  [[ -x "$postgres_root/bin/pg_restore" ]] || fail 'Nix PostgreSQL toolchain is missing pg_restore'
  PG_BIN="$postgres_root/bin"
  export PATH="$PG_BIN:$PATH"
}

start_postgres() {
  mkdir -p "$PG_SOCKET"
  "$PG_BIN/initdb" -D "$PG_DATA" --username=postgres --auth-local=trust \
    --auth-host=reject --encoding=UTF8 --no-locale >/dev/null
  printf "\nlisten_addresses = ''\nunix_socket_directories = '%s'\nunix_socket_permissions = 0700\n" \
    "$PG_SOCKET" >>"$PG_DATA/postgresql.conf"
  if ! "$PG_BIN/pg_ctl" -D "$PG_DATA" -l "$TMP_ROOT/postgres.log" -w start >/dev/null; then
    sed -n '1,240p' "$TMP_ROOT/postgres.log" >&2
    fail 'smoke-owned PostgreSQL did not start'
  fi
  POSTGRES_STARTED=true

  "$PG_BIN/createdb" -h "$PG_SOCKET" -U postgres loom_main
  "$PG_BIN/psql" -X -v ON_ERROR_STOP=1 -h "$PG_SOCKET" -U postgres -d postgres \
    -c 'CREATE ROLE loom_provenance LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;' >/dev/null
  "$PG_BIN/createdb" -h "$PG_SOCKET" -U postgres -O loom_provenance loom_provenance
  "$PG_BIN/psql" -X -v ON_ERROR_STOP=1 -h "$PG_SOCKET" -U postgres -d postgres \
    -c 'REVOKE CONNECT ON DATABASE loom_main FROM PUBLIC; GRANT CONNECT ON DATABASE loom_main TO postgres;' >/dev/null
}

start_daemon() {
  local auto_migrate="$1"
  local log_path="$2"
  local remaining=300

  env \
    LOOM_ENV=test \
    LOOM_NODE_ID=main \
    LOOM_NODE_KIND=main \
    LOOM_NODE_ROLE=main \
    LOOM_RUNTIME_CLASS=main_full \
    LOOM_DATA_DIR="$RUNTIME_ROOT/data" \
    LOOM_OBJECT_STORE="$RUNTIME_ROOT/data/object-store" \
    LOOM_SERVICE_ROOT="$RUNTIME_ROOT/service" \
    LOOM_STORAGE_ROOT="$RUNTIME_ROOT/storage" \
    LOOM_CANONICAL_USER_BACKUPS_ROOT="$RUNTIME_ROOT/storage/backups" \
    LOOM_IMPORTS_ROOT="$RUNTIME_ROOT/storage/imports" \
    LOOM_USER_BACKUPS_ROOT="$RUNTIME_ROOT/storage/backups" \
    LOOM_ARCHIVE_ROOT="$RUNTIME_ROOT/storage/archive" \
    LOOM_GENERATED_ROOT="$RUNTIME_ROOT/data/generated" \
    LOOM_BOX_STATE_ROOT="$RUNTIME_ROOT/data/box-state" \
    LOOM_STORAGE_RETENTION_ROOT="$RUNTIME_ROOT/data/storage-retention" \
    LOOM_BOX_PATH="$RUNTIME_ROOT/box" \
    LOOM_SOCKET_PATH="$SOCKET_PATH" \
    LOOM_DB_URL="$MAIN_DB_URL" \
    LOOM_PROVENANCE_DB_URL="$PROVENANCE_DB_URL" \
    LOOM_MIGRATIONS_DIR="$ROOT/migrations" \
    LOOM_AUTO_MIGRATE="$auto_migrate" \
    LOOM_BOOTSTRAP_MODE=dev \
    "$LOOMD_BIN" serve >"$log_path" 2>&1 &
  DAEMON_PID=$!

  while [[ "$remaining" -gt 0 ]]; do
    if [[ -S "$SOCKET_PATH" ]]; then
      return
    fi
    if ! kill -0 "$DAEMON_PID" >/dev/null 2>&1; then
      sed -n '1,240p' "$log_path" >&2
      fail 'loomd exited before creating its disposable Unix socket'
    fi
    sleep 0.1
    remaining=$((remaining - 1))
  done
  sed -n '1,240p' "$log_path" >&2
  fail 'loomd did not create its disposable Unix socket within thirty seconds'
}

request() {
  curl --fail --silent --show-error --unix-socket "$SOCKET_PATH" "$@"
}

request_to_file() {
  local output_path="$1"
  local status
  shift
  status="$(curl --silent --show-error --output "$output_path" --write-out '%{http_code}' \
    --unix-socket "$SOCKET_PATH" "$@")"
  if [[ "$status" != 200 ]]; then
    sed -n '1,240p' "$output_path" >&2
    fail "unexpected HTTP status $status for ${*: -1}"
  fi
}

assert_no_marker() {
  local path="$1"
  local marker="$2"
  if grep -Fq -- "$marker" "$path"; then
    fail "$path leaked protected marker: $marker"
  fi
}

require_command go
require_command curl
require_command jq
resolve_postgres

mkdir -p "$TMP_ROOT/bin" "$RUNTIME_ROOT/run" "$RUNTIME_ROOT/storage/imports" \
  "$RUNTIME_ROOT/storage/backups" "$RUNTIME_ROOT/storage/archive" \
  "$RUNTIME_ROOT/data/object-store" "$RUNTIME_ROOT/data/box-state" \
  "$RUNTIME_ROOT/data/generated" "$RUNTIME_ROOT/data/storage-retention" \
  "$RUNTIME_ROOT/box/Documents"

log 'starting a smoke-owned PostgreSQL 17 + pgvector cluster'
start_postgres
MAIN_DB_URL="postgresql://postgres@/loom_main?host=$PG_SOCKET"
PROVENANCE_DB_URL="postgresql://loom_provenance@/loom_provenance?host=$PG_SOCKET"
ADMIN_DB_URL="postgresql://postgres@/postgres?host=$PG_SOCKET"
export LOOM_PROVENANCE_TEST_DB_URL="$ADMIN_DB_URL"
pass 'disposable PostgreSQL cluster and isolated database role are ready'

log 'building the real loomd binary into the temporary root'
(cd "$ROOT" && go build -o "$LOOMD_BIN" ./cmd/loomd)
pass 'loomd built without installing or activating a service'

log 'booting loomd with both disposable databases and temporary runtime roots'
start_daemon true "$TMP_ROOT/loomd-first.log"
request_to_file "$TMP_ROOT/health-first.json" http://loom/v1/provenance/health
jq -e '
  .ok == true and
  .data.state == "ready" and
  .data.code == "ready" and
  .data.database == "loom_provenance" and
  .data.role == "loom_provenance" and
  .data.applied_head == .data.packaged_head
' "$TMP_ROOT/health-first.json" >/dev/null
pass 'loomd opened separate main and provenance pools at the exact schema head'

cat >"$TMP_ROOT/register.json" <<'JSON'
{
  "candidates": [
    {
      "schema_version": "1.0",
      "claim": "claim-body-MUST-NOT-LEAK",
      "record_kind": "decision",
      "record_context": "disposable integrated acceptance",
      "sources": [
        {
          "source_reference_id": "00000000-0000-4000-8000-000000006001",
          "kind": "codex_current_thread",
          "status": "unresolved",
          "verification_posture": "unverified",
          "resolver_name": "disposable-smoke",
          "resolver_version": "1.0",
          "gap_reason": "source-excerpt-MUST-NOT-LEAK",
          "submitted": {"credential": "credential-MUST-NOT-LEAK"}
        }
      ],
      "domain": "provenance-runtime-acceptance",
      "visibility": "private",
      "temporal_interpretation": {
        "interpretation": "Applies only to the disposable Slice 6 smoke."
      },
      "assertion_posture": "source_claim",
      "producer": {
        "producer_id": "semantic-source-actor-MUST-NOT-AUTHORIZE",
        "producer_kind": "working_agent",
        "task_id": "disposable-slice-6"
      }
    }
  ]
}
JSON

request_to_file "$TMP_ROOT/register-first.json" -H 'Content-Type: application/json' \
  -H 'X-Loom-Idempotency-Key: provenance-smoke-register-first' \
  --data-binary "@$TMP_ROOT/register.json" \
  http://loom/v1/provenance/candidates
CANDIDATE_ID="$(jq -er '.data.receipt.receipts[0].candidate_id' "$TMP_ROOT/register-first.json")"
jq -e '
  .ok == true and
  .data.receipt.replayed == false and
  .data.receipt.receipts[0].effective_state == "pending" and
  .data.receipt.receipts[0].immediately_readable == true and
  .data.execution_authority.capability == "main@provenance.candidate.register" and
  .data.execution_authority.actor_id != "semantic-source-actor-MUST-NOT-AUTHORIZE"
' "$TMP_ROOT/register-first.json" >/dev/null

request_to_file "$TMP_ROOT/register-replay.json" -H 'Content-Type: application/json' \
  -H 'X-Loom-Idempotency-Key: provenance-smoke-register-alias' \
  --data-binary "@$TMP_ROOT/register.json" \
  http://loom/v1/provenance/candidates
jq -e --arg candidate "$CANDIDATE_ID" '
  .ok == true and
  .data.receipt.replayed == true and
  .data.receipt.receipts[0].candidate_id == $candidate
' "$TMP_ROOT/register-replay.json" >/dev/null
pass 'live registration is replay-safe and semantic identity grants no execution authority'

jq -n --arg candidate "$CANDIDATE_ID" '{
  scope: {domain: "provenance-runtime-acceptance", visibility: "private"},
  producer: {
    producer_id: "manual-reviewer",
    producer_kind: "working_agent",
    task_id: "disposable-slice-6"
  },
  operations: [
    {
      operation_type: "reject_candidate",
      schema_version: "1.0",
      operation_id: "00000000-0000-4000-8000-000000006101",
      occurred_at: "2026-08-30T12:00:00Z",
      producer: {
        producer_id: "manual-reviewer",
        producer_kind: "working_agent",
        task_id: "disposable-slice-6"
      },
      candidate_id: $candidate,
      reason: "The disposable acceptance candidate is intentionally rejected."
    }
  ]
}' >"$TMP_ROOT/reject.json"

request_to_file "$TMP_ROOT/reject-first.json" -H 'Content-Type: application/json' \
  -H 'X-Loom-Idempotency-Key: provenance-smoke-lifecycle-first' \
  --data-binary "@$TMP_ROOT/reject.json" \
  http://loom/v1/provenance/operations
jq -e '
  .ok == true and
  .data.receipt.replayed == false and
  .data.receipt.receipts[0].effective_state == "rejected" and
  .data.execution_authority.capability == "main@provenance.lifecycle.apply"
' "$TMP_ROOT/reject-first.json" >/dev/null

request_to_file "$TMP_ROOT/reject-replay.json" -H 'Content-Type: application/json' \
  -H 'X-Loom-Idempotency-Key: provenance-smoke-lifecycle-alias' \
  --data-binary "@$TMP_ROOT/reject.json" \
  http://loom/v1/provenance/operations
jq -e '.ok == true and .data.receipt.replayed == true' "$TMP_ROOT/reject-replay.json" >/dev/null
pass 'live manual lifecycle mutation is append-oriented and replay-safe'

search_status="$(curl --silent --output "$TMP_ROOT/search.json" --write-out '%{http_code}' \
  --unix-socket "$SOCKET_PATH" -H 'Content-Type: application/json' \
  --data '{"query":"not implemented"}' http://loom/v1/provenance/search)"
[[ "$search_status" == 404 ]] || fail "unexpected provenance search status: $search_status"
pass 'no ranked-search route is exposed by the foundation'

stop_daemon
pass 'first loomd process closed both database pools cleanly'

log 'restarting loomd without migration writes and checking durable state'
start_daemon false "$TMP_ROOT/loomd-restart.log"
request_to_file "$TMP_ROOT/candidate-after-restart.json" \
  "http://loom/v1/provenance/candidates/$CANDIDATE_ID?limit=10"
jq -e --arg candidate "$CANDIDATE_ID" '
  .ok == true and
  .data.candidate.candidate_id == $candidate and
  .data.effective_state == "rejected" and
  .data.candidate.claim == "claim-body-MUST-NOT-LEAK"
' "$TMP_ROOT/candidate-after-restart.json" >/dev/null
request_to_file "$TMP_ROOT/health-after-restart.json" http://loom/v1/provenance/health
jq -e '.ok == true and .data.state == "ready" and .data.code == "ready"' \
  "$TMP_ROOT/health-after-restart.json" >/dev/null
pass 'restart preserves the ledger and checks readiness without remigrating'

for path in \
  "$TMP_ROOT/health-first.json" \
  "$TMP_ROOT/health-after-restart.json" \
  "$TMP_ROOT/loomd-first.log" \
  "$TMP_ROOT/loomd-restart.log"
do
  assert_no_marker "$path" 'claim-body-MUST-NOT-LEAK'
  assert_no_marker "$path" 'source-excerpt-MUST-NOT-LEAK'
  assert_no_marker "$path" 'credential-MUST-NOT-LEAK'
done
pass 'ordinary runtime health and logs contain no semantic or credential markers'

stop_daemon

main_has_provenance="$(
  "$PG_BIN/psql" -X -Aqt -h "$PG_SOCKET" -U postgres -d loom_main \
    -c "SELECT CASE WHEN to_regnamespace('provenance') IS NULL THEN 'no' ELSE 'yes' END"
)"
[[ "$main_has_provenance" == no ]] || fail 'loom_main contains a provenance schema'

forbidden_tables="$(
  "$PG_BIN/psql" -X -Aqt -h "$PG_SOCKET" -U postgres -d loom_provenance -c \
    "SELECT count(*) FROM information_schema.tables WHERE table_schema='provenance' AND table_name ~ '(project|repository|archivist|clarification|fts)'"
)"
[[ "$forbidden_tables" == 0 ]] || fail 'loom_provenance contains deferred project/search/Archivist tables'

set +e
"$PG_BIN/psql" -X -Aqt -h "$PG_SOCKET" -U loom_provenance -d loom_main \
  -c 'SELECT 1' >"$TMP_ROOT/cross-database.out" 2>"$TMP_ROOT/cross-database.err"
cross_database_status=$?
set -e
[[ "$cross_database_status" -ne 0 ]] || fail 'provenance role connected to loom_main'
pass 'database schemas and role access remain separated'

log 'running real dump/restore, health, authorization, and support-redaction contracts'
(cd "$ROOT" && go test -count=1 ./internal/backup \
  -run '^(TestProvenanceRestoreDrillComparesCompleteLogicalLedgerPostgres|TestValidateProvenanceRestoreDrillDatabaseEnforcesIsolation)$')
(cd "$ROOT" && go test -count=1 ./internal/provenance \
  -run '^(TestRecoverySnapshotIsDeterministicCompleteAndContentFreePostgres|TestBuildHealthReportUsesBoundedAggregateQueriesPostgres|TestHealthReportIsBoundedMetadataOnlyAndClassifiesBackupFreshness)$')
(cd "$ROOT" && go test -count=1 ./internal/httpapi \
  -run '^(TestPolicyProvenanceAuthorizerBindsDecisionToAuthenticatedContext|TestProvenanceMutationDeniesUnauthorizedActorNodeBeforeStoreAccess|TestProvenanceRegistrationSeparatesExecutionAuthorityAndSemanticProducer|TestProvenanceHealthIsMetadataOnlyAndSearchDoesNotExist)$')
(cd "$ROOT" && go test -count=1 ./internal/supportbundle \
  -run '^(TestProvenanceCollectorEmitsOnlyBoundedMetadata|TestDefaultCollectorsRegisterProvenanceAndSkipWithoutProvider)$')
(cd "$ROOT" && go test -count=1 ./internal/backupcoverage \
  -run '^TestCheckFailsClosedWhenLatestManifestOmitsOrCorruptsProvenanceRecovery$')
(cd "$ROOT" && go test -count=1 ./internal/loomdapp \
  -run '^TestCloseRuntimeDatabases')
pass 'backup, restore, health, authorization, redaction, and fail-closed transition contracts passed'

log "passed checks: $PASS_COUNT"

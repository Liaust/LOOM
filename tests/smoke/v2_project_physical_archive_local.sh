#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_ROOT="$(mktemp -d /tmp/loom-project-physical.XXXXXX)"
PG_DATA="$TMP_ROOT/pg"
PG_SOCKET="$TMP_ROOT"
SOCKET_PATH="$TMP_ROOT/loomd.sock"
RUNTIME_DB="loom_project_physical_runtime_$$"
MAIN_DB_URL="postgres://postgres@/$RUNTIME_DB?host=$PG_SOCKET&sslmode=disable"
PROVENANCE_DB_URL="postgres://loom_provenance@/loom_provenance?host=$PG_SOCKET&sslmode=disable"
BOX_ROOT="$TMP_ROOT/box"
STORAGE_ROOT="$TMP_ROOT/storage"
CREDENTIALS_ROOT="$TMP_ROOT/credentials"
LOOM_BIN="$TMP_ROOT/bin/loom"
LOOMD_BIN="$TMP_ROOT/bin/loomd"
DAEMON_PID=""
POSTGRES_STARTED=false
PASS_COUNT=0

log() { printf '[project-physical] %s\n' "$*"; }
pass() { PASS_COUNT=$((PASS_COUNT + 1)); printf '[ok] %s\n' "$*"; }
fail() { printf '[fail] %s\n' "$*" >&2; exit 1; }
stop_daemon() {
  if [[ -n "$DAEMON_PID" ]]; then
    if kill -0 "$DAEMON_PID" 2>/dev/null; then kill -TERM "$DAEMON_PID"; fi
    wait "$DAEMON_PID" || true
    DAEMON_PID=""
  fi
}
cleanup() {
  local result=$?
  trap - EXIT INT TERM HUP
  set +e
  stop_daemon
  if [[ "$POSTGRES_STARTED" == true ]]; then
    if ! "$PG_BIN/pg_ctl" -D "$PG_DATA" -m fast -w stop >/dev/null; then
      printf '[fail] PostgreSQL stop failed; preserving %s\n' "$TMP_ROOT" >&2
      exit 1
    fi
  fi
  if [[ "$result" -ne 0 ]]; then
    # Emit bounded diagnostics before deleting only this smoke-owned root.
    for evidence in "$TMP_ROOT"/*-result.json "$TMP_ROOT"/*-plan.json; do
      [[ -f "$evidence" ]] && jq -c '{ok, error, phase: .data.phase, digest: .plan_digest}' "$evidence" >&2
    done
    [[ -f "$TMP_ROOT/loomd.log" ]] && tail -n 30 "$TMP_ROOT/loomd.log" >&2
  fi
  case "$TMP_ROOT" in
    /tmp/loom-project-physical.*)
      # Generated views are intentionally read-only; only this stopped,
      # disposable tree is made owner-writable for fixture teardown.
      find -P "$TMP_ROOT" -xdev -type d -exec chmod u+w {} + || result=1
      find "$TMP_ROOT" -xdev -depth -delete || result=1
      [[ ! -e "$TMP_ROOT" ]] || result=1 ;;
    *) result=1 ;;
  esac
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

for command in go git curl jq openssl; do
  command -v "$command" >/dev/null || fail "required command unavailable: $command"
done
NIX="$(command -v nix || true)"
if [[ -z "$NIX" && -x /nix/var/nix/profiles/default/bin/nix ]]; then
  NIX=/nix/var/nix/profiles/default/bin/nix
fi
[[ -n "$NIX" ]] || fail 'Nix is required for the pinned disposable PostgreSQL toolchain'
PG_ROOT="$(cd "$ROOT" && "$NIX" --extra-experimental-features 'nix-command flakes' build --no-link --print-out-paths --impure --expr 'let f = (import ./tests/nix/source-flake.nix {}); in f.inputs.nixpkgs.legacyPackages.${builtins.currentSystem}.postgresql_17.withPackages (p: [ p.pgvector ])')"
PG_BIN="$PG_ROOT/bin"
[[ -x "$PG_BIN/initdb" ]] || fail 'pinned PostgreSQL 17 was not resolved'
export PATH="$PG_BIN:$PATH"
mkdir -p "$TMP_ROOT/bin" "$TMP_ROOT/home" "$TMP_ROOT/config" "$TMP_ROOT/data" \
  "$BOX_ROOT/Topics" "$BOX_ROOT/Projects" "$BOX_ROOT/Library" \
  "$STORAGE_ROOT/archive/projects" "$STORAGE_ROOT/archive/topics" "$STORAGE_ROOT/archive/library" \
  "$STORAGE_ROOT/imports" "$STORAGE_ROOT/backups" "$CREDENTIALS_ROOT"
# Canonicalize the private credential path on macOS (/tmp -> /private/tmp).
CREDENTIALS_ROOT="$(cd "$CREDENTIALS_ROOT" && pwd -P)"
openssl rand -hex 32 | tr -d '\n' >"$CREDENTIALS_ROOT/workspace-archive-manifest-key"
chmod 0400 "$CREDENTIALS_ROOT/workspace-archive-manifest-key"
"$PG_BIN/initdb" -D "$PG_DATA" --username=postgres --auth-local=trust --auth-host=reject --encoding=UTF8 --no-locale >/dev/null
"$PG_BIN/pg_ctl" -D "$PG_DATA" -l "$TMP_ROOT/postgres.log" -o "-c listen_addresses='' -c unix_socket_permissions=0700 -k $PG_SOCKET" -w start >/dev/null
POSTGRES_STARTED=true
"$PG_BIN/createdb" -h "$PG_SOCKET" -U postgres "$RUNTIME_DB"
"$PG_BIN/psql" -X -v ON_ERROR_STOP=1 -h "$PG_SOCKET" -U postgres -d postgres -c 'CREATE ROLE loom_provenance LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT' >/dev/null
"$PG_BIN/createdb" -h "$PG_SOCKET" -U postgres -O loom_provenance loom_provenance

RUNTIME_ENV=(
  "HOME=$TMP_ROOT/home" "XDG_CONFIG_HOME=$TMP_ROOT/config"
  "CREDENTIALS_DIRECTORY=$CREDENTIALS_ROOT" "LOOM_ENV=test"
  "LOOM_NODE_ID=main" "LOOM_NODE_KIND=main" "LOOM_NODE_ROLE=main" "LOOM_RUNTIME_CLASS=main_full"
  "LOOM_DATA_DIR=$TMP_ROOT/data" "LOOM_OBJECT_STORE=$TMP_ROOT/data/object-store"
  "LOOM_SERVICE_ROOT=$TMP_ROOT/service" "LOOM_STORAGE_ROOT=$STORAGE_ROOT"
  "LOOM_CANONICAL_USER_BACKUPS_ROOT=$STORAGE_ROOT/backups" "LOOM_USER_BACKUPS_ROOT=$STORAGE_ROOT/backups"
  "LOOM_IMPORTS_ROOT=$STORAGE_ROOT/imports" "LOOM_ARCHIVE_ROOT=$STORAGE_ROOT/archive"
  "LOOM_GENERATED_ROOT=$TMP_ROOT/data/generated" "LOOM_BOX_STATE_ROOT=$TMP_ROOT/data/box-state"
  "LOOM_STORAGE_RETENTION_ROOT=$TMP_ROOT/data/storage-retention" "LOOM_BOX_PATH=$BOX_ROOT"
  "LOOM_SOCKET_PATH=$SOCKET_PATH" "LOOM_DB_URL=$MAIN_DB_URL" "LOOM_PROVENANCE_DB_URL=$PROVENANCE_DB_URL"
  "LOOM_MIGRATIONS_DIR=$ROOT/migrations" "LOOM_AUTO_MIGRATE=true" "LOOM_BOOTSTRAP_MODE=dev"
  "LOOM_WORKSPACE_ARCHIVE_ENABLED=true" "LOOM_WORKSPACE_ARCHIVE_MANIFEST_KEY_ID=project-physical-smoke-v1"
)
loom() { env "${RUNTIME_ENV[@]}" "$LOOM_BIN" --socket "$SOCKET_PATH" --json "$@"; }
sql() { "$PG_BIN/psql" -X -At -v ON_ERROR_STOP=1 "$MAIN_DB_URL" -c "$1"; }
start_daemon() {
  local attempts=300
  env "${RUNTIME_ENV[@]}" "$LOOMD_BIN" serve >>"$TMP_ROOT/loomd.log" 2>&1 &
  DAEMON_PID=$!
  while [[ "$attempts" -gt 0 ]]; do
    kill -0 "$DAEMON_PID" 2>/dev/null || fail 'disposable daemon exited during startup'
    if [[ -S "$SOCKET_PATH" ]] && curl --silent --fail --unix-socket "$SOCKET_PATH" http://loom/v1/health >/dev/null; then return; fi
    sleep 0.1
    attempts=$((attempts - 1))
  done
  fail 'disposable daemon did not become ready in thirty seconds'
}

log 'building CLI and daemon; starting isolated PostgreSQL-backed runtime'
(cd "$ROOT" && go build -o "$LOOM_BIN" ./cmd/loom)
(cd "$ROOT" && go build -o "$LOOMD_BIN" ./cmd/loomd)
start_daemon
[[ "$(sql 'SELECT max(version_id) FROM public.goose_db_version WHERE is_applied')" == 68 ]] || fail 'disposable database is not at migration 68'
pass 'real daemon is ready against smoke-owned PostgreSQL at head 68'

log 'registering a canonical inactive project through the supported CLI'
loom box init --path "$BOX_ROOT" --profile main >"$TMP_ROOT/box-init.json"
jq -e '.status_after.state == "ok" and .status_after.initialized == true' "$TMP_ROOT/box-init.json" >/dev/null
loom project scaffold 'Physical Acceptance' --owner-node main --slug physical-acceptance --facets repos --directory "$BOX_ROOT/Projects" >"$TMP_ROOT/scaffold.json"
PROJECT_ROOT="$BOX_ROOT/Projects/physical-acceptance"
# The canonical archive contract accepts active project declarations, whereas
# a new scaffold deliberately starts as draft. Prepare that declared fixture
# before registration; do not change a persisted lifecycle to bypass a guard.
sed 's/status: draft/status: active/' "$PROJECT_ROOT/.loom/project.yaml" >"$TMP_ROOT/active-project.yaml"
mv "$TMP_ROOT/active-project.yaml" "$PROJECT_ROOT/.loom/project.yaml"
printf 'physical project payload\n' >"$PROJECT_ROOT/payload.txt"
loom project register "$PROJECT_ROOT" --backend --idempotency-key physical-acceptance-register >"$TMP_ROOT/register-result.json"
PROJECT_ID="$(jq -er '.data.detail.project.project.project_id' "$TMP_ROOT/register-result.json")"
pass 'supported registration created the canonical project'

if ! loom project archive plan "$PROJECT_ID" --reason 'disposable independent acceptance' >"$TMP_ROOT/archive-plan.json"; then
  (cd "$ROOT" && LOOM_PROJECT_PHYSICAL_TEST_DB_URL="$MAIN_DB_URL" go test -count=1 -run '^TestProjectPhysicalPostgresAcceptanceLivePlan$' -v ./internal/storagearchive)
  fail 'supported archive review failed'
fi
ARCHIVE_OP="$(jq -er '.workspace.operation_id' "$TMP_ROOT/archive-plan.json")"
ARCHIVE_DIGEST="$(jq -er '.plan_digest' "$TMP_ROOT/archive-plan.json")"
[[ "$(sql 'SELECT count(*) FROM projects.physical_archive_plan_evidence')" == 0 ]] || fail 'planning persisted private plan evidence'
[[ "$(sql 'SELECT count(*) FROM storage.workspace_archive_operations')" == 0 ]] || fail 'planning wrote the workspace journal'
stop_daemon
start_daemon
pass 'read-only review survived an actual daemon restart'

loom project archive apply "$PROJECT_ID" "$TMP_ROOT/archive-plan.json" --plan-digest "$ARCHIVE_DIGEST" --yes >"$TMP_ROOT/archive-result.json"
jq -e '.ok == true and (.data | .phase == "complete" and .mutation_blocked == true and .recoverable == false)' "$TMP_ROOT/archive-result.json" >/dev/null
ARCHIVE_ROOT="$STORAGE_ROOT/archive/projects/physical-acceptance/project"
[[ ! -e "$PROJECT_ROOT" && -f "$ARCHIVE_ROOT/payload.txt" ]] || fail 'archive did not leave exactly one payload'
[[ "$(sql 'SELECT count(*) FROM projects.physical_archive_plan_evidence')" == 1 ]] || fail 'archive did not preserve one exact plan'
stop_daemon
start_daemon
loom project archive inspect "$PROJECT_ID" >"$TMP_ROOT/archive-inspect.json"
loom project archive recover "$PROJECT_ID" "$ARCHIVE_OP" --plan-digest "$ARCHIVE_DIGEST" --yes >"$TMP_ROOT/archive-replay.json"
jq -e '.ok == true and (.data | .phase == "complete" and .replay == true)' "$TMP_ROOT/archive-replay.json" >/dev/null
pass 'archive and exact replay survive restart with one preserved payload and plan'

loom project archive restore plan "$PROJECT_ID" --reason 'disposable inactive restore' >"$TMP_ROOT/restore-plan.json"
RESTORE_OP="$(jq -er '.workspace.operation_id' "$TMP_ROOT/restore-plan.json")"
RESTORE_DIGEST="$(jq -er '.plan_digest' "$TMP_ROOT/restore-plan.json")"
stop_daemon
start_daemon
loom project archive restore apply "$PROJECT_ID" "$TMP_ROOT/restore-plan.json" --plan-digest "$RESTORE_DIGEST" --yes >"$TMP_ROOT/restore-result.json"
jq -e '.ok == true and (.data | .phase == "complete" and .mutation_blocked == true and .activation_state == "inactive" and .recoverable == false)' "$TMP_ROOT/restore-result.json" >/dev/null
[[ -f "$PROJECT_ROOT/payload.txt" && ! -e "$ARCHIVE_ROOT" ]] || fail 'restore did not leave exactly one payload'
stop_daemon
start_daemon
loom project archive restore recover "$PROJECT_ID" "$RESTORE_OP" --plan-digest "$RESTORE_DIGEST" --yes >"$TMP_ROOT/restore-replay.json"
jq -e '.ok == true and (.data | .phase == "complete" and .replay == true and .activation_state == "inactive")' "$TMP_ROOT/restore-replay.json" >/dev/null
[[ "$(sql 'SELECT count(*) FROM projects.physical_archive_plan_evidence')" == 2 ]] || fail 'restore private evidence count is not two'
pass 'restore and replay survive restart without runtime activation'

stop_daemon
log 'running independent real-process crash/recovery and fidelity matrix'
(cd "$ROOT" && LOOM_PROJECT_PHYSICAL_TEST_DB_URL="$MAIN_DB_URL" go test -race -count=1 -run '^TestProjectPhysicalPostgresAcceptance(CrashMatrix|IdentityConstraints|ConcurrentRecovery)$' -v ./internal/storagearchive)
pass 'project and generic move durable boundaries survive real process exit and database-backed recovery'

log 'running combined registry and owner-node fence acceptance (host manager simulated)'
(cd "$ROOT" && LOOM_PROJECT_PHYSICAL_TEST_DB_URL="$MAIN_DB_URL" go test -race -count=1 -run '^TestProjectPhysicalPostgresAcceptanceRuntime$' -v ./internal/storagearchive)
pass 'active runtimes stop before movement and remain fenced through inactive restore'

for evidence in "$TMP_ROOT"/*.json "$TMP_ROOT/loomd.log"; do
  if grep -F -f "$CREDENTIALS_ROOT/workspace-archive-manifest-key" "$evidence" >/dev/null; then fail 'manifest key leaked into output'; fi
done
printf '[project-physical] PASS (%d checks)\n' "$PASS_COUNT"

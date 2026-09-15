#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_BASE="${TMPDIR:-/tmp}"
TMP_BASE="${TMP_BASE%/}"
TMP_ROOT="$(mktemp -d "$TMP_BASE/loom-cloud-runtime-optimization.XXXXXX")"
PG_SOCKET="$(mktemp -d /tmp/loom-cloud-runtime-pg.XXXXXX)"
SOCKET_ROOT="$(mktemp -d /tmp/loom-cloud-runtime-socket.XXXXXX)"
PG_DATA="$TMP_ROOT/postgres"
RUNTIME_ROOT="$TMP_ROOT/runtime"
GO_TMP_ROOT="$TMP_ROOT/go-tmp"
SOCKET_PATH="$SOCKET_ROOT/loomd.sock"
LOOM_BIN="$TMP_ROOT/bin/loom"
LOOMD_BIN="$TMP_ROOT/bin/loomd"
DAEMON_PID=""
POSTGRES_STARTED=false
PASS_COUNT=0

log() {
  printf '[cloud-runtime-optimization] %s\n' "$*"
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
  local daemon_status=0
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
    [[ "$remaining" -gt 0 ]] || fail 'disposable loomd did not stop within ten seconds'
  fi
  wait "$DAEMON_PID" || daemon_status=$?
  DAEMON_PID=""
  [[ "$daemon_status" -eq 0 ]] || fail "disposable loomd exited with status $daemon_status"
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
    */loom-cloud-runtime-optimization.*)
      find "$TMP_ROOT" -xdev -depth -delete >/dev/null 2>&1 || true
      ;;
  esac
  case "$PG_SOCKET" in
    /tmp/loom-cloud-runtime-pg.*)
      find "$PG_SOCKET" -xdev -depth -delete >/dev/null 2>&1 || true
      ;;
  esac
  case "$SOCKET_ROOT" in
    /tmp/loom-cloud-runtime-socket.*)
      find "$SOCKET_ROOT" -xdev -depth -delete >/dev/null 2>&1 || true
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
  fail 'Nix is required for the pinned disposable Borg and PostgreSQL toolchains'
}

resolve_borg() {
  local candidate="${LOOM_TEST_BORG_BINARY:-}"
  local output_root
  local output_path
  if [[ -n "$candidate" ]]; then
    [[ -x "$candidate" ]] || fail "LOOM_TEST_BORG_BINARY is not executable: $candidate"
  else
    output_root="$(
      cd "$ROOT"
      "$NIX" --extra-experimental-features 'nix-command flakes' \
        build --no-link --print-out-paths --impure --expr \
        'let
          flake = (import ./tests/nix/source-flake.nix {});
          pkgs = flake.inputs.nixpkgs.legacyPackages.${builtins.currentSystem};
        in pkgs.borgbackup.overrideAttrs (_: {
          version = "1.4.3";
          src = pkgs.fetchPypi {
            pname = "borgbackup";
            version = "1.4.3";
            hash = "sha256-ebv6dF0ZAdaFlzWEvS0Wo1Bobd0Xb2oiREkPsBmWRB8=";
          };
        })'
    )"
    candidate=""
    while IFS= read -r output_path; do
      if [[ -x "$output_path/bin/borg" ]]; then
        candidate="$output_path/bin/borg"
        break
      fi
    done <<<"$output_root"
    [[ -n "$candidate" ]] || fail 'Nix did not return an executable Borg 1.4.3 output'
  fi
  [[ "$($candidate --version)" == 'borg 1.4.3' ]] \
    || fail "disposable acceptance requires Borg 1.4.3: $candidate"
  BORG_BINARY="$candidate"
}

resolve_postgres() {
  local postgres_root
  postgres_root="$(
    cd "$ROOT"
    "$NIX" --extra-experimental-features 'nix-command flakes' \
      build --no-link --print-out-paths --impure --expr \
      'let flake = (import ./tests/nix/source-flake.nix {}); in flake.inputs.nixpkgs.legacyPackages.${builtins.currentSystem}.postgresql_17.withPackages (p: [ p.pgvector ])' \
      | tail -n 1
  )"
  [[ -x "$postgres_root/bin/initdb" ]] || fail 'Nix did not return a PostgreSQL 17 server toolchain'
  [[ -x "$postgres_root/bin/pg_dump" ]] || fail 'PostgreSQL toolchain is missing pg_dump'
  [[ -x "$postgres_root/bin/pg_restore" ]] || fail 'PostgreSQL toolchain is missing pg_restore'
  PG_BIN="$postgres_root/bin"
  export PATH="$PG_BIN:$PATH"
}

start_postgres() {
  "$PG_BIN/initdb" -D "$PG_DATA" --username=postgres --auth-local=trust \
    --auth-host=reject --encoding=UTF8 --no-locale >/dev/null
  printf "\nlisten_addresses = ''\nunix_socket_directories = '%s'\nunix_socket_permissions = 0700\n" \
    "$PG_SOCKET" >>"$PG_DATA/postgresql.conf"
  if ! "$PG_BIN/pg_ctl" -D "$PG_DATA" -l "$TMP_ROOT/postgres.log" -w start >/dev/null; then
    sed -n '1,240p' "$TMP_ROOT/postgres.log" >&2
    fail 'smoke-owned PostgreSQL did not start'
  fi
  POSTGRES_STARTED=true
	"$PG_BIN/psql" -X -v ON_ERROR_STOP=1 -h "$PG_SOCKET" -U postgres -d postgres \
	  -c 'CREATE ROLE loom LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT' >/dev/null
	"$PG_BIN/psql" -X -v ON_ERROR_STOP=1 -h "$PG_SOCKET" -U postgres -d template1 \
	  -c 'CREATE EXTENSION IF NOT EXISTS vector' >/dev/null
	"$PG_BIN/createdb" -h "$PG_SOCKET" -U postgres -O loom loom_main
  "$PG_BIN/psql" -X -v ON_ERROR_STOP=1 -h "$PG_SOCKET" -U postgres -d loom_main \
    -c 'CREATE EXTENSION IF NOT EXISTS vector' >/dev/null
  "$PG_BIN/psql" -X -v ON_ERROR_STOP=1 -h "$PG_SOCKET" -U postgres -d postgres \
    -c 'CREATE ROLE loom_provenance LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT' >/dev/null
  "$PG_BIN/createdb" -h "$PG_SOCKET" -U postgres -O loom_provenance loom_provenance
}

runtime_env() {
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
	  PGHOST="$PG_SOCKET" \
	  PGUSER=loom \
    LOOM_MIGRATIONS_DIR="$ROOT/migrations" \
    LOOM_BOOTSTRAP_MODE=dev \
    "$@"
}

start_daemon() {
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
	  PGHOST="$PG_SOCKET" \
	  PGUSER=loom \
    LOOM_MIGRATIONS_DIR="$ROOT/migrations" \
    LOOM_BOOTSTRAP_MODE=dev \
    LOOM_AUTO_MIGRATE=true \
    "$LOOMD_BIN" serve >"$TMP_ROOT/loomd.log" 2>&1 &
  DAEMON_PID=$!
  while [[ "$remaining" -gt 0 ]]; do
    if [[ -S "$SOCKET_PATH" ]]; then
      return
    fi
    if ! kill -0 "$DAEMON_PID" >/dev/null 2>&1; then
      sed -n '1,240p' "$TMP_ROOT/loomd.log" >&2
      fail 'disposable loomd exited before creating its Unix socket'
    fi
    sleep 0.1
    remaining=$((remaining - 1))
  done
  sed -n '1,240p' "$TMP_ROOT/loomd.log" >&2
  fail 'disposable loomd did not create its Unix socket within thirty seconds'
}

wait_for_worker_policy() {
  local worker_ref="$1"
  local output_path="$2"
  local remaining=100
  while [[ "$remaining" -gt 0 ]]; do
    if runtime_env "$LOOM_BIN" --socket "$SOCKET_PATH" --json worker policy inspect "$worker_ref" >"$output_path" 2>/dev/null \
      && jq -e '.ok == true and .data.policy_fingerprint != ""' "$output_path" >/dev/null; then
      return
    fi
    sleep 0.1
    remaining=$((remaining - 1))
  done
  sed -n '1,240p' "$output_path" >&2 || true
  fail "worker policy did not become available: $worker_ref"
}

verify_daily_dry_run() {
  local worker_ref="$1"
  local local_time="$2"
  local label="$3"
  local inspect_path="$TMP_ROOT/${worker_ref//./-}-inspect.json"
  local dry_run_path="$TMP_ROOT/${worker_ref//./-}-dry-run.json"
  local fingerprint
  wait_for_worker_policy "$worker_ref" "$inspect_path"
  fingerprint="$(jq -er '.data.policy_fingerprint' "$inspect_path")"
  runtime_env "$LOOM_BIN" --socket "$SOCKET_PATH" --json worker policy set "$worker_ref" \
    --dry-run --mode daily_local --local-time "$local_time" --timezone Europe/Amsterdam \
    --expected-policy-fingerprint "$fingerprint" >"$dry_run_path"
  jq -e --arg local_time "$local_time" '
    .ok == true and
    .data.dry_run == true and
    .data.applied == false and
    .data.new.policy.mode == "daily_local" and
    .data.new.policy.local_time == $local_time and
    .data.new.policy.timezone == "Europe/Amsterdam" and
    .data.new.next_run_after != null
  ' "$dry_run_path" >/dev/null || fail "$label daily-local dry-run did not preserve exact policy evidence"
}

verify_real_profiles() {
  local verify_root="$TMP_ROOT/verify-profiles"
  local repository="$verify_root/repository"
  local source="$verify_root/source"
  local state="$verify_root/state"
  local passphrase="$verify_root/borg.passphrase"
  local config_path="$verify_root/cloud.json"
  local archive='loom-main-history-profile'
  mkdir -p "$source" "$state/cache" "$state/security"
  printf 'profile verification payload\n' >"$source/payload.txt"
  printf 'disposable-only\n' >"$passphrase"
  chmod 0600 "$passphrase"
  env BORG_CACHE_DIR="$state/cache" BORG_SECURITY_DIR="$state/security" \
    "$BORG_BINARY" init --encryption none "$repository" >/dev/null
  (
    cd "$source"
    env BORG_CACHE_DIR="$state/cache" BORG_SECURITY_DIR="$state/security" \
      "$BORG_BINARY" create --files-cache ctime,size,inode --files-changed ctime \
      "$repository::$archive" . >/dev/null
  )
  printf '[loom-cloud]\ntype = sftp\n' >"$verify_root/rclone.conf"
  printf '{\n  "schema_version": "loom.cloud.config.v0.6.4",\n  "enabled": true,\n  "provider": "disposable_local",\n  "driver": "rclone",\n  "remote_name": "loom-cloud",\n  "remote_root": "loom",\n  "rclone_config_path": "%s",\n  "rclone_binary": "rclone",\n  "state_dir": "%s",\n  "roots": {"main_snapshots":"main-snapshots","full_offload":"full-offload","cloud_folder":"cloud-folder"},\n  "snapshots": {"backend":"borg","borg":{"binary":"%s","repository":"%s","passphrase_file":"%s","cache_dir":"%s","security_dir":"%s","encryption":"none","compression":"none","check_mode":"repository"}}\n}\n' \
    "$verify_root/rclone.conf" "$state" "$BORG_BINARY" "$repository" "$passphrase" "$state/cache" "$state/security" >"$config_path"

  runtime_env "$LOOM_BIN" --json cloud snapshot verify "$archive" --local \
    --node-id loom-main --cloud-config "$config_path" --profile metadata >"$verify_root/metadata.json"
  jq -e '.ok == true and .data.status == "succeeded" and .data.profile == "metadata" and .data.coverage == "archive_metadata_complete"' \
    "$verify_root/metadata.json" >/dev/null

  runtime_env "$LOOM_BIN" --json cloud snapshot verify --local \
    --node-id loom-main --cloud-config "$config_path" --profile rolling_repository --max-duration 1s >"$verify_root/rolling.json"
  jq -e '.ok == true and .data.status == "succeeded" and .data.profile == "rolling_repository" and .data.coverage == "repository_check_time_bounded" and .data.archive == null' \
    "$verify_root/rolling.json" >/dev/null

  runtime_env "$LOOM_BIN" --json cloud snapshot verify "$archive" --local \
    --node-id loom-main --cloud-config "$config_path" --profile archive_data >"$verify_root/archive-data.json"
  jq -e '.ok == true and .data.status == "succeeded" and .data.profile == "archive_data" and .data.coverage == "archive_data_complete"' \
    "$verify_root/archive-data.json" >/dev/null
}

require_command go
require_command jq
require_command sed
NIX="$(resolve_nix)"

log 'resolving exact disposable Borg 1.4.3 and PostgreSQL 17 + pgvector'
resolve_borg
resolve_postgres
pass 'exact disposable toolchains resolved'

mkdir -p "$TMP_ROOT/bin" "$RUNTIME_ROOT/run" "$RUNTIME_ROOT/storage/imports" \
  "$RUNTIME_ROOT/storage/backups" "$RUNTIME_ROOT/storage/archive" \
  "$RUNTIME_ROOT/data/object-store" "$RUNTIME_ROOT/data/box-state" \
  "$RUNTIME_ROOT/data/generated" "$RUNTIME_ROOT/data/storage-retention" \
  "$RUNTIME_ROOT/box/Documents" "$GO_TMP_ROOT"
chmod 0700 "$GO_TMP_ROOT"
export TMPDIR="$GO_TMP_ROOT"

log 'starting smoke-owned PostgreSQL and bootstrapping the real disposable runtime'
start_postgres
MAIN_DB_URL="postgresql://loom@/loom_main?host=$PG_SOCKET"
PROVENANCE_DB_URL="postgresql://loom_provenance@/loom_provenance?host=$PG_SOCKET"
ADMIN_DB_URL="postgresql://postgres@/postgres?host=$PG_SOCKET"
(cd "$ROOT" && go build -o "$LOOM_BIN" ./cmd/loom && go build -o "$LOOMD_BIN" ./cmd/loomd)
start_daemon
pass 'real loomd migrated and seeded isolated main/provenance state'

log 'proving exact 03:00 and 03:15 Europe/Amsterdam policy transport without applying either schedule'
verify_daily_dry_run main.main_backup 03:00 'main backup'
verify_daily_dry_run main.cloud_snapshot_upload 03:15 'cloud archive'
pass 'both accepted daily-local policies dry-run with durable next occurrences'

log 'running real Borg 1.4.3 v1/v2 create, repeat, change, fetch, and interrupted-resume paths'
(
  cd "$ROOT"
  LOOM_TEST_BORG_BINARY="$BORG_BINARY" go test -count=1 -run \
	  '^(TestBorgDirectArchiveV2DisposableRepositoryIncrementalCreateRepeatChangeAndPendingResume|TestBorgDirectArchiveDisposableRepository|TestBorgDirectArchiveV2DisposableRepositoryHistoricalAndCurrentStrictRestore|TestBorgDirectArchiveV2RawBoxRootsCompatibility)$' \
    ./internal/cloudstorage
)
pass 'real disposable Borg v1/v2 archive paths passed'

log 'running the adversarial v2 mutation, topology, warning, boundary, and strict recovery matrix'
(
  cd "$ROOT"
  go test -count=1 -run \
    '^(TestBorgDirectArchiveV2IncrementalCreateUsesBoundedVerificationAndEvidence|TestBorgDirectArchiveV2AddRemoveAndMetadataOnlyCases|TestBorgDirectArchiveV2PendingResumeRequiresTrustedLocalEvidence|TestBorgDirectArchiveV2RequestPreservesV1CanonicalReplayAndPendingResume|TestBorgDirectArchiveV2SharedRemoteLockConflictFailsBeforeBorg|TestBorgDirectArchiveV2WarningAndPackageDriftRemainFailures|TestBorgDirectArchiveV2DoesNotPerformSecondUserRootPreparation|TestBorgSnapshotListKeepsLegacyAndCommittedDirectNamespacesSeparate|TestBorgDirectArchiveFetchAndStrictOperationalProvenanceRestore|TestBorgDirectArchiveV2FetchAuthenticatesConfinesAndStrictlyRestores|TestBorgDirectArchiveV2FetchRejectsUnexpectedOrEscapingPayloadBeforeExtraction|TestBorgDirectArchiveV2FetchRejectsEscapingSymlinkAndPackageDrift)$' \
    ./internal/cloudstorage
)
pass 'add/remove/metadata/exclusion/link/warning/resume/fetch/restore matrix passed'

log 'proving all three assurance profiles against the real disposable Borg repository'
verify_real_profiles
(
  cd "$ROOT"
  go test -count=1 -run \
    '^(TestBorgSnapshotVerifyUsesExplicitDeepAssuranceProfiles|TestBorgSnapshotVerifyRejectsMixedOrIncompleteProfilesBeforeBorg|TestBorgSnapshotVerifyFailureDoesNotClaimCompletedCoverage)$' \
    ./internal/cloudstorage
)
pass 'metadata, bounded rolling, and archive-data claims are typed and truthful'

log 'running daily-local normal/DST, stale-package, and pre-mutation overlap refusals'
(
  cd "$ROOT"
  LOOM_TEST_DB_URL="$MAIN_DB_URL" go test -count=1 -run \
    '^(TestDailyLocalNextAfterEuropeAmsterdamNormalAndDSTDays|TestDailyLocalDSTGapAndOverlapFireAtMostOncePerCalendarDay|TestDailyWorkerPolicyApplicationDueSelectionAndRacePostgres|TestSupervisorTickIdempotencyKeyUsesDurableDailyOccurrence)$' \
    ./internal/workers
  LOOM_TEST_DB_URL="$MAIN_DB_URL" go test -count=1 -run \
    '^(TestCloudSnapshotScheduledDailyRecoveryPackageWindowNormalAndDSTDays|TestCloudSnapshotScheduledDailyRejectsStaleOrMissingPackageBeforeMutation)$' \
    ./internal/workers/runtimes
)
pass 'daily-local DST and stale/overlap refusal contracts passed before mutation'

log 'running real disposable PostgreSQL dump/restore integrity proof'
(
  cd "$ROOT"
  umask 077
  LOOM_PROVENANCE_TEST_DB_URL="$ADMIN_DB_URL" go test -count=1 -run \
	  '^(TestProvenanceRestoreDrillComparesCompleteLogicalLedgerPostgres|TestRestoreAuthorityDisposableOperationalAndHistoricalProvenancePostgres)$' ./internal/backup
)
pass 'strict PostgreSQL authority recovery and historical integrity proof passed'

log 'proving typed authority failures and atomic secret-free restore-failure receipts'
(
  cd "$ROOT"
  go test -count=1 -run \
    '^(TestRestoreAuthorityFailureContractIsClosedBoundedAndSecretFree|TestRestoreAuthorityFailureCancellationAndCleanupTruth|TestOperationalRestoreDrillPreservesTypedAuthorityFailureThroughWrappingAndCleanup)$' \
    ./internal/restoreauthority ./internal/backup
  go test -count=1 -run '^TestRestoreFailure(Receipt|Evidence)' ./internal/cloudstorage
)
pass 'typed stage/code, cleanup truth, redaction, and atomic receipt contracts passed'

stop_daemon
printf '[ok] cloud backup runtime optimization local acceptance passed (%d checkpoints)\n' "$PASS_COUNT"

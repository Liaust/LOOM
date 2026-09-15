#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FIXTURES="$ROOT/internal/estatemigration/testdata/project_pilot"
PACK_SOURCE="$ROOT/internal/projectcontracts/repo_development_pack"
STATE_PREFIX=loom-digital-estate-project-pilot.
PHASE=""
STATE_ROOT=""
STATE_ID=""
CONTROL_DIR=""
ACCEPTANCE_RECEIPT=""
VERIFICATION_RECEIPT=""
HMAC_KEY=""
MARKER_FILE=""
TMP_ROOT=""
PG_SOCKET=""
PG_DATA=""
RUNTIME_ROOT=""
PROJECTS_ROOT=""
SOURCE_ROOT=""
SOCKET_PATH=""
LOOM_BIN=""
LOOMD_BIN=""
DAEMON_PID=""
POSTGRES_STARTED=false
ACTIVE_WORKTREE_ID=""
PASS_COUNT=0

export GIT_OPTIONAL_LOCKS=0

usage() {
  printf 'usage: %s prepare|verify-orca|finalize --state-dir /private/tmp/%s<id>\n' \
    "$0" "$STATE_PREFIX" >&2
  exit 2
}

log() {
  printf '[digital-estate-project-pilot] %s\n' "$*"
}

pass() {
  PASS_COUNT=$((PASS_COUNT + 1))
  printf '[ok] %s\n' "$*"
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

parse_arguments() {
  [[ "$#" -eq 3 ]] || usage
  PHASE="$1"
  [[ "$PHASE" == prepare || "$PHASE" == verify-orca || "$PHASE" == finalize ]] || usage
  [[ "$2" == --state-dir ]] || usage
  case "$3" in
    /*) ;;
    *) fail 'state directory must be an absolute path' ;;
  esac

  local parent base
  parent="$(realpath "$(dirname "$3")")"
  base="$(basename "$3")"
  [[ "$base" == "$STATE_PREFIX"* && "$base" != "$STATE_PREFIX" ]] \
    || fail "state directory basename must begin with $STATE_PREFIX"
  STATE_ROOT="$parent/$base"
  case "$STATE_ROOT" in
    /private/tmp/loom-digital-estate-project-pilot.*|/tmp/loom-digital-estate-project-pilot.*) ;;
    *) fail 'state directory must be a uniquely named child of the local temporary directory' ;;
  esac
  [[ "$STATE_ROOT" != "$ROOT" && "$STATE_ROOT" != "$ROOT/"* ]] \
    || fail 'state directory must be outside the repository'
  [[ ! -L "$STATE_ROOT" ]] || fail 'state directory must not be a symbolic link'

  TMP_ROOT="$STATE_ROOT"
  CONTROL_DIR="$STATE_ROOT/control"
  ACCEPTANCE_RECEIPT="$STATE_ROOT/acceptance-state.json"
  VERIFICATION_RECEIPT="$STATE_ROOT/orca-verification-state.json"
  HMAC_KEY="$CONTROL_DIR/acceptance-hmac.key"
  MARKER_FILE="$CONTROL_DIR/state-marker.json"
  # Keep Unix socket paths below Darwin's sun_path limit while retaining every
  # artifact inside the caller-supplied state directory.
  PG_SOCKET="$STATE_ROOT/pgs"
  PG_DATA="$STATE_ROOT/postgres"
  RUNTIME_ROOT="$STATE_ROOT/runtime"
  PROJECTS_ROOT="$RUNTIME_ROOT/box/Projects"
  SOURCE_ROOT="$STATE_ROOT/sources"
  SOCKET_PATH="$STATE_ROOT/l.sock"
  LOOM_BIN="$STATE_ROOT/bin/loom"
  LOOMD_BIN="$STATE_ROOT/bin/loomd"
}

state_path() {
  case "$1" in
    "$STATE_ROOT"|"$STATE_ROOT"/*) printf '%s\n' "$1" ;;
    *) fail "path escapes smoke state directory: $1" ;;
  esac
}

sha256_file() {
  shasum -a 256 "$1" | awk '{print $1}'
}

hmac_file() {
  local key_hex
  key_hex="$(tr -d '\r\n' <"$HMAC_KEY")"
  [[ "$key_hex" =~ ^[0-9a-f]{64}$ ]] || fail 'smoke receipt HMAC key is malformed'
  openssl dgst -sha256 -mac HMAC -macopt "hexkey:$key_hex" -r "$1" | awk '{print $1}'
}

initialize_state() {
  if [[ -e "$STATE_ROOT" ]]; then
    [[ -d "$STATE_ROOT" && ! -L "$STATE_ROOT" ]] || fail 'existing state path is not a real directory'
    [[ -z "$(find "$STATE_ROOT" -mindepth 1 -maxdepth 1 -print -quit)" ]] \
      || fail 'prepare requires an empty state directory or a valid completed acceptance receipt'
  else
    mkdir "$STATE_ROOT"
  fi
  chmod 700 "$STATE_ROOT"
  mkdir -p "$CONTROL_DIR" "$STATE_ROOT/run"
  chmod 700 "$CONTROL_DIR" "$STATE_ROOT/run"
  STATE_ID="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  local created_at marker_tmp
  created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  marker_tmp="$CONTROL_DIR/state-marker.json.tmp"
  jq -n -S \
    --arg schema_version loom.digital_estate_project_pilot_state_marker.v1 \
    --arg state_id "$STATE_ID" \
    --arg state_root "$STATE_ROOT" \
    --arg created_at "$created_at" \
    '{schema_version:$schema_version,state_id:$state_id,state_root:$state_root,created_at:$created_at}' \
    >"$marker_tmp"
  mv "$marker_tmp" "$MARKER_FILE"
  openssl rand -hex 32 >"$HMAC_KEY"
  chmod 600 "$MARKER_FILE" "$HMAC_KEY"
}

load_state_identity() {
  [[ -d "$STATE_ROOT" && ! -L "$STATE_ROOT" ]] || fail 'smoke state directory is missing or unsafe'
  [[ -d "$CONTROL_DIR" && ! -L "$CONTROL_DIR" ]] || fail 'smoke control directory is missing or unsafe'
  [[ -f "$MARKER_FILE" && ! -L "$MARKER_FILE" ]] || fail 'smoke state marker is missing or unsafe'
  [[ -f "$HMAC_KEY" && ! -L "$HMAC_KEY" ]] || fail 'smoke receipt authentication key is missing or unsafe'
  jq -e --arg root "$STATE_ROOT" '
    .schema_version == "loom.digital_estate_project_pilot_state_marker.v1" and
    .state_root == $root and (.state_id | type == "string" and length > 0)
  ' "$MARKER_FILE" >/dev/null || fail 'smoke state marker does not bind this state directory'
  STATE_ID="$(jq -er '.state_id' "$MARKER_FILE")"
}

build_harness_manifest() {
  local output="$STATE_ROOT/harness-manifest.json"
  local records file relative digest bytes
  records="$(mktemp "$CONTROL_DIR/harness-records.XXXXXX")"
  : >"$records"
  local inputs=(
    "$ROOT/tests/smoke/v2_digital_estate_project_pilot.sh"
    "$ROOT/internal/estatemigration/project_pilot_test.go"
    "$ROOT/internal/cloudstorage/digital_estate_project_pilot_test.go"
  )
  while IFS= read -r file; do
    inputs+=("$file")
  done < <(find "$FIXTURES" -type f | LC_ALL=C sort)
  for file in "${inputs[@]}"; do
    [[ -f "$file" && ! -L "$file" ]] || fail "harness input is missing or unsafe: $file"
    relative="${file#"$ROOT/"}"
    [[ "$relative" != "$file" && "$relative" != .. && "$relative" != ../* && "$relative" != */../* ]] \
      || fail "harness input escapes repository: $file"
    digest="sha256:$(sha256_file "$file")"
    bytes="$(wc -c <"$file" | tr -d ' ')"
    jq -cn --arg path "$relative" --arg sha256 "$digest" --argjson bytes "$bytes" \
      '{path:$path,sha256:$sha256,bytes:$bytes}' >>"$records"
  done
  jq -s -S --arg state_id "$STATE_ID" \
    '{schema_version:"loom.digital_estate_project_pilot_harness_manifest.v1",state_id:$state_id,files:.}' \
    "$records" >"$output.tmp"
  chmod 600 "$output.tmp"
  mv "$output.tmp" "$output"
  find "$records" -delete
}

verify_harness_manifest() {
  local manifest="$STATE_ROOT/harness-manifest.json"
  [[ -f "$manifest" && ! -L "$manifest" ]] || fail 'harness manifest is missing or unsafe'
  jq -e --arg state_id "$STATE_ID" '
    .schema_version == "loom.digital_estate_project_pilot_harness_manifest.v1" and
    .state_id == $state_id and (.files | type == "array" and length >= 4)
  ' "$manifest" >/dev/null || fail 'harness manifest schema is invalid'
  local relative expected absolute actual
  while IFS=$'\t' read -r relative expected; do
    [[ -n "$relative" && "$relative" != /* && "$relative" != .. && "$relative" != ../* && "$relative" != */../* ]] \
      || fail "unsafe repository path in harness manifest: $relative"
    absolute="$ROOT/$relative"
    [[ -f "$absolute" && ! -L "$absolute" ]] || fail "harness input is missing or unsafe: $relative"
    actual="sha256:$(sha256_file "$absolute")"
    [[ "$actual" == "$expected" ]] || fail "harness input changed after prepare: $relative"
  done < <(jq -r '.files[] | [.path,.sha256] | @tsv' "$manifest")
}

write_authenticated_json() {
  local unsigned="$1"
  local output="$2"
  local canonical tag output_tmp
  canonical="$(mktemp "$CONTROL_DIR/unsigned.XXXXXX")"
  output_tmp="$output.tmp"
  jq -cS 'del(.authentication.tag)' "$unsigned" >"$canonical"
  tag="$(hmac_file "$canonical")"
  [[ "$tag" =~ ^[0-9a-f]{64}$ ]] || fail 'failed to compute receipt HMAC'
  jq -S --arg tag "hmac-sha256:$tag" '.authentication.tag = $tag' "$canonical" >"$output_tmp"
  chmod 600 "$output_tmp"
  mv "$output_tmp" "$output"
  find "$canonical" -delete
}

verify_evidence_manifest() {
  local manifest="$1"
  [[ -f "$manifest" && ! -L "$manifest" ]] || fail "evidence manifest is missing or unsafe: $manifest"
  jq -e '.schema_version == "loom.digital_estate_project_pilot_evidence_manifest.v1" and (.files | type == "array" and length > 0)' \
    "$manifest" >/dev/null || fail 'evidence manifest schema is invalid'
  local relative expected absolute actual
  while IFS=$'\t' read -r relative expected; do
    [[ -n "$relative" && "$relative" != /* && "$relative" != .. && "$relative" != ../* && "$relative" != */../* ]] \
      || fail "unsafe evidence path in manifest: $relative"
    absolute="$STATE_ROOT/$relative"
    state_path "$absolute" >/dev/null
    [[ -f "$absolute" && ! -L "$absolute" ]] || fail "evidence file is missing or unsafe: $relative"
    actual="sha256:$(sha256_file "$absolute")"
    [[ "$actual" == "$expected" ]] || fail "evidence digest changed: $relative"
  done < <(jq -r '.files[] | [.path,.sha256] | @tsv' "$manifest")
}

verify_authenticated_receipt() {
  local receipt="$1"
  local expected_schema="$2"
  local expected_phase="$3"
  [[ -f "$receipt" && ! -L "$receipt" ]] || fail "authenticated receipt is missing or unsafe: $receipt"

  local canonical expected_tag actual_tag key_id marker_digest manifest_relative manifest_digest manifest_absolute
  canonical="$(mktemp "$CONTROL_DIR/receipt-check.XXXXXX")"
  jq -cS 'del(.authentication.tag)' "$receipt" >"$canonical"
  expected_tag="hmac-sha256:$(hmac_file "$canonical")"
  actual_tag="$(jq -er '.authentication.tag' "$receipt")"
  find "$canonical" -delete
  [[ "$actual_tag" == "$expected_tag" ]] || fail "receipt HMAC authentication failed: $receipt"

  key_id="sha256:$(sha256_file "$HMAC_KEY")"
  marker_digest="sha256:$(sha256_file "$MARKER_FILE")"
  jq -e \
    --arg schema "$expected_schema" \
    --arg phase "$expected_phase" \
    --arg state_id "$STATE_ID" \
    --arg root "$STATE_ROOT" \
    --arg key_id "$key_id" \
    --arg marker_digest "$marker_digest" '
      .schema_version == $schema and .phase == $phase and
      .state_id == $state_id and .state_root == $root and
      .state_marker_sha256 == $marker_digest and
      .authentication.algorithm == "hmac-sha256" and
      .authentication.key_id == $key_id
    ' "$receipt" >/dev/null || fail "receipt identity binding failed: $receipt"

  manifest_relative="$(jq -er '.evidence_manifest.path' "$receipt")"
  [[ "$manifest_relative" != /* && "$manifest_relative" != .. && "$manifest_relative" != ../* && "$manifest_relative" != */../* ]] \
    || fail 'receipt evidence manifest path is unsafe'
  manifest_absolute="$STATE_ROOT/$manifest_relative"
  manifest_digest="sha256:$(sha256_file "$manifest_absolute")"
  [[ "$manifest_digest" == "$(jq -er '.evidence_manifest.sha256' "$receipt")" ]] \
    || fail 'receipt evidence manifest digest changed'
  verify_evidence_manifest "$manifest_absolute"
  if [[ "$expected_schema" == loom.digital_estate_project_acceptance_state.v1 ]]; then
    [[ "$(jq -er '.harness_manifest.path' "$receipt")" == harness-manifest.json ]] \
      || fail 'acceptance receipt harness manifest path changed'
    [[ "$(jq -er '.harness_manifest.sha256' "$receipt")" == "sha256:$(sha256_file "$STATE_ROOT/harness-manifest.json")" ]] \
      || fail 'acceptance receipt harness manifest digest changed'
    verify_harness_manifest
  fi
}

build_evidence_manifest() {
  local output="$1"
  shift
  local records record file relative digest bytes
  records="$(mktemp "$CONTROL_DIR/evidence-records.XXXXXX")"
  : >"$records"
  for file in "$@"; do
    state_path "$file" >/dev/null
    [[ -f "$file" && ! -L "$file" ]] || fail "cannot bind missing evidence file: $file"
    relative="${file#"$STATE_ROOT/"}"
    digest="sha256:$(sha256_file "$file")"
    bytes="$(wc -c <"$file" | tr -d ' ')"
    jq -cn --arg path "$relative" --arg sha256 "$digest" --argjson bytes "$bytes" \
      '{path:$path,sha256:$sha256,bytes:$bytes}' >>"$records"
  done
  record="$output.tmp"
  jq -s -S --arg state_id "$STATE_ID" \
    '{schema_version:"loom.digital_estate_project_pilot_evidence_manifest.v1",state_id:$state_id,files:.}' \
    "$records" >"$record"
  chmod 600 "$record"
  mv "$record" "$output"
  find "$records" -delete
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
  local nix_command postgres_root
  nix_command="$(resolve_nix)"
  postgres_root="$(
    cd "$ROOT"
    "$nix_command" --extra-experimental-features 'nix-command flakes' \
      build --no-link --print-out-paths --impure --expr \
      'let flake = (import ./tests/nix/source-flake.nix {}); in flake.inputs.nixpkgs.legacyPackages.${builtins.currentSystem}.postgresql_17.withPackages (p: [ p.pgvector ])' \
      | tail -n 1
  )"
  [[ -x "$postgres_root/bin/initdb" ]] || fail 'Nix did not return a PostgreSQL server toolchain'
  PG_BIN="$postgres_root/bin"
  export PATH="$PG_BIN:$PATH"
}

start_postgres() {
  mkdir -p "$PG_SOCKET"
  "$PG_BIN/initdb" -D "$PG_DATA" --username=postgres --auth-local=trust \
    --auth-host=reject --encoding=UTF8 --no-locale >/dev/null
  printf "\nlisten_addresses = ''\nunix_socket_directories = '%s'\nunix_socket_permissions = 0700\n" \
    "$PG_SOCKET" >>"$PG_DATA/postgresql.conf"
  if ! "$PG_BIN/pg_ctl" -D "$PG_DATA" -l "$STATE_ROOT/postgres.log" -w start >/dev/null; then
    sed -n '1,240p' "$STATE_ROOT/postgres.log" >&2
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
  local remaining=300
  env \
    XDG_CONFIG_HOME="$RUNTIME_ROOT/config" \
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
    LOOM_AUTO_MIGRATE=true \
    LOOM_BOOTSTRAP_MODE=dev \
    "$LOOMD_BIN" serve >"$STATE_ROOT/loomd.log" 2>&1 &
  DAEMON_PID=$!

  while [[ "$remaining" -gt 0 ]]; do
    if [[ -S "$SOCKET_PATH" ]]; then
      return
    fi
    if ! kill -0 "$DAEMON_PID" >/dev/null 2>&1; then
      sed -n '1,240p' "$STATE_ROOT/loomd.log" >&2
      fail 'loomd exited before creating its disposable Unix socket'
    fi
    sleep 0.1
    remaining=$((remaining - 1))
  done
  sed -n '1,240p' "$STATE_ROOT/loomd.log" >&2
  fail 'loomd did not create its disposable Unix socket within thirty seconds'
}

stop_services() {
  local failures=0
  set +e
  if [[ -n "$DAEMON_PID" ]] && kill -0 "$DAEMON_PID" >/dev/null 2>&1; then
    kill -TERM "$DAEMON_PID" >/dev/null 2>&1 || failures=$((failures + 1))
    wait "$DAEMON_PID" >/dev/null 2>&1 || failures=$((failures + 1))
  fi
  DAEMON_PID=""
  if [[ "$POSTGRES_STARTED" == true ]]; then
    "$PG_BIN/pg_ctl" -D "$PG_DATA" -m fast -w stop >/dev/null 2>&1 || failures=$((failures + 1))
  fi
  POSTGRES_STARTED=false
  set -e
  ((failures == 0)) || return 1
}

cleanup() {
  local exit_status=$?
  local cleanup_failures=0
  trap - EXIT INT TERM HUP
  set +e
  if [[ -n "$ACTIVE_WORKTREE_ID" ]]; then
    orca terminal stop --worktree "id:$ACTIVE_WORKTREE_ID" --json >/dev/null 2>&1 || true
    orca worktree rm --worktree "id:$ACTIVE_WORKTREE_ID" --force --json >/dev/null 2>&1 \
      || cleanup_failures=$((cleanup_failures + 1))
  fi
  stop_services || cleanup_failures=$((cleanup_failures + 1))
  set -e
  if ((cleanup_failures > 0)); then
    printf '[fail] cleanup uncertainty; smoke state preserved at %s\n' "$STATE_ROOT" >&2
    exit 1
  fi
  exit "$exit_status"
}

trap cleanup EXIT INT TERM HUP

loom() {
  env \
    XDG_CONFIG_HOME="$RUNTIME_ROOT/config" \
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
    LOOM_BOOTSTRAP_MODE=dev \
    "$LOOM_BIN" --socket "$SOCKET_PATH" --json --no-animation --no-color "$@"
}

git_commit() {
  local repository="$1"
  local message="$2"
  git -C "$repository" -c user.name='LOOM Digital Estate Pilot' \
    -c user.email='digital-estate-pilot@invalid' commit -q -m "$message"
}

init_repository() {
  local repository="$1"
  mkdir -p "$repository"
  git -C "$repository" init -q -b main
}

snapshot_repository() {
  local repository="$1"
  local output="$2"
  {
    git -C "$repository" rev-parse HEAD
    git -C "$repository" symbolic-ref -q HEAD || true
    git -C "$repository" for-each-ref --format='%(refname) %(objectname)' | LC_ALL=C sort
    git -C "$repository" status --porcelain=v1 --untracked-files=all
  } >"$output"
}

compare_repository() {
  local source="$1"
  local target="$2"
  local label="$3"
  snapshot_repository "$source" "$STATE_ROOT/$label-source.git-state"
  snapshot_repository "$target" "$STATE_ROOT/$label-target.git-state"
  cmp -s "$STATE_ROOT/$label-source.git-state" "$STATE_ROOT/$label-target.git-state" \
    || fail "$label Git HEAD, refs, dirty, or untracked state changed"
  git -C "$source" fsck --full --strict >/dev/null
  git -C "$target" fsck --full --strict >/dev/null
}

record_retained_git_evidence() {
  snapshot_repository "$SOURCE_ROOT/single-project/repos/application" "$STATE_ROOT/retained-single-source.git-state"
  snapshot_repository "$PROJECTS_ROOT/single-project/repos/application" "$STATE_ROOT/retained-single-target.git-state"
  snapshot_repository "$SOURCE_ROOT/multi-project/repos/core" "$STATE_ROOT/retained-multi-core-source.git-state"
  snapshot_repository "$PROJECTS_ROOT/multi-project/repos/core" "$STATE_ROOT/retained-multi-core-target.git-state"
  snapshot_repository "$SOURCE_ROOT/multi-project/repos/plugin" "$STATE_ROOT/retained-multi-plugin-source.git-state"
  snapshot_repository "$PROJECTS_ROOT/multi-project/repos/plugin" "$STATE_ROOT/retained-multi-plugin-target.git-state"
  git -C "$SOURCE_ROOT/multi-project/repos/core" submodule status --recursive \
    >"$STATE_ROOT/retained-multi-core-source.submodule-state"
  git -C "$PROJECTS_ROOT/multi-project/repos/core" submodule status --recursive \
    >"$STATE_ROOT/retained-multi-core-target.submodule-state"
}

verify_retained_git_evidence() {
  local checks current repository evidence
  checks="$(mktemp "$CONTROL_DIR/git-checks.XXXXXX")"
  printf '%s\t%s\n' \
    "$SOURCE_ROOT/single-project/repos/application" "$STATE_ROOT/retained-single-source.git-state" \
    "$PROJECTS_ROOT/single-project/repos/application" "$STATE_ROOT/retained-single-target.git-state" \
    "$SOURCE_ROOT/multi-project/repos/core" "$STATE_ROOT/retained-multi-core-source.git-state" \
    "$PROJECTS_ROOT/multi-project/repos/core" "$STATE_ROOT/retained-multi-core-target.git-state" \
    "$SOURCE_ROOT/multi-project/repos/plugin" "$STATE_ROOT/retained-multi-plugin-source.git-state" \
    "$PROJECTS_ROOT/multi-project/repos/plugin" "$STATE_ROOT/retained-multi-plugin-target.git-state" \
    >"$checks"
  while IFS=$'\t' read -r repository evidence; do
    current="$(mktemp "$CONTROL_DIR/git-current.XXXXXX")"
    snapshot_repository "$repository" "$current"
    cmp -s "$current" "$evidence" || fail "retained Git state changed: $repository"
    find "$current" -delete
    git -C "$repository" fsck --full --strict >/dev/null
  done <"$checks"
  find "$checks" -delete

  current="$(mktemp "$CONTROL_DIR/submodule-current.XXXXXX")"
  git -C "$SOURCE_ROOT/multi-project/repos/core" submodule status --recursive >"$current"
  cmp -s "$current" "$STATE_ROOT/retained-multi-core-source.submodule-state" \
    || fail 'retained source submodule state changed'
  git -C "$PROJECTS_ROOT/multi-project/repos/core" submodule status --recursive >"$current"
  cmp -s "$current" "$STATE_ROOT/retained-multi-core-target.submodule-state" \
    || fail 'retained target submodule state changed'
  find "$current" -delete
  git -C "$SOURCE_ROOT/multi-project/repos/core/vendor/shared" fsck --full --strict >/dev/null
  git -C "$PROJECTS_ROOT/multi-project/repos/core/vendor/shared" fsck --full --strict >/dev/null
}

verify_retained_trees() {
  LOOM_DIGITAL_ESTATE_PILOT_ROOT="$STATE_ROOT" \
    go test -count=1 -run '^TestDigitalEstateProjectPilotRetainedTrees$' ./internal/cloudstorage
}

assert_orca_ready() {
  local status_file="$1"
  local hosts_file="$2"
  orca status --json >"$status_file"
  jq -e '.ok == true and .result.runtime.state == "ready" and .result.runtime.reachable == true' \
    "$status_file" >/dev/null || fail 'supported local ORCA runtime evidence is unavailable'
  orca host list --json >"$hosts_file"
  jq -e '.ok == true and any(.result.hosts[]; .id == "local" and .kind == "local")' \
    "$hosts_file" >/dev/null || fail 'ORCA did not expose the local host selector'
}

query_orca_inventory() {
  local prefix="$1"
  local raw_projects="$CONTROL_DIR/$prefix-projects.raw.json"
  local raw_setups="$CONTROL_DIR/$prefix-setups.raw.json"
  local raw_worktrees="$CONTROL_DIR/$prefix-worktrees.raw.json"
  orca project list --json >"$raw_projects"
  orca project setups --host local --json >"$raw_setups"
  orca worktree list --json >"$raw_worktrees"
  jq -e '.ok == true and (.result.projects | type == "array")' "$raw_projects" >/dev/null \
    || fail 'ORCA project inventory failed'
  jq -e '.ok == true and (.result.setups | type == "array")' "$raw_setups" >/dev/null \
    || fail 'ORCA project setup inventory failed'
  jq -e '.ok == true and ((.result.worktrees // []) | type == "array")' "$raw_worktrees" >/dev/null \
    || fail 'ORCA worktree inventory failed'

  local single="$PROJECTS_ROOT/single-project"
  local multi="$PROJECTS_ROOT/multi-project"
  jq -S --arg single "$single" --arg multi "$multi" '
    {ok:.ok,exact_path_matches:[.result.projects[]? |
      select(([.. | strings | select(. == $single or . == $multi)] | length) > 0)]}
  ' "$raw_projects" >"$STATE_ROOT/$prefix-projects.json"
  jq -S --arg single "$single" --arg multi "$multi" '
    {ok:.ok,exact_path_matches:[.result.setups[]? |
      select(([.. | strings | select(. == $single or . == $multi)] | length) > 0)]}
  ' "$raw_setups" >"$STATE_ROOT/$prefix-setups.json"
  jq -S --arg single "$single" --arg multi "$multi" '
    {ok:.ok,exact_path_matches:[(.result.worktrees // .result.items // [])[]? |
      select(
        ((.path // .worktreePath // .worktree_path // "") == $single) or
        ((.path // .worktreePath // .worktree_path // "") == $multi) or
        ((.path // .worktreePath // .worktree_path // "") | startswith($single + "/")) or
        ((.path // .worktreePath // .worktree_path // "") | startswith($multi + "/")) or
        ([.. | strings | select(. == $single or . == $multi)] | length) > 0
      )]}
  ' "$raw_worktrees" >"$STATE_ROOT/$prefix-worktrees.json"
  find "$raw_projects" "$raw_setups" "$raw_worktrees" -delete
}

assert_orca_paths_absent() {
  local prefix="$1"
  local single="$PROJECTS_ROOT/single-project"
  local multi="$PROJECTS_ROOT/multi-project"
  jq -e '.ok == true and (.exact_path_matches | length) == 0' "$STATE_ROOT/$prefix-projects.json" >/dev/null \
    || fail 'an accepted project path is already present in ORCA projects'
  jq -e '.ok == true and (.exact_path_matches | length) == 0' "$STATE_ROOT/$prefix-setups.json" >/dev/null \
    || fail 'an accepted project path is already present in ORCA setups'
  jq -e '.ok == true and (.exact_path_matches | length) == 0' "$STATE_ROOT/$prefix-worktrees.json" >/dev/null \
    || fail 'an accepted project workspace is already present in ORCA'
}

acceptance_evidence_files() {
  find "$STATE_ROOT" -maxdepth 1 -type f \
    \( -name '*.json' -o -name '*.git-state' -o -name '*.submodule-state' \) \
    ! -name 'acceptance-state.json' \
    ! -name 'orca-verification-state.json' \
    ! -name '*evidence-manifest.json' \
    | LC_ALL=C sort
}

write_acceptance_receipt() {
  local manifest="$STATE_ROOT/acceptance-evidence-manifest.json"
  local files=()
  while IFS= read -r file; do
    files+=("$file")
  done < <(acceptance_evidence_files)
  ((${#files[@]} > 0)) || fail 'no bounded acceptance evidence was produced'
  build_evidence_manifest "$manifest" "${files[@]}"

  local unsigned prepared_at prepared_epoch key_id marker_digest manifest_digest harness_digest
  unsigned="$(mktemp "$CONTROL_DIR/acceptance-unsigned.XXXXXX")"
  prepared_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  prepared_epoch="$(date -u +%s)"
  key_id="sha256:$(sha256_file "$HMAC_KEY")"
  marker_digest="sha256:$(sha256_file "$MARKER_FILE")"
  manifest_digest="sha256:$(sha256_file "$manifest")"
  harness_digest="sha256:$(sha256_file "$STATE_ROOT/harness-manifest.json")"
  jq -n -S \
    --arg state_id "$STATE_ID" \
    --arg state_root "$STATE_ROOT" \
    --arg prepared_at "$prepared_at" \
    --argjson prepared_epoch "$prepared_epoch" \
    --arg marker_digest "$marker_digest" \
    --arg manifest_digest "$manifest_digest" \
    --arg harness_digest "$harness_digest" \
    --arg key_id "$key_id" \
    --arg single_source "$SOURCE_ROOT/single-project" \
    --arg multi_source "$SOURCE_ROOT/multi-project" \
    --arg single_path "$PROJECTS_ROOT/single-project" \
    --arg multi_path "$PROJECTS_ROOT/multi-project" '
      {
        schema_version:"loom.digital_estate_project_acceptance_state.v1",
        phase:"prepare",
        state_id:$state_id,
        state_root:$state_root,
        prepared_at:$prepared_at,
        prepared_at_epoch:$prepared_epoch,
        state_marker_sha256:$marker_digest,
        accepted_projects:[
          {
            key:"single-project",source_path:$single_source,path:$single_path,
            loom_project_id:"project_01ARZ3NDEKTSV4RRFFQ69G6AAA",
            repository_ids:["repo_01ARZ3NDEKTSV4RRFFQ69G6AAB"]
          },
          {
            key:"multi-project",source_path:$multi_source,path:$multi_path,
            loom_project_id:"project_01ARZ3NDEKTSV4RRFFQ69G6AAC",
            repository_ids:["repo_01ARZ3NDEKTSV4RRFFQ69G6AAD","repo_01ARZ3NDEKTSV4RRFFQ69G6AAE"]
          }
        ],
        gates:{
          source_stability:"passed",git_integrity:"passed",git_state_equivalence:"passed",
          reviewed_project_to_repo:"passed",loom_registration:"passed",
          repository_membership:"passed",bounded_provenance:"passed",
          backup_coverage:"passed",isolated_fetch_restore:"passed",
          pre_orca_absence:"passed"
        },
        services:{loomd:"stopped",postgresql:"stopped"},
        orca:{host_id:"local",registration_matches_before_acceptance:0,workspace_matches_before_acceptance:0},
        harness_manifest:{path:"harness-manifest.json",sha256:$harness_digest},
        evidence_manifest:{path:"acceptance-evidence-manifest.json",sha256:$manifest_digest},
        authentication:{algorithm:"hmac-sha256",key_id:$key_id}
      }
    ' >"$unsigned"
  write_authenticated_json "$unsigned" "$ACCEPTANCE_RECEIPT"
  find "$unsigned" -delete
}

print_prepare_handoff() {
  printf 'PREPARE_COMPLETE=true\n'
  printf 'STATE_DIRECTORY=%s\n' "$STATE_ROOT"
  printf 'ACCEPTANCE_RECEIPT=%s\n' "$ACCEPTANCE_RECEIPT"
  printf 'ACCEPTANCE_RECEIPT_SHA256=sha256:%s\n' "$(sha256_file "$ACCEPTANCE_RECEIPT")"
  printf 'ACCEPTED_SINGLE_PROJECT=%s\n' "$PROJECTS_ROOT/single-project"
  printf 'ACCEPTED_MULTI_PROJECT=%s\n' "$PROJECTS_ROOT/multi-project"
  printf 'PRE_ORCA_REGISTRATION_MATCHES=0\n'
  printf 'PRE_ORCA_WORKSPACE_MATCHES=0\n'
}

prepare_replay() {
  load_state_identity
  verify_authenticated_receipt "$ACCEPTANCE_RECEIPT" \
    loom.digital_estate_project_acceptance_state.v1 prepare
  verify_retained_trees
  verify_retained_git_evidence
  assert_orca_ready "$CONTROL_DIR/replay-orca-status.json" "$CONTROL_DIR/replay-orca-hosts.json"
  query_orca_inventory replay-orca
  assert_orca_paths_absent replay-orca
  find "$STATE_ROOT/replay-orca-projects.json" "$STATE_ROOT/replay-orca-setups.json" \
    "$STATE_ROOT/replay-orca-worktrees.json" -delete
  pass 'authenticated prepare receipt replayed with unchanged estate and pre-ORCA absence'
  print_prepare_handoff
}

prepare_phase() {
  if [[ -f "$ACCEPTANCE_RECEIPT" ]]; then
    prepare_replay
    return
  fi
  initialize_state
  [[ -d "$FIXTURES" ]] || fail "project pilot fixtures are missing: $FIXTURES"
  [[ -d "$PACK_SOURCE/templates/.repo" ]] || fail 'repository development pack is missing'

  log 'building LOOM binaries inside the caller-supplied smoke state'
  mkdir -p "$STATE_ROOT/bin" "$SOURCE_ROOT" "$PROJECTS_ROOT" "$RUNTIME_ROOT/config"
  go build -o "$LOOM_BIN" ./cmd/loom
  go build -o "$LOOMD_BIN" ./cmd/loomd
  pass 'LOOM CLI and daemon binaries built outside the repository'

  log 'creating the disposable single-repository estate'
  cp -R "$FIXTURES/single-project" "$SOURCE_ROOT/single-project"
  mkdir -p "$SOURCE_ROOT/single-project/.loom/agent-packs"
  cp -R "$PACK_SOURCE" "$SOURCE_ROOT/single-project/.loom/agent-packs/repo-development"
  local single_repo="$SOURCE_ROOT/single-project/repos/application"
  init_repository "$single_repo"
  printf '%s\n' '# Single application' >"$single_repo/README.md"
  printf '%s\n' 'node_modules/' >"$single_repo/.gitignore"
  mkdir -p "$single_repo/docs"
  printf '%s\n' 'single pilot guide' >"$single_repo/docs/guide.md"
  ln -s guide.md "$single_repo/docs/current"
  git -C "$single_repo" add .
  git_commit "$single_repo" 'fixture: initialize legacy single repository'
  git -C "$single_repo" switch -q -c feature/preserved-branch
  printf '%s\n' 'preserved feature branch' >"$single_repo/feature.txt"
  git -C "$single_repo" add feature.txt
  git_commit "$single_repo" 'fixture: add preserved branch'
  git -C "$single_repo" switch -q main
  git -C "$single_repo" tag migration-pilot-v1

  local single_project_state_before
  single_project_state_before="$(git -C "$single_repo" hash-object .project/STATE.md)"
  local migration_flags=(
    --path "$single_repo"
    --pack "$SOURCE_ROOT/single-project/.loom/agent-packs/repo-development"
    --repository-id repo_01ARZ3NDEKTSV4RRFFQ69G6AAB
    --repository-name single-application
    --role primary
    --purpose 'Exercise reviewed disposable repository migration.'
    --owner-project-id project_01ARZ3NDEKTSV4RRFFQ69G6AAA
    --owner-project-slug digital-estate-single
    --stable-branch main
    --default-branch main
    --topic migration
    --topic disposable
  )
  "$LOOM_BIN" --json --no-animation --no-color repo-state migration-plan \
    "${migration_flags[@]}" >"$STATE_ROOT/repo-state-plan.json"
  jq -e '
    .kind == "migrate_project_state" and .apply_allowed == true and .counts.conflicting == 0 and
    any(.files[]; .source == ".project/PROJECT.md" and .disposition == "renamed") and
    any(.files[]; .source == ".project/STATE.md" and .disposition == "preserved")
  ' "$STATE_ROOT/repo-state-plan.json" >/dev/null || fail 'reviewed .project migration plan was not bounded and applyable'
  local migration_digest
  migration_digest="$(jq -er '.digest' "$STATE_ROOT/repo-state-plan.json")"
  "$LOOM_BIN" --json --no-animation --no-color repo-state migration-plan \
    "${migration_flags[@]}" --apply --plan-digest "$migration_digest" --yes \
    >"$STATE_ROOT/repo-state-apply.json"
  jq -e '.applied == true and .staged == false and .commit_created == false' \
    "$STATE_ROOT/repo-state-apply.json" >/dev/null || fail 'reviewed .project migration apply overclaimed Git effects'
  [[ -f "$single_repo/.repo/repo.yaml" ]] || fail 'reviewed migration did not create .repo/repo.yaml'
  [[ "$(git -C "$single_repo" hash-object .project/STATE.md)" == "$single_project_state_before" ]] \
    || fail 'reviewed migration changed the .project source'
  git -C "$single_repo" add .repo
  git_commit "$single_repo" 'fixture: accept reviewed repository state migration'
  printf '%s\n' 'dirty source change' >>"$single_repo/README.md"
  printf '%s\n' 'untracked source evidence' >"$single_repo/local-scratch.txt"
  mkdir -p "$single_repo/node_modules/disposable-dependency"
  printf '%s\n' 'reconstructible dependency' >"$single_repo/node_modules/disposable-dependency/index.js"
  "$LOOM_BIN" --json --no-animation --no-color repo-state validate \
    --path "$single_repo" \
    --repository-id repo_01ARZ3NDEKTSV4RRFFQ69G6AAB \
    --owner-project-id project_01ARZ3NDEKTSV4RRFFQ69G6AAA \
    --owner-project-slug digital-estate-single \
    --role primary >"$STATE_ROOT/repo-state-validate.json"
  jq -e '.tracking_status == "valid" and .freshness.tracked_state == "clean" and (.accepted_context | length) == 0' \
    "$STATE_ROOT/repo-state-validate.json" >/dev/null || fail 'reviewed .repo state did not validate against authoritative membership'
  pass 'single estate has reviewed .project to .repo migration, branches, tag, symlink, dirt, untracked state, and ignored dependency'

  log 'creating the disposable multi-repository estate with a submodule'
  cp -R "$FIXTURES/multi-project" "$SOURCE_ROOT/multi-project"
  local dependency_repo="$STATE_ROOT/submodule-source"
  init_repository "$dependency_repo"
  printf '%s\n' 'shared dependency payload' >"$dependency_repo/shared.txt"
  git -C "$dependency_repo" add shared.txt
  git_commit "$dependency_repo" 'fixture: initialize shared dependency'

  local multi_core="$SOURCE_ROOT/multi-project/repos/core"
  init_repository "$multi_core"
  printf '%s\n' '# Multi core' >"$multi_core/README.md"
  mkdir -p "$multi_core/docs"
  printf '%s\n' 'core guide' >"$multi_core/docs/guide.md"
  ln -s guide.md "$multi_core/docs/current"
  git -C "$multi_core" add .
  git_commit "$multi_core" 'fixture: initialize core repository'
  git -C "$multi_core" switch -q -c feature/core-preserved
  printf '%s\n' 'core feature' >"$multi_core/core-feature.txt"
  git -C "$multi_core" add core-feature.txt
  git_commit "$multi_core" 'fixture: add core feature branch'
  git -C "$multi_core" switch -q main
  git -C "$multi_core" tag core-pilot-v1
  git -C "$multi_core" -c protocol.file.allow=always submodule add -q "$dependency_repo" vendor/shared
  git -C "$multi_core/vendor/shared" repack -a -d
  git -C "$multi_core" add .gitmodules vendor/shared
  git_commit "$multi_core" 'fixture: add shared submodule'
  printf '%s\n' 'dirty core source change' >>"$multi_core/README.md"
  printf '%s\n' 'untracked core evidence' >"$multi_core/core-scratch.txt"

  local multi_plugin="$SOURCE_ROOT/multi-project/repos/plugin"
  init_repository "$multi_plugin"
  printf '%s\n' '# Multi plugin' >"$multi_plugin/README.md"
  printf '%s\n' 'node_modules/' >"$multi_plugin/.gitignore"
  printf '%s\n' 'plugin implementation' >"$multi_plugin/plugin.txt"
  ln -s plugin.txt "$multi_plugin/current-plugin"
  git -C "$multi_plugin" add .
  git_commit "$multi_plugin" 'fixture: initialize plugin repository'
  git -C "$multi_plugin" switch -q -c feature/plugin-preserved
  printf '%s\n' 'plugin feature' >"$multi_plugin/plugin-feature.txt"
  git -C "$multi_plugin" add plugin-feature.txt
  git_commit "$multi_plugin" 'fixture: add plugin feature branch'
  git -C "$multi_plugin" switch -q main
  git -C "$multi_plugin" tag plugin-pilot-v1
  printf '%s\n' 'dirty plugin source change' >>"$multi_plugin/README.md"
  printf '%s\n' 'untracked plugin evidence' >"$multi_plugin/plugin-scratch.txt"
  mkdir -p "$multi_plugin/node_modules/disposable-plugin"
  printf '%s\n' 'ignored plugin dependency' >"$multi_plugin/node_modules/disposable-plugin/index.js"
  pass 'multi estate has two repositories, submodule, branches, tags, symlinks, dirt, untracked state, and ignored dependencies'

  log 'running the reviewed inventory, manifest, staging, verification, and atomic publication path'
  LOOM_DIGITAL_ESTATE_PILOT_ROOT="$STATE_ROOT" \
    go test -count=1 -run '^TestDigitalEstateProjectPilot$' ./internal/estatemigration
  jq -e '
    .schema_version == "loom.digital_estate_project_pilot.v1" and
    (.pilots | length) == 2 and
    all(.pilots[];
      .publication_status == "published_pending_acceptance" and
      .dirty_repositories > 0 and .untracked_entries > 0 and
      .tag_refs > 0 and .branch_refs >= 2 and .symlinks > 0 and .ignored_entries > 0) and
    any(.pilots[]; .name == "multi-repository" and .repository_count >= 3 and .submodules > 0)
  ' "$STATE_ROOT/publication-evidence.json" >/dev/null || fail 'publication evidence did not cover the declared estate matrix'
  [[ ! -e "$PROJECTS_ROOT/single-project/repos/application/node_modules" ]] || fail 'ignored single-project dependency was published'
  [[ ! -e "$PROJECTS_ROOT/multi-project/repos/plugin/node_modules" ]] || fail 'ignored multi-project dependency was published'
  [[ -d "$PROJECTS_ROOT/single-project/repos/application/.git" ]] || fail 'single-project Git object database was not preserved'
  [[ -f "$PROJECTS_ROOT/multi-project/repos/core/vendor/shared/.git" ]] || fail 'submodule Git link was not preserved'
  [[ "$(readlink "$PROJECTS_ROOT/single-project/repos/application/docs/current")" == guide.md ]] || fail 'single-project relative symlink changed'
  [[ "$(readlink "$PROJECTS_ROOT/multi-project/repos/plugin/current-plugin")" == plugin.txt ]] || fail 'multi-project relative symlink changed'
  pass 'LOOM inventory, manifest, publisher, ignored-entry, and symlink gates passed'

  log 'verifying Git object databases, refs, HEAD, and working-tree manifests'
  compare_repository "$single_repo" "$PROJECTS_ROOT/single-project/repos/application" single
  compare_repository "$multi_core" "$PROJECTS_ROOT/multi-project/repos/core" multi-core
  compare_repository "$multi_plugin" "$PROJECTS_ROOT/multi-project/repos/plugin" multi-plugin
  git -C "$PROJECTS_ROOT/multi-project/repos/core/vendor/shared" fsck --full --strict >/dev/null
  cmp -s <(git -C "$multi_core" submodule status --recursive) \
    <(git -C "$PROJECTS_ROOT/multi-project/repos/core" submodule status --recursive) \
    || fail 'submodule commit or state changed during publication'
  [[ -d "$single_repo/.project" && -d "$PROJECTS_ROOT/single-project/repos/application/.project" ]] \
    || fail '.project source was not retained alongside reviewed .repo output'
  pass 'source and accepted Git states match, fsck passes, and legacy project state remains retained'

  log 'booting a disposable LOOM runtime for canonical registration and provenance'
  resolve_postgres
  start_postgres
  MAIN_DB_URL="postgres://postgres@/loom_main?host=$PG_SOCKET&sslmode=disable"
  PROVENANCE_DB_URL="postgres://loom_provenance@/loom_provenance?host=$PG_SOCKET&sslmode=disable"
  start_daemon
  loom box init --path "$RUNTIME_ROOT/box" --profile main >"$STATE_ROOT/box-init.json"
  jq -e '.status_after.state == "ok" and .status_after.initialized == true' "$STATE_ROOT/box-init.json" >/dev/null
  pass 'disposable PostgreSQL, provenance database, daemon, and Box are ready'

  log 'accepting stable LOOM project and repository identities before ORCA'
  local project project_path project_slug project_id member_count
  for project in single multi; do
    if [[ "$project" == single ]]; then
      project_path="$PROJECTS_ROOT/single-project"
      project_slug=digital-estate-single
      project_id=project_01ARZ3NDEKTSV4RRFFQ69G6AAA
      member_count=1
    else
      project_path="$PROJECTS_ROOT/multi-project"
      project_slug=digital-estate-multi
      project_id=project_01ARZ3NDEKTSV4RRFFQ69G6AAC
      member_count=2
    fi
    if ! loom project validate "$project_path" >"$STATE_ROOT/$project-validate.json"; then
      jq . "$STATE_ROOT/$project-validate.json" >&2 || true
      tail -n 120 "$STATE_ROOT/loomd.log" >&2
      fail "$project project validation command failed"
    fi
    loom project plan "$project_path" >"$STATE_ROOT/$project-plan.json"
    jq -e --argjson count "$member_count" '.ok == true and .registerable == true and (.repository_members | length) == $count' \
      "$STATE_ROOT/$project-validate.json" >/dev/null || fail "$project project did not validate with exact membership"
    loom project register "$project_path" --backend \
      --idempotency-key "digital-estate-$project-register" >"$STATE_ROOT/$project-register.json"
    jq -e --arg id "$project_id" --argjson count "$member_count" '
      .ok == true and .data.created == true and
      .data.detail.project.project.project_id == $id and
      .data.repository_source.member_count == $count
    ' "$STATE_ROOT/$project-register.json" >/dev/null || fail "$project project registration identity changed"
    loom project register "$project_path" --backend \
      --idempotency-key "digital-estate-$project-register-replay" >"$STATE_ROOT/$project-register-replay.json"
    jq -e --arg id "$project_id" '
      .ok == true and .data.unchanged == true and
      .data.detail.project.project.project_id == $id and
      .data.repository_source.classification == "identical_replay"
    ' "$STATE_ROOT/$project-register-replay.json" >/dev/null || fail "$project registration replay changed stable identity"
    loom project repos list "$project_slug" >"$STATE_ROOT/$project-repos.json"
    jq -e --argjson count "$member_count" '.ok == true and (.data.repositories | length) == $count' \
      "$STATE_ROOT/$project-repos.json" >/dev/null || fail "$project membership did not persist"
  done

  loom project repos inspect digital-estate-single repo_01ARZ3NDEKTSV4RRFFQ69G6AAB \
    >"$STATE_ROOT/single-repo-inspect.json"
  jq -e '
    .ok == true and
    .data.repository.repository_id == "repo_01ARZ3NDEKTSV4RRFFQ69G6AAB" and
    .data.repository.relative_path == "application" and
    .data.repository.git.dirty.dirty == true and
    .data.repository.git.dirty.untracked_changes == true
  ' "$STATE_ROOT/single-repo-inspect.json" >/dev/null || fail 'single repository identity or dirty state was not preserved'
  loom project repos status digital-estate-multi >"$STATE_ROOT/multi-repo-status.json"
  jq -e '
    .ok == true and .data.observation.member_count == 2 and .data.observation.observed == 2 and
    all(.data.repositories[]; .git.dirty.dirty == true and .git.dirty.untracked_changes == true)
  ' "$STATE_ROOT/multi-repo-status.json" >/dev/null || fail 'multi-repository membership or dirty state was not preserved'
  pass 'LOOM canonical registration, idempotent identity, membership, and Git posture passed'

  log 'extracting only bounded repository provenance from the accepted projects'
  loom provenance repo sync digital-estate-single \
    --idempotency-key digital-estate-single-provenance >"$STATE_ROOT/single-provenance-sync.json"
  loom provenance repo sync digital-estate-multi \
    --idempotency-key digital-estate-multi-provenance >"$STATE_ROOT/multi-provenance-sync.json"
  jq -e '.ok == true and .data.receipt.project_id == "project_01ARZ3NDEKTSV4RRFFQ69G6AAA" and (.data.receipt.repositories | length) == 1' \
    "$STATE_ROOT/single-provenance-sync.json" >/dev/null || fail 'single-project provenance sync was not bounded to one member'
  jq -e '.ok == true and .data.receipt.project_id == "project_01ARZ3NDEKTSV4RRFFQ69G6AAC" and (.data.receipt.repositories | length) == 2' \
    "$STATE_ROOT/multi-provenance-sync.json" >/dev/null || fail 'multi-project provenance sync was not bounded to two members'
  loom provenance repo get repo_01ARZ3NDEKTSV4RRFFQ69G6AAB >"$STATE_ROOT/single-provenance-get.json"
  jq -e '
    .ok == true and .data.repository_id == "repo_01ARZ3NDEKTSV4RRFFQ69G6AAB" and
    .data.owning_project.project_id == "project_01ARZ3NDEKTSV4RRFFQ69G6AAA" and
    .data.tracking_status == "valid" and (.data.accepted_context | length) == 0
  ' "$STATE_ROOT/single-provenance-get.json" >/dev/null || fail 'bounded provenance projection overclaimed accepted context or changed identity'
  loom provenance repo list --project digital-estate-multi --limit 2 >"$STATE_ROOT/multi-provenance-list.json"
  jq -e '.ok == true and .data.returned == 2 and (.data.items | length) == 2 and .data.truncated == false' \
    "$STATE_ROOT/multi-provenance-list.json" >/dev/null || fail 'bounded multi-project provenance listing failed'
  pass 'bounded project/repository provenance extraction and exact reads passed'

  log 'proving backup coverage and isolated fetch/restore before ORCA registration'
  LOOM_DIGITAL_ESTATE_PILOT_ROOT="$STATE_ROOT" \
    go test -count=1 -run '^TestDigitalEstateProjectPilotBackupFetchRestore$' ./internal/cloudstorage
  jq -e '
    .schema_version == "loom.digital_estate_project_backup_pilot.v1" and
    (.coverage_status == "ok" or .coverage_status == "warning") and
    .project_coverage_status == "covered" and .critical_missing == 0 and
    .snapshot_status == "succeeded" and
    .fetch_status == "succeeded" and (.project_digest | startswith("sha256:"))
  ' "$STATE_ROOT/backup-evidence.json" >/dev/null || fail 'backup coverage or isolated fetch/restore evidence failed'
  compare_repository "$single_repo" "$STATE_ROOT/isolated-restore/main-documents/single-project/repos/application" restored-single
  compare_repository "$multi_core" "$STATE_ROOT/isolated-restore/main-documents/multi-project/repos/core" restored-multi-core
  git -C "$STATE_ROOT/isolated-restore/main-documents/multi-project/repos/core/vendor/shared" fsck --full --strict >/dev/null
  pass 'existing snapshot abstraction preserved the full accepted estate in an isolated restore'

  LOOM_DIGITAL_ESTATE_PILOT_ROOT="$STATE_ROOT" LOOM_DIGITAL_ESTATE_RECORD_TREE_EVIDENCE=1 \
    go test -count=1 -run '^TestDigitalEstateProjectPilotRetainedTrees$' ./internal/cloudstorage
  record_retained_git_evidence
  verify_retained_git_evidence
  pass 'retained source, target, restore, Git, and submodule identities recorded'

  log 'stopping disposable services before publishing acceptance'
  stop_services || fail 'disposable LOOM/PostgreSQL services did not stop cleanly'
  [[ -z "$DAEMON_PID" && "$POSTGRES_STARTED" == false ]] || fail 'disposable services remain active'
  pass 'disposable services stopped; state is confined to the caller-supplied directory'

  log 'proving exact accepted project roots are absent from ORCA before acceptance'
  assert_orca_ready "$STATE_ROOT/pre-orca-status.json" "$STATE_ROOT/pre-orca-hosts.json"
  query_orca_inventory pre-orca
  assert_orca_paths_absent pre-orca
  pass 'no accepted project registration or workspace exists before acceptance'

  build_harness_manifest
  write_acceptance_receipt
  verify_authenticated_receipt "$ACCEPTANCE_RECEIPT" \
    loom.digital_estate_project_acceptance_state.v1 prepare
  pass 'authenticated bounded acceptance-state receipt published'

  log "prepare completed with $PASS_COUNT checks; waiting for the two reviewed ORCA Browse folder actions"
  print_prepare_handoff
}

normalize_orca_projects() {
  jq -S '
    [
      .result.projects[]? |
      {
        project_id:(.id // .projectId // .project_id // .project.id // ""),
        kind:(.kind // .projectKind // .project_kind // ""),
        name:(.name // .displayName // .display_name // .title // "")
      }
    ]
  ' "$1" >"$2"
}

normalize_orca_setups() {
  jq -S '
    [
      .result.setups[]? |
      {
        setup_id:(.id // .setupId // .setup_id // ""),
        project_id:(.projectId // .project_id // .project.id // .projectRef // .project_ref // ""),
        path:(.path // .rootPath // .root_path // ""),
        host_id:(.hostId // .host_id // .host.id // ""),
        kind:(.kind // .setupKind // .setup_kind // ""),
        state:(.state // .status // ""),
        method:(.method // .setupMethod // .setup_method // "")
      }
    ]
  ' "$1" >"$2"
}

normalize_orca_worktrees() {
  jq -S '
    [
      (.result.worktrees // .result.items // [])[]? |
      {
        worktree_id:(.id // .worktreeId // .worktree_id // ""),
        instance_id:(.instanceId // .instance_id // ""),
        project_id:(.projectId // .project_id // .project.id // ""),
        setup_id:(.projectHostSetupId // .project_host_setup_id // .setupId // .setup_id // ""),
        repo_id:(.repoId // .repo_id // .repo.id // ""),
        host_id:(.hostId // .host_id // .host.id // ""),
        path:(.path // .worktreePath // .worktree_path // ""),
        name:(.name // .displayName // .display_name // ""),
        created_at:(.createdAt // .created_at // ""),
        is_bare:(.isBare // .is_bare // false),
        is_main_worktree:(.isMainWorktree // .is_main_worktree // false),
        is_archived:(.isArchived // .is_archived // false)
      }
    ]
  ' "$1" >"$2"
}

discover_orca_folder_projects() {
  local raw_projects="$CONTROL_DIR/verify-orca-projects.raw.json"
  local raw_setups="$CONTROL_DIR/verify-orca-setups.raw.json"
  local all_projects="$CONTROL_DIR/verify-orca-projects-all-normalized.json"
  local all_setups="$CONTROL_DIR/verify-orca-setups-all-normalized.json"
  normalize_orca_projects "$raw_projects" "$all_projects"
  normalize_orca_setups "$raw_setups" "$all_setups"
  jq -S --arg single "$PROJECTS_ROOT/single-project" --arg multi "$PROJECTS_ROOT/multi-project" \
    '[.[] | select(.path == $single or .path == $multi)]' "$all_setups" \
    >"$STATE_ROOT/verify-orca-setups-normalized.json"
  jq -e --arg single "$PROJECTS_ROOT/single-project" --arg multi "$PROJECTS_ROOT/multi-project" '
    ([.[] | select(.path == $single or .path == $multi)] | length) == 2 and
    ([.[] | select(.path == $single)] | length) == 1 and
    ([.[] | select(.path == $multi)] | length) == 1 and
    all(.[] | select(.path == $single or .path == $multi);
      (.setup_id | length) > 0 and (.project_id | length) > 0 and
      .host_id == "local" and .kind == "folder") and
    ([.[] | select(.path == $single or .path == $multi) | .setup_id] | unique | length) == 2 and
    ([.[] | select(.path == $single or .path == $multi) | .project_id] | unique | length) == 2
  ' "$STATE_ROOT/verify-orca-setups-normalized.json" >/dev/null \
    || fail 'ORCA did not expose exactly one local folder setup for each accepted project path'

  local single_project_id multi_project_id single_setup_id multi_setup_id
  single_project_id="$(jq -er --arg path "$PROJECTS_ROOT/single-project" '.[] | select(.path == $path) | .project_id' "$STATE_ROOT/verify-orca-setups-normalized.json")"
  multi_project_id="$(jq -er --arg path "$PROJECTS_ROOT/multi-project" '.[] | select(.path == $path) | .project_id' "$STATE_ROOT/verify-orca-setups-normalized.json")"
  single_setup_id="$(jq -er --arg path "$PROJECTS_ROOT/single-project" '.[] | select(.path == $path) | .setup_id' "$STATE_ROOT/verify-orca-setups-normalized.json")"
  multi_setup_id="$(jq -er --arg path "$PROJECTS_ROOT/multi-project" '.[] | select(.path == $path) | .setup_id' "$STATE_ROOT/verify-orca-setups-normalized.json")"

  jq -S --arg single "$single_project_id" --arg multi "$multi_project_id" \
    '[.[] | select(.project_id == $single or .project_id == $multi)]' "$all_projects" \
    >"$STATE_ROOT/verify-orca-projects-normalized.json"
  jq -S --arg single "$single_project_id" --arg multi "$multi_project_id" '
    {ok:.ok,project_matches:[.result.projects[]? |
      select((.id // .projectId // .project_id // .project.id // "") == $single or
             (.id // .projectId // .project_id // .project.id // "") == $multi)]}
  ' "$raw_projects" >"$STATE_ROOT/verify-orca-projects.json"
  jq -n -S --slurpfile matches "$STATE_ROOT/verify-orca-setups-normalized.json" \
    '{ok:true,setup_matches:$matches[0]}' >"$STATE_ROOT/verify-orca-setups.json"

  jq -e --arg single "$single_project_id" --arg multi "$multi_project_id" '
    ([.[] | select(.project_id == $single)] | length) == 1 and
    ([.[] | select(.project_id == $multi)] | length) == 1
  ' "$STATE_ROOT/verify-orca-projects-normalized.json" >/dev/null \
    || fail 'ORCA project inventory did not bind both unique folder-project identities'

  orca project setups --project "$single_project_id" --host local --json \
    >"$STATE_ROOT/verify-orca-single-setups.json"
  orca project setups --project "$multi_project_id" --host local --json \
    >"$STATE_ROOT/verify-orca-multi-setups.json"
  normalize_orca_setups "$STATE_ROOT/verify-orca-single-setups.json" "$STATE_ROOT/verify-orca-single-setups-normalized.json"
  normalize_orca_setups "$STATE_ROOT/verify-orca-multi-setups.json" "$STATE_ROOT/verify-orca-multi-setups-normalized.json"
  jq -e --arg project "$single_project_id" --arg setup "$single_setup_id" --arg path "$PROJECTS_ROOT/single-project" '
    length == 1 and .[0].project_id == $project and .[0].setup_id == $setup and
    .[0].path == $path and .[0].host_id == "local" and .[0].kind == "folder"
  ' "$STATE_ROOT/verify-orca-single-setups-normalized.json" >/dev/null \
    || fail 'single-project folder identity is not one-to-one'
  jq -e --arg project "$multi_project_id" --arg setup "$multi_setup_id" --arg path "$PROJECTS_ROOT/multi-project" '
    length == 1 and .[0].project_id == $project and .[0].setup_id == $setup and
    .[0].path == $path and .[0].host_id == "local" and .[0].kind == "folder"
  ' "$STATE_ROOT/verify-orca-multi-setups-normalized.json" >/dev/null \
    || fail 'multi-project folder identity is not one-to-one'

  find "$raw_projects" "$raw_setups" "$all_projects" "$all_setups" -delete

  printf '%s\t%s\t%s\t%s\n' "$single_project_id" "$single_setup_id" "$multi_project_id" "$multi_setup_id"
}

capture_matching_worktrees() {
  local stem="$1"
  local single_project_id="$2"
  local single_setup_id="$3"
  local multi_project_id="$4"
  local multi_setup_id="$5"
  local unique_name="$6"
  local raw="$CONTROL_DIR/$stem.raw.json"
  local all="$CONTROL_DIR/$stem-all-normalized.json"
  orca worktree list --json >"$raw"
  jq -e '.ok == true and ((.result.worktrees // .result.items // []) | type == "array")' "$raw" >/dev/null \
    || fail 'ORCA worktree inventory failed'
  normalize_orca_worktrees "$raw" "$all"
  jq -S \
    --arg single_project "$single_project_id" --arg single_setup "$single_setup_id" \
    --arg multi_project "$multi_project_id" --arg multi_setup "$multi_setup_id" \
    --arg single_path "$PROJECTS_ROOT/single-project" --arg multi_path "$PROJECTS_ROOT/multi-project" \
    --arg unique_name "$unique_name" '
      [.[] | select(
        .project_id == $single_project or .project_id == $multi_project or
        .setup_id == $single_setup or .setup_id == $multi_setup or
        .repo_id == $single_setup or .repo_id == $multi_setup or
        .path == $single_path or .path == $multi_path or
        (.path | startswith($single_path + "/")) or
        (.path | startswith($multi_path + "/")) or
        .name == $unique_name
      )]
    ' "$all" >"$STATE_ROOT/$stem-normalized.json"
  jq -n -S --slurpfile matches "$STATE_ROOT/$stem-normalized.json" \
    '{ok:true,worktree_matches:$matches[0]}' >"$STATE_ROOT/$stem.json"
  find "$raw" "$all" -delete
}

assert_inherent_folder_workspaces() {
  local normalized="$1"
  local single_project_id="$2"
  local single_setup_id="$3"
  local multi_project_id="$4"
  local multi_setup_id="$5"
  jq -e \
    --arg single_project "$single_project_id" --arg single_setup "$single_setup_id" \
    --arg multi_project "$multi_project_id" --arg multi_setup "$multi_setup_id" \
    --arg single_path "$PROJECTS_ROOT/single-project" --arg multi_path "$PROJECTS_ROOT/multi-project" '
      def inherent($project; $setup; $path; $name):
        .worktree_id == ($setup + "::" + $path) and
        (.instance_id | type == "string" and length > 0) and
        .project_id == $project and .setup_id == $setup and .repo_id == $setup and
        .host_id == "local" and .path == $path and .name == $name and
        .is_bare == false and .is_main_worktree == true and .is_archived == false;
      length == 2 and
      ([.[] | select(inherent($single_project; $single_setup; $single_path; "single-project"))] | length) == 1 and
      ([.[] | select(inherent($multi_project; $multi_setup; $multi_path; "multi-project"))] | length) == 1 and
      ([.[].worktree_id] | unique | length) == 2 and
      ([.[].instance_id] | unique | length) == 2
    ' "$normalized" >/dev/null \
    || fail 'ORCA did not expose exactly one inherent main folder workspace for each accepted project'
}

assert_inherent_and_disposable_workspaces() {
  local normalized="$1"
  local single_project_id="$2"
  local single_setup_id="$3"
  local multi_project_id="$4"
  local multi_setup_id="$5"
  local worktree_id="$6"
  local worktree_path="$7"
  local worktree_name="$8"
  jq -e \
    --arg single_project "$single_project_id" --arg single_setup "$single_setup_id" \
    --arg multi_project "$multi_project_id" --arg multi_setup "$multi_setup_id" \
    --arg single_path "$PROJECTS_ROOT/single-project" --arg multi_path "$PROJECTS_ROOT/multi-project" \
    --arg worktree_id "$worktree_id" --arg worktree_path "$worktree_path" --arg worktree_name "$worktree_name" '
      def inherent($project; $setup; $path; $name):
        .worktree_id == ($setup + "::" + $path) and
        (.instance_id | type == "string" and length > 0) and
        .project_id == $project and .setup_id == $setup and .repo_id == $setup and
        .host_id == "local" and .path == $path and .name == $name and
        .is_bare == false and .is_main_worktree == true and .is_archived == false;
      def disposable:
        .worktree_id == $worktree_id and
        (.instance_id | type == "string" and length > 0) and
        .worktree_id == ($single_setup + "::" + $single_path + "::workspace:" + .instance_id) and
        .project_id == $single_project and .setup_id == $single_setup and .repo_id == $single_setup and
        .host_id == "local" and .path == $worktree_path and .name == $worktree_name and
        .is_bare == false and .is_main_worktree == false and .is_archived == false;
      length == 3 and
      ([.[] | select(inherent($single_project; $single_setup; $single_path; "single-project"))] | length) == 1 and
      ([.[] | select(inherent($multi_project; $multi_setup; $multi_path; "multi-project"))] | length) == 1 and
      ([.[] | select(disposable)] | length) == 1 and
      ([.[].worktree_id] | unique | length) == 3 and
      ([.[].instance_id] | unique | length) == 3
    ' "$normalized" >/dev/null \
    || fail 'ORCA workspace inventory was not exactly two inherent folder surfaces plus one disposable child'
}

assert_disposable_worktree_response() {
  local response="$1"
  local project_id="$2"
  local setup_id="$3"
  local worktree_id="$4"
  local instance_id="$5"
  local worktree_path="$6"
  local worktree_name="$7"
  jq -e \
    --arg project "$project_id" --arg setup "$setup_id" \
    --arg id "$worktree_id" --arg instance "$instance_id" \
    --arg path "$worktree_path" --arg name "$worktree_name" '
    .ok == true and
    ((.result.worktree // .result) as $w |
      ($w.id // $w.worktreeId // $w.worktree_id // "") == $id and
      ($w.instanceId // $w.instance_id // "") == $instance and
      ($w.projectId // $w.project_id // $w.project.id // "") == $project and
      ($w.projectHostSetupId // $w.project_host_setup_id // $w.setupId // $w.setup_id // "") == $setup and
      ($w.repoId // $w.repo_id // $w.repo.id // "") == $setup and
      ($w.hostId // $w.host_id // $w.host.id // "") == "local" and
      ($w.path // $w.worktreePath // $w.worktree_path // "") == $path and
      ($w.displayName // $w.display_name // $w.name // "") == $name and
      ($w.isBare // $w.is_bare // false) == false and
      ($w.isMainWorktree // $w.is_main_worktree // false) == false and
      ($w.isArchived // $w.is_archived // false) == false)
  ' "$response" >/dev/null
}

write_verification_receipt() {
  local single_project_id="$1"
  local single_setup_id="$2"
  local multi_project_id="$3"
  local multi_setup_id="$4"
  local worktree_id="$5"
  local worktree_path="$6"
  local worktree_name="$7"
  local create_started_epoch="$8"
  local manifest="$STATE_ROOT/orca-verification-evidence-manifest.json"
  local evidence_files=(
    "$STATE_ROOT/verify-orca-status.json"
    "$STATE_ROOT/verify-orca-hosts.json"
    "$STATE_ROOT/verify-orca-projects.json"
    "$STATE_ROOT/verify-orca-projects-normalized.json"
    "$STATE_ROOT/verify-orca-setups.json"
    "$STATE_ROOT/verify-orca-setups-normalized.json"
    "$STATE_ROOT/verify-orca-single-setups.json"
    "$STATE_ROOT/verify-orca-single-setups-normalized.json"
    "$STATE_ROOT/verify-orca-multi-setups.json"
    "$STATE_ROOT/verify-orca-multi-setups-normalized.json"
    "$STATE_ROOT/verify-orca-worktrees-before.json"
    "$STATE_ROOT/verify-orca-worktrees-before-normalized.json"
    "$STATE_ROOT/verify-orca-worktree-create.json"
    "$STATE_ROOT/verify-orca-worktree-show.json"
    "$STATE_ROOT/verify-orca-worktrees-created.json"
    "$STATE_ROOT/verify-orca-worktrees-created-normalized.json"
    "$STATE_ROOT/verify-orca-worktree-remove.json"
    "$STATE_ROOT/verify-orca-worktrees-after.json"
    "$STATE_ROOT/verify-orca-worktrees-after-normalized.json"
  )
  build_evidence_manifest "$manifest" "${evidence_files[@]}"

  local single_inherent_id single_inherent_instance multi_inherent_id multi_inherent_instance worktree_instance
  single_inherent_id="$(jq -er --arg project "$single_project_id" '.[] | select(.project_id == $project and .is_main_worktree == true) | .worktree_id' "$STATE_ROOT/verify-orca-worktrees-before-normalized.json")"
  single_inherent_instance="$(jq -er --arg project "$single_project_id" '.[] | select(.project_id == $project and .is_main_worktree == true) | .instance_id' "$STATE_ROOT/verify-orca-worktrees-before-normalized.json")"
  multi_inherent_id="$(jq -er --arg project "$multi_project_id" '.[] | select(.project_id == $project and .is_main_worktree == true) | .worktree_id' "$STATE_ROOT/verify-orca-worktrees-before-normalized.json")"
  multi_inherent_instance="$(jq -er --arg project "$multi_project_id" '.[] | select(.project_id == $project and .is_main_worktree == true) | .instance_id' "$STATE_ROOT/verify-orca-worktrees-before-normalized.json")"
  worktree_instance="$(jq -er --arg id "$worktree_id" '.[] | select(.worktree_id == $id and .is_main_worktree == false) | .instance_id' "$STATE_ROOT/verify-orca-worktrees-created-normalized.json")"

  local unsigned verified_at verified_epoch key_id marker_digest manifest_digest acceptance_digest
  unsigned="$(mktemp "$CONTROL_DIR/verification-unsigned.XXXXXX")"
  verified_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  verified_epoch="$(date -u +%s)"
  key_id="sha256:$(sha256_file "$HMAC_KEY")"
  marker_digest="sha256:$(sha256_file "$MARKER_FILE")"
  manifest_digest="sha256:$(sha256_file "$manifest")"
  acceptance_digest="sha256:$(sha256_file "$ACCEPTANCE_RECEIPT")"
  jq -n -S \
    --arg state_id "$STATE_ID" --arg state_root "$STATE_ROOT" \
    --arg verified_at "$verified_at" --argjson verified_epoch "$verified_epoch" \
    --argjson create_started_epoch "$create_started_epoch" \
    --arg marker_digest "$marker_digest" --arg manifest_digest "$manifest_digest" \
    --arg acceptance_digest "$acceptance_digest" --arg key_id "$key_id" \
    --arg single_path "$PROJECTS_ROOT/single-project" --arg multi_path "$PROJECTS_ROOT/multi-project" \
    --arg single_project "$single_project_id" --arg single_setup "$single_setup_id" \
    --arg multi_project "$multi_project_id" --arg multi_setup "$multi_setup_id" \
    --arg single_inherent_id "$single_inherent_id" --arg single_inherent_instance "$single_inherent_instance" \
    --arg multi_inherent_id "$multi_inherent_id" --arg multi_inherent_instance "$multi_inherent_instance" \
    --arg worktree_id "$worktree_id" --arg worktree_instance "$worktree_instance" \
    --arg worktree_path "$worktree_path" --arg worktree_name "$worktree_name" '
      {
        schema_version:"loom.digital_estate_project_orca_verification_state.v1",
        phase:"verify-orca",state_id:$state_id,state_root:$state_root,
        verified_at:$verified_at,verified_at_epoch:$verified_epoch,
        state_marker_sha256:$marker_digest,
        acceptance_receipt_sha256:$acceptance_digest,
        folder_projects:[
          {
            key:"single-project",path:$single_path,project_id:$single_project,setup_id:$single_setup,
            host_id:"local",kind:"folder",
            inherent_workspace:{
              id:$single_inherent_id,instance_id:$single_inherent_instance,path:$single_path,
              project_id:$single_project,setup_id:$single_setup,repo_id:$single_setup,
              host_id:"local",name:"single-project",is_bare:false,is_main_worktree:true,is_archived:false
            }
          },
          {
            key:"multi-project",path:$multi_path,project_id:$multi_project,setup_id:$multi_setup,
            host_id:"local",kind:"folder",
            inherent_workspace:{
              id:$multi_inherent_id,instance_id:$multi_inherent_instance,path:$multi_path,
              project_id:$multi_project,setup_id:$multi_setup,repo_id:$multi_setup,
              host_id:"local",name:"multi-project",is_bare:false,is_main_worktree:true,is_archived:false
            }
          }
        ],
        disposable_workspace:{
          id:$worktree_id,instance_id:$worktree_instance,path:$worktree_path,name:$worktree_name,
          project_id:$single_project,setup_id:$single_setup,repo_id:$single_setup,
          host_id:"local",is_main_worktree:false,path_mode:"shared-folder-overlay",
          create_started_epoch:$create_started_epoch,removed:true
        },
        evidence_manifest:{path:"orca-verification-evidence-manifest.json",sha256:$manifest_digest},
        authentication:{algorithm:"hmac-sha256",key_id:$key_id}
      }
    ' >"$unsigned"
  write_authenticated_json "$unsigned" "$VERIFICATION_RECEIPT"
  find "$unsigned" -delete
}

verify_verification_receipt() {
  verify_authenticated_receipt "$VERIFICATION_RECEIPT" \
    loom.digital_estate_project_orca_verification_state.v1 verify-orca
  [[ "$(jq -er '.acceptance_receipt_sha256' "$VERIFICATION_RECEIPT")" == "sha256:$(sha256_file "$ACCEPTANCE_RECEIPT")" ]] \
    || fail 'verification receipt no longer binds the authenticated acceptance receipt'
  jq -e \
    --arg single_path "$PROJECTS_ROOT/single-project" --arg multi_path "$PROJECTS_ROOT/multi-project" \
    --argjson accepted_epoch "$(jq -er '.prepared_at_epoch' "$ACCEPTANCE_RECEIPT")" '
      (.folder_projects | type == "array" and length == 2) and
      ([.folder_projects[] | select(.key == "single-project")] | length) == 1 and
      ([.folder_projects[] | select(.key == "multi-project")] | length) == 1 and
      all(.folder_projects[];
        .host_id == "local" and .kind == "folder" and
        (.project_id | type == "string" and length > 0) and
        (.setup_id | type == "string" and length > 0) and
        .inherent_workspace.id == (.setup_id + "::" + .path) and
        (.inherent_workspace.instance_id | type == "string" and length > 0) and
        .inherent_workspace.path == .path and
        .inherent_workspace.project_id == .project_id and
        .inherent_workspace.setup_id == .setup_id and
        .inherent_workspace.repo_id == .setup_id and
        .inherent_workspace.host_id == "local" and
        .inherent_workspace.name == .key and
        .inherent_workspace.is_bare == false and
        .inherent_workspace.is_main_worktree == true and
        .inherent_workspace.is_archived == false) and
      (.folder_projects[] | select(.key == "single-project") | .path) == $single_path and
      (.folder_projects[] | select(.key == "multi-project") | .path) == $multi_path and
      ([.folder_projects[].project_id] | unique | length) == 2 and
      ([.folder_projects[].setup_id] | unique | length) == 2 and
      ([.folder_projects[].inherent_workspace.id] | unique | length) == 2 and
      ([.folder_projects[].inherent_workspace.instance_id] | unique | length) == 2 and
      ((.folder_projects[] | select(.key == "single-project")) as $single |
       .disposable_workspace as $d |
        ($d.id | type == "string" and length > 0) and
        ($d.instance_id | type == "string" and length > 0) and
        ($d.path | type == "string" and length > 0) and
        ($d.name | type == "string" and length > 0) and
        $d.project_id == $single.project_id and $d.setup_id == $single.setup_id and
        $d.repo_id == $single.setup_id and $d.host_id == "local" and
        $d.id == ($single.setup_id + "::" + $single.path + "::workspace:" + $d.instance_id) and
        $d.is_main_worktree == false and $d.create_started_epoch > $accepted_epoch and
        $d.path == $single.path and $d.path_mode == "shared-folder-overlay" and
        $d.removed == true)
    ' "$VERIFICATION_RECEIPT" >/dev/null \
    || fail 'verification receipt folder-workspace binding is invalid'
}

print_verification_handoff() {
  printf 'VERIFY_ORCA_COMPLETE=true\n'
  printf 'STATE_DIRECTORY=%s\n' "$STATE_ROOT"
  printf 'VERIFICATION_RECEIPT=%s\n' "$VERIFICATION_RECEIPT"
  printf 'VERIFICATION_RECEIPT_SHA256=sha256:%s\n' "$(sha256_file "$VERIFICATION_RECEIPT")"
  jq -r '.folder_projects[] | "ORCA_FOLDER_PROJECT=" + .key + "\t" + .project_id + "\t" + .setup_id + "\t" + .path' \
    "$VERIFICATION_RECEIPT"
}

verify_orca_replay() {
  verify_verification_receipt
  verify_retained_trees
  verify_retained_git_evidence
  local single_project_id single_setup_id multi_project_id multi_setup_id worktree_id worktree_name
  single_project_id="$(jq -er '.folder_projects[] | select(.key == "single-project") | .project_id' "$VERIFICATION_RECEIPT")"
  single_setup_id="$(jq -er '.folder_projects[] | select(.key == "single-project") | .setup_id' "$VERIFICATION_RECEIPT")"
  multi_project_id="$(jq -er '.folder_projects[] | select(.key == "multi-project") | .project_id' "$VERIFICATION_RECEIPT")"
  multi_setup_id="$(jq -er '.folder_projects[] | select(.key == "multi-project") | .setup_id' "$VERIFICATION_RECEIPT")"
  worktree_id="$(jq -er '.disposable_workspace.id' "$VERIFICATION_RECEIPT")"
  worktree_name="$(jq -er '.disposable_workspace.name' "$VERIFICATION_RECEIPT")"
  assert_orca_ready "$CONTROL_DIR/replay-orca-status.json" "$CONTROL_DIR/replay-orca-hosts.json"
  capture_matching_worktrees replay-orca-worktrees \
    "$single_project_id" "$single_setup_id" "$multi_project_id" "$multi_setup_id" "$worktree_name"
  assert_inherent_folder_workspaces "$STATE_ROOT/replay-orca-worktrees-normalized.json" \
    "$single_project_id" "$single_setup_id" "$multi_project_id" "$multi_setup_id"
  jq -e --arg id "$worktree_id" '([.[] | select(.worktree_id == $id)] | length) == 0' \
    "$STATE_ROOT/replay-orca-worktrees-normalized.json" >/dev/null \
    || fail 'verified disposable ORCA workspace reappeared'
  pass 'authenticated ORCA verification receipt replayed with both inherent folder surfaces and no disposable child'
  print_verification_handoff
}

verify_orca_phase() {
  load_state_identity
  verify_authenticated_receipt "$ACCEPTANCE_RECEIPT" \
    loom.digital_estate_project_acceptance_state.v1 prepare
  if [[ -f "$VERIFICATION_RECEIPT" ]]; then
    verify_orca_replay
    return
  fi
  verify_retained_trees
  verify_retained_git_evidence
  pass 'acceptance receipt authenticated and retained estate is unchanged'

  assert_orca_ready "$STATE_ROOT/verify-orca-status.json" "$STATE_ROOT/verify-orca-hosts.json"
  orca project list --json >"$CONTROL_DIR/verify-orca-projects.raw.json"
  orca project setups --host local --json >"$CONTROL_DIR/verify-orca-setups.raw.json"
  jq -e '.ok == true' "$CONTROL_DIR/verify-orca-projects.raw.json" >/dev/null \
    || fail 'ORCA project discovery failed'
  jq -e '.ok == true' "$CONTROL_DIR/verify-orca-setups.raw.json" >/dev/null \
    || fail 'ORCA setup discovery failed'
  local identities single_project_id single_setup_id multi_project_id multi_setup_id
  identities="$(discover_orca_folder_projects)"
  IFS=$'\t' read -r single_project_id single_setup_id multi_project_id multi_setup_id <<<"$identities"
  [[ -n "$single_project_id" && -n "$single_setup_id" && -n "$multi_project_id" && -n "$multi_setup_id" ]] \
    || fail 'ORCA folder-project identity discovery was incomplete'
  pass 'both exact accepted roots have unique local folder-project identities'

  local worktree_name="loom-estate-pilot-${STATE_ID%%-*}"
  capture_matching_worktrees verify-orca-worktrees-before \
    "$single_project_id" "$single_setup_id" "$multi_project_id" "$multi_setup_id" "$worktree_name"
  assert_inherent_folder_workspaces "$STATE_ROOT/verify-orca-worktrees-before-normalized.json" \
    "$single_project_id" "$single_setup_id" "$multi_project_id" "$multi_setup_id"
  pass 'both exact folder projects expose one inherent main workspace and no disposable child'

  local accepted_epoch create_started_epoch worktree_id worktree_instance worktree_path
  accepted_epoch="$(jq -er '.prepared_at_epoch' "$ACCEPTANCE_RECEIPT")"
  create_started_epoch="$(date -u +%s)"
  ((create_started_epoch > accepted_epoch)) || fail 'ORCA workspace creation did not begin after accepted custody receipt'
  orca worktree create --project "$single_project_id" --host local \
    --name "$worktree_name" --no-parent --setup skip --json \
    >"$STATE_ROOT/verify-orca-worktree-create.json"
  jq -e '.ok == true and ((.result.worktree.id // "") | length) > 0 and ((.result.worktree.path // "") | length) > 0' \
    "$STATE_ROOT/verify-orca-worktree-create.json" >/dev/null || fail 'ORCA did not create a typed disposable workspace'
  worktree_id="$(jq -er '.result.worktree.id' "$STATE_ROOT/verify-orca-worktree-create.json")"
  worktree_instance="$(jq -er '.result.worktree.instanceId' "$STATE_ROOT/verify-orca-worktree-create.json")"
  worktree_path="$(jq -er '.result.worktree.path' "$STATE_ROOT/verify-orca-worktree-create.json")"
  [[ "$worktree_path" == "$PROJECTS_ROOT/single-project" ]] \
    || fail "ORCA folder workspace did not reuse only the exact accepted single-project root: $worktree_path"
  [[ "$worktree_id" == "$single_setup_id::$worktree_path::workspace:$worktree_instance" ]] \
    || fail 'ORCA folder workspace identity is not the exact non-main overlay form'
  jq -e --argjson accepted_epoch "$accepted_epoch" '
    .result.worktree.cliProvenance.kind == "created-by-cli" and
    (.result.worktree.cliProvenance.createdAt | type == "number") and
    .result.worktree.cliProvenance.createdAt > ($accepted_epoch * 1000)
  ' "$STATE_ROOT/verify-orca-worktree-create.json" >/dev/null \
    || fail 'ORCA folder workspace lacks post-acceptance public-CLI provenance'
  ACTIVE_WORKTREE_ID="$worktree_id"
  assert_disposable_worktree_response "$STATE_ROOT/verify-orca-worktree-create.json" \
    "$single_project_id" "$single_setup_id" "$worktree_id" "$worktree_instance" "$worktree_path" "$worktree_name" \
    || fail 'ORCA create response did not bind the disposable workspace to the accepted project'
  orca worktree show --worktree "id:$worktree_id" --json >"$STATE_ROOT/verify-orca-worktree-show.json"
  assert_disposable_worktree_response "$STATE_ROOT/verify-orca-worktree-show.json" \
    "$single_project_id" "$single_setup_id" "$worktree_id" "$worktree_instance" "$worktree_path" "$worktree_name" \
    || fail 'ORCA worktree show did not preserve accepted project identity'
  capture_matching_worktrees verify-orca-worktrees-created \
    "$single_project_id" "$single_setup_id" "$multi_project_id" "$multi_setup_id" "$worktree_name"
  assert_inherent_and_disposable_workspaces "$STATE_ROOT/verify-orca-worktrees-created-normalized.json" \
    "$single_project_id" "$single_setup_id" "$multi_project_id" "$multi_setup_id" \
    "$worktree_id" "$worktree_path" "$worktree_name"
  pass 'ORCA created one unique disposable workspace only after accepted custody'

  orca worktree rm --worktree "id:$worktree_id" --force --json \
    >"$STATE_ROOT/verify-orca-worktree-remove.json"
  jq -e '.ok == true' "$STATE_ROOT/verify-orca-worktree-remove.json" >/dev/null \
    || fail 'ORCA did not confirm disposable workspace removal'
  ACTIVE_WORKTREE_ID=""
  capture_matching_worktrees verify-orca-worktrees-after \
    "$single_project_id" "$single_setup_id" "$multi_project_id" "$multi_setup_id" "$worktree_name"
  assert_inherent_folder_workspaces "$STATE_ROOT/verify-orca-worktrees-after-normalized.json" \
    "$single_project_id" "$single_setup_id" "$multi_project_id" "$multi_setup_id"
  verify_retained_trees
  verify_retained_git_evidence
  pass 'disposable ORCA workspace removed and retained estate stayed unchanged'

  write_verification_receipt "$single_project_id" "$single_setup_id" \
    "$multi_project_id" "$multi_setup_id" "$worktree_id" "$worktree_path" \
    "$worktree_name" "$create_started_epoch"
  verify_verification_receipt
  pass 'authenticated bounded ORCA verification receipt published'
  print_verification_handoff
}

assert_verified_orca_state_absent() {
  local single_project_id single_setup_id multi_project_id multi_setup_id
  local single_inherent_id single_inherent_instance multi_inherent_id multi_inherent_instance
  local worktree_id worktree_instance worktree_name
  single_project_id="$(jq -er '.folder_projects[] | select(.key == "single-project") | .project_id' "$VERIFICATION_RECEIPT")"
  single_setup_id="$(jq -er '.folder_projects[] | select(.key == "single-project") | .setup_id' "$VERIFICATION_RECEIPT")"
  multi_project_id="$(jq -er '.folder_projects[] | select(.key == "multi-project") | .project_id' "$VERIFICATION_RECEIPT")"
  multi_setup_id="$(jq -er '.folder_projects[] | select(.key == "multi-project") | .setup_id' "$VERIFICATION_RECEIPT")"
  single_inherent_id="$(jq -er '.folder_projects[] | select(.key == "single-project") | .inherent_workspace.id' "$VERIFICATION_RECEIPT")"
  single_inherent_instance="$(jq -er '.folder_projects[] | select(.key == "single-project") | .inherent_workspace.instance_id' "$VERIFICATION_RECEIPT")"
  multi_inherent_id="$(jq -er '.folder_projects[] | select(.key == "multi-project") | .inherent_workspace.id' "$VERIFICATION_RECEIPT")"
  multi_inherent_instance="$(jq -er '.folder_projects[] | select(.key == "multi-project") | .inherent_workspace.instance_id' "$VERIFICATION_RECEIPT")"
  worktree_id="$(jq -er '.disposable_workspace.id' "$VERIFICATION_RECEIPT")"
  worktree_instance="$(jq -er '.disposable_workspace.instance_id' "$VERIFICATION_RECEIPT")"
  worktree_name="$(jq -er '.disposable_workspace.name' "$VERIFICATION_RECEIPT")"

  assert_orca_ready "$CONTROL_DIR/finalize-orca-status.json" "$CONTROL_DIR/finalize-orca-hosts.json"
  orca project list --json >"$CONTROL_DIR/finalize-orca-projects.json"
  orca project setups --host local --json >"$CONTROL_DIR/finalize-orca-setups.json"
  orca worktree list --json >"$CONTROL_DIR/finalize-orca-worktrees.json"
  jq -e \
    --arg single_project "$single_project_id" --arg multi_project "$multi_project_id" \
    --arg single_path "$PROJECTS_ROOT/single-project" --arg multi_path "$PROJECTS_ROOT/multi-project" '
      ([.result.projects[]? | .. | strings |
        select(. == $single_project or . == $multi_project or . == $single_path or . == $multi_path)] | length) == 0
    ' "$CONTROL_DIR/finalize-orca-projects.json" >/dev/null || fail 'a smoke-owned ORCA project remains after UI removal'
  jq -e \
    --arg single_project "$single_project_id" --arg multi_project "$multi_project_id" \
    --arg single_setup "$single_setup_id" --arg multi_setup "$multi_setup_id" \
    --arg single_path "$PROJECTS_ROOT/single-project" --arg multi_path "$PROJECTS_ROOT/multi-project" '
      ([.result.setups[]? | .. | strings |
        select(. == $single_project or . == $multi_project or . == $single_setup or . == $multi_setup or
               . == $single_path or . == $multi_path)] | length) == 0
    ' "$CONTROL_DIR/finalize-orca-setups.json" >/dev/null || fail 'a smoke-owned ORCA setup remains after UI removal'
  jq -e \
    --arg single_project "$single_project_id" --arg multi_project "$multi_project_id" \
    --arg single_setup "$single_setup_id" --arg multi_setup "$multi_setup_id" \
    --arg single_inherent "$single_inherent_id" --arg multi_inherent "$multi_inherent_id" \
    --arg single_instance "$single_inherent_instance" --arg multi_instance "$multi_inherent_instance" \
    --arg single_path "$PROJECTS_ROOT/single-project" --arg multi_path "$PROJECTS_ROOT/multi-project" \
    --arg worktree "$worktree_id" --arg worktree_instance "$worktree_instance" --arg name "$worktree_name" '
      ([((.result.worktrees // .result.items // [])[]?) | .. | strings |
        select(. == $single_project or . == $multi_project or . == $single_setup or . == $multi_setup or
               . == $single_inherent or . == $multi_inherent or
               . == $single_instance or . == $multi_instance or
               . == $single_path or . == $multi_path or . == $worktree or
               . == $worktree_instance or . == $name)] | length) == 0
    ' "$CONTROL_DIR/finalize-orca-worktrees.json" >/dev/null || fail 'smoke-owned ORCA workspace metadata remains after UI removal'
}

finalize_phase() {
  if [[ ! -e "$STATE_ROOT" ]]; then
    trap - EXIT INT TERM HUP
    printf 'FINALIZE_COMPLETE=true\n'
    printf 'STATE_DIRECTORY_REMOVED=%s\n' "$STATE_ROOT"
    printf 'FINALIZE_REPLAY=already_absent\n'
    exit 0
  fi
  load_state_identity
  verify_authenticated_receipt "$ACCEPTANCE_RECEIPT" \
    loom.digital_estate_project_acceptance_state.v1 prepare
  verify_verification_receipt
  assert_verified_orca_state_absent
  verify_retained_trees
  verify_retained_git_evidence
  pass 'ORCA project, setup, and workspace identities are absent and retained estate is unchanged'

  local removed_root="$STATE_ROOT"
  trap - EXIT INT TERM HUP
  if ! find "$removed_root" -xdev -depth -delete; then
    printf '[fail] final cleanup uncertainty; inspect %s\n' "$removed_root" >&2
    exit 1
  fi
  [[ ! -e "$removed_root" ]] || {
    printf '[fail] smoke-owned state directory remains after finalize: %s\n' "$removed_root" >&2
    exit 1
  }
  printf 'FINALIZE_COMPLETE=true\n'
  printf 'STATE_DIRECTORY_REMOVED=%s\n' "$removed_root"
  exit 0
}

require_command awk
require_command cmp
require_command date
require_command find
require_command git
require_command go
require_command jq
require_command openssl
require_command orca
require_command realpath
require_command sed
require_command shasum
require_command uuidgen

parse_arguments "$@"

case "$PHASE" in
  prepare) prepare_phase ;;
  verify-orca) verify_orca_phase ;;
  finalize) finalize_phase ;;
esac

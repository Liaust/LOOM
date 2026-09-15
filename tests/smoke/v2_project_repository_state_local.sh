#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FIXTURES="$ROOT/internal/projectcontracts/testdata/repository_state_acceptance"
TMP_BASE="${TMPDIR:-/tmp}"
TMP_BASE="${TMP_BASE%/}"
TMP_ROOT="$(mktemp -d "$TMP_BASE/loom-project-repository-state.XXXXXX")"
PG_SOCKET="$(mktemp -d /tmp/loom-project-repos-pg.XXXXXX)"
DAEMON_SOCKET_ROOT="$(mktemp -d /tmp/loom-project-repos-daemon.XXXXXX)"
PG_DATA="$TMP_ROOT/postgres"
RUNTIME_ROOT="$TMP_ROOT/runtime"
PROJECTS_ROOT="$RUNTIME_ROOT/box/Projects"
SOCKET_PATH="$DAEMON_SOCKET_ROOT/loomd.sock"
LOOM_BIN="$TMP_ROOT/bin/loom"
LOOMD_BIN="$TMP_ROOT/bin/loomd"
DAEMON_PID=""
POSTGRES_STARTED=false
PASS_COUNT=0

log() {
  printf '[project-repository-state] %s\n' "$*"
}

pass() {
  PASS_COUNT=$((PASS_COUNT + 1))
  printf '[ok] %s\n' "$*"
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
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
    */loom-project-repository-state.*)
      find "$TMP_ROOT" -xdev -depth -delete >/dev/null 2>&1 || true
      ;;
  esac
  case "$PG_SOCKET" in
    /tmp/loom-project-repos-pg.*)
      find "$PG_SOCKET" -xdev -depth -delete >/dev/null 2>&1 || true
      ;;
  esac
  case "$DAEMON_SOCKET_ROOT" in
    /tmp/loom-project-repos-daemon.*)
      find "$DAEMON_SOCKET_ROOT" -xdev -depth -delete >/dev/null 2>&1 || true
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
  local remaining=300
  env \
    HOME="$RUNTIME_ROOT/home" \
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
    "$LOOMD_BIN" serve >"$TMP_ROOT/loomd.log" 2>&1 &
  DAEMON_PID=$!

  while [[ "$remaining" -gt 0 ]]; do
    if [[ -S "$SOCKET_PATH" ]]; then
      return
    fi
    if ! kill -0 "$DAEMON_PID" >/dev/null 2>&1; then
      sed -n '1,240p' "$TMP_ROOT/loomd.log" >&2
      fail 'loomd exited before creating its disposable Unix socket'
    fi
    sleep 0.1
    remaining=$((remaining - 1))
  done
  sed -n '1,240p' "$TMP_ROOT/loomd.log" >&2
  fail 'loomd did not create its disposable Unix socket within thirty seconds'
}

loom() {
  env \
    HOME="$RUNTIME_ROOT/home" \
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
    "$LOOM_BIN" --socket "$SOCKET_PATH" --json "$@"
}

install_fixture_project() {
  local project_root="$1"
  local project_fixture="$2"
  local repos_fixture="$3"
  mkdir -p "$project_root/.loom/contracts" "$project_root/repos"
  cp "$FIXTURES/$project_fixture" "$project_root/.loom/project.yaml"
  cp "$FIXTURES/$repos_fixture" "$project_root/.loom/contracts/repos.yaml"
}

render_fixture() {
  local source="$1"
  local destination="$2"
  local project_id="$3"
  local repository_id="${4:-}"
  sed \
    -e "s/__PROJECT_ID__/$project_id/g" \
    -e "s/__REPOSITORY_ID__/$repository_id/g" \
    "$source" >"$destination"
}

replace_literal() {
  local path="$1"
  local before="$2"
  local after="$3"
  sed "s|$before|$after|" "$path" >"$path.next"
  mv "$path.next" "$path"
}

init_git_repository() {
  local repository_root="$1"
  mkdir -p "$repository_root"
  git -C "$repository_root" init -q -b main
  printf '%s\n' "fixture $(basename "$repository_root")" >"$repository_root/README.md"
  git -C "$repository_root" add README.md
  git -C "$repository_root" -c user.name='LOOM Slice 5' -c user.email='slice5@invalid' \
    commit -q -m 'fixture: initialize repository'
}

assert_project_analysis() {
  local path="$1"
  local schema="$2"
  local member_count="$3"
  jq -e --arg schema "$schema" --argjson count "$member_count" '
    .ok == true and
    .registerable == true and
    .repository_source.contract_schema_version == $schema and
    ((.repository_members // []) | length) == $count
  ' "$path" >/dev/null || fail "project analysis assertion failed: $path"
}

policy_projection() {
  local source="$1"
  local destination="$2"
  jq -S '{
    repos: [.repos[] | {
      key, path, project_path, display_name, status, sync, backup, index,
      include: (.include // []), exclude: (.exclude // [])
    }],
    watched_roots: [.watched_roots[] | {
      key, backend_root_key, safe_root_key, root_relative_path, display_name,
      include: (.include // []), exclude: (.exclude // []), sync_mode,
      backup_mode, index_mode, delete_mode, config_hash, config_json
    }]
  }' "$source" >"$destination"
}

require_command go
require_command git
require_command jq
require_command sed
require_command cmp
resolve_postgres

mkdir -p \
  "$TMP_ROOT/bin" \
  "$RUNTIME_ROOT/home" \
  "$RUNTIME_ROOT/config" \
  "$RUNTIME_ROOT/run" \
  "$RUNTIME_ROOT/storage/imports" \
  "$RUNTIME_ROOT/storage/backups" \
  "$RUNTIME_ROOT/storage/archive" \
  "$RUNTIME_ROOT/data/object-store" \
  "$RUNTIME_ROOT/data/box-state" \
  "$RUNTIME_ROOT/data/generated" \
  "$RUNTIME_ROOT/data/storage-retention" \
  "$PROJECTS_ROOT"

log 'starting a smoke-owned PostgreSQL 17 + pgvector cluster'
start_postgres
MAIN_DB_URL="postgresql://postgres@/loom_main?host=$PG_SOCKET"
PROVENANCE_DB_URL="postgresql://loom_provenance@/loom_provenance?host=$PG_SOCKET"
pass 'disposable PostgreSQL cluster and isolated databases are ready'

log 'building disposable loom and loomd binaries'
(cd "$ROOT" && go build -o "$LOOM_BIN" ./cmd/loom && go build -o "$LOOMD_BIN" ./cmd/loomd)
pass 'real CLI and daemon binaries built outside the repository'

log 'booting loomd against disposable database and filesystem roots'
start_daemon
pass 'loomd migrated and bootstrapped the disposable runtime'

postgres_version_num="$(
  "$PG_BIN/psql" -X -Aqt "$MAIN_DB_URL" -c 'SHOW server_version_num'
)"
[[ "$postgres_version_num" == 17* ]] \
  || fail "disposable PostgreSQL major version is not 17: $postgres_version_num"
main_schema_head="$(
  "$PG_BIN/psql" -X -Aqt "$MAIN_DB_URL" \
    -c 'SELECT max(version_id) FROM public.goose_db_version WHERE is_applied'
)"
[[ "$main_schema_head" == 62 ]] \
  || fail "main database schema head is $main_schema_head, want 62"
vector_version="$(
  "$PG_BIN/psql" -X -Aqt "$MAIN_DB_URL" \
    -c "SELECT extversion FROM pg_extension WHERE extname = 'vector'"
)"
[[ -n "$vector_version" ]] || fail 'main database does not have the pgvector extension'
provenance_schema_head="$(
  "$PG_BIN/psql" -X -Aqt -h "$PG_SOCKET" -U postgres -d loom_provenance \
    -c 'SELECT max(version) FROM provenance.schema_migrations'
)"
[[ "$provenance_schema_head" == 6 ]] \
  || fail "provenance database schema head is $provenance_schema_head, want 6"
main_has_provenance="$(
  "$PG_BIN/psql" -X -Aqt "$MAIN_DB_URL" \
    -c "SELECT CASE WHEN to_regnamespace('provenance') IS NULL THEN 'no' ELSE 'yes' END"
)"
[[ "$main_has_provenance" == no ]] || fail 'main database contains a provenance schema'
provenance_has_projects="$(
  "$PG_BIN/psql" -X -Aqt -h "$PG_SOCKET" -U postgres -d loom_provenance \
    -c "SELECT CASE WHEN to_regnamespace('projects') IS NULL THEN 'no' ELSE 'yes' END"
)"
[[ "$provenance_has_projects" == no ]] || fail 'provenance database contains a projects schema'
set +e
"$PG_BIN/psql" -X -Aqt -h "$PG_SOCKET" -U loom_provenance -d loom_main \
  -c 'SELECT 1' >"$TMP_ROOT/provenance-main-connect.out" 2>"$TMP_ROOT/provenance-main-connect.err"
cross_database_status=$?
set -e
[[ "$cross_database_status" -ne 0 ]] || fail 'provenance role connected to the main database'
pass 'PostgreSQL 17, pgvector, main head 62, provenance head 6, and database isolation passed'

loom box init --path "$RUNTIME_ROOT/box" --profile main >"$TMP_ROOT/box-init.json"
jq -e '.status_after.state == "ok" and .status_after.initialized == true' \
  "$TMP_ROOT/box-init.json" >/dev/null
pass 'disposable main-profile Box is canonical before backend project analysis'

"$PG_BIN/psql" -X -v ON_ERROR_STOP=1 "$MAIN_DB_URL" >/dev/null <<'SQL'
INSERT INTO nodes.nodes (
  node_id, node_key, display_name, node_kind, node_role, runtime_class, status, metadata
) VALUES (
  'node_slice5_remote', 'workspace-remote', 'Slice 5 Remote',
  'workspace', 'workspace', 'database_capable', 'active', '{}'::jsonb
) ON CONFLICT (node_key) DO NOTHING;
SQL
pass 'disposable remote-owner node fixture is available without external contact'

log 'scaffolding and registering a current explicit-empty project'
loom project scaffold 'Slice 5 Scaffold' \
  --owner-node main \
  --slug slice5-scaffold \
  --facets repos \
  --directory "$PROJECTS_ROOT" >"$TMP_ROOT/scaffold.json"
scaffold_root="$PROJECTS_ROOT/slice5-scaffold"
[[ -f "$scaffold_root/.loom/project.yaml" ]] || fail 'scaffold omitted project contract'
[[ -f "$scaffold_root/.loom/contracts/repos.yaml" ]] || fail 'scaffold omitted repositories contract'
loom project validate "$scaffold_root" >"$TMP_ROOT/scaffold-validate.json"
loom project plan "$scaffold_root" >"$TMP_ROOT/scaffold-plan.json"
assert_project_analysis "$TMP_ROOT/scaffold-validate.json" repos.contract.v0.4 0
jq -e '.registerable == true and ((.repository_members // []) | length) == 0' \
  "$TMP_ROOT/scaffold-plan.json" >/dev/null
if ! loom project register "$scaffold_root" --backend \
  --idempotency-key slice5-scaffold-register >"$TMP_ROOT/scaffold-register.json"; then
  sed -n '1,240p' "$TMP_ROOT/scaffold-register.json" >&2
  tail -n 120 "$TMP_ROOT/loomd.log" >&2
  fail 'backend registration of the scaffolded project failed'
fi
jq -e '.ok == true and .data.created == true and .data.repository_source.member_count == 0' \
  "$TMP_ROOT/scaffold-register.json" >/dev/null
loom project repos list slice5-scaffold >"$TMP_ROOT/scaffold-repos.json"
jq -e '.ok == true and (.data.repositories | length) == 0' "$TMP_ROOT/scaffold-repos.json" >/dev/null
pass 'scaffold, validate, plan, backend register, and empty membership passed'

log 'proving the committed explicit-empty fixture is independently registerable'
empty_root="$PROJECTS_ROOT/slice5-empty"
install_fixture_project "$empty_root" v04-empty-project.yaml v04-empty-repos.yaml
loom project validate "$empty_root" >"$TMP_ROOT/empty-validate.json"
assert_project_analysis "$TMP_ROOT/empty-validate.json" repos.contract.v0.4 0
loom project register "$empty_root" --backend \
  --idempotency-key slice5-empty-register >"$TMP_ROOT/empty-register.json"
jq -e '.ok == true and .data.repository_source.member_count == 0' "$TMP_ROOT/empty-register.json" >/dev/null
pass 'explicit fixture preserves zero-member persistence'

log 'registering v0.3, upgrading to v0.4, and comparing watched-root policy'
compat_root="$PROJECTS_ROOT/slice5-compat"
install_fixture_project "$compat_root" v03-project.yaml v03-repos.yaml
mkdir -p "$compat_root/repos/source"
loom project validate "$compat_root" >"$TMP_ROOT/compat-v03-validate.json"
loom project plan "$compat_root" >"$TMP_ROOT/compat-v03-plan.json"
assert_project_analysis "$TMP_ROOT/compat-v03-validate.json" repos.contract.v0.3 0
policy_projection "$TMP_ROOT/compat-v03-validate.json" "$TMP_ROOT/compat-v03-policy.json"
loom project register "$compat_root" \
  --idempotency-key slice5-compat-v03-direct >"$TMP_ROOT/compat-v03-register.json"
compat_project_id="$(jq -er '.data.detail.project.project.project_id' "$TMP_ROOT/compat-v03-register.json")"
jq -e '
  .ok == true and
  .data.repository_source.classification == "first_registration" and
  .data.repository_source.source_revision == 1 and
  .data.repository_source.member_count == 0
' "$TMP_ROOT/compat-v03-register.json" >/dev/null

render_fixture "$FIXTURES/v04-compat-project.yaml" "$compat_root/.loom/project.yaml" "$compat_project_id"
cp "$FIXTURES/v04-compat-repos.yaml" "$compat_root/.loom/contracts/repos.yaml"
loom project validate "$compat_root" >"$TMP_ROOT/compat-v04-validate.json"
loom project plan "$compat_root" >"$TMP_ROOT/compat-v04-plan.json"
assert_project_analysis "$TMP_ROOT/compat-v04-validate.json" repos.contract.v0.4 0
policy_projection "$TMP_ROOT/compat-v04-validate.json" "$TMP_ROOT/compat-v04-policy.json"
cmp -s "$TMP_ROOT/compat-v03-policy.json" "$TMP_ROOT/compat-v04-policy.json" \
  || fail 'v0.3 to v0.4 changed effective watched-root policy'
loom project register "$compat_root" --backend \
  --idempotency-key slice5-compat-v04-backend >"$TMP_ROOT/compat-v04-register.json"
jq -e --arg project "$compat_project_id" '
  .ok == true and
  .data.updated == true and
  .data.detail.project.project.project_id == $project and
  .data.repository_source.source_revision == 2 and
  .data.repository_source.member_count == 0
' "$TMP_ROOT/compat-v04-register.json" >/dev/null
loom project repos list slice5-compat >"$TMP_ROOT/compat-repos.json"
jq -e '
  .ok == true and
  .data.source.project_contract_schema_version == "project.contract.v0.4" and
  .data.source.repos_contract_schema_version == "repos.contract.v0.4" and
  (.data.repositories | length) == 0
' "$TMP_ROOT/compat-repos.json" >/dev/null
pass 'v0.3 compatibility, direct registration, v0.4 upgrade, and policy equivalence passed'

log 'proving one-member identity survives replay, relocation, and path rename'
one_root="$PROJECTS_ROOT/slice5-one"
install_fixture_project "$one_root" v04-one-project.yaml v04-one-repos.yaml
init_git_repository "$one_root/repos/service"
mkdir -p "$one_root/repos/service/.repo"
render_fixture "$FIXTURES/repo-identity-valid.yaml" \
  "$one_root/repos/service/.repo/repo.yaml" \
  project_01ARZ3NDEKTSV4RRFFQ69G5FAX \
  repo_01ARZ3NDEKTSV4RRFFQ69G5FAA
loom project validate "$one_root" >"$TMP_ROOT/one-validate.json"
assert_project_analysis "$TMP_ROOT/one-validate.json" repos.contract.v0.4 1
loom project register "$one_root" \
  --idempotency-key slice5-one-direct >"$TMP_ROOT/one-direct-register.json"
jq -e '
  .ok == true and
  .data.created == true and
  .data.repository_source.classification == "first_registration" and
  .data.repository_source.source_revision == 1 and
  .data.repository_source.member_count == 1
' "$TMP_ROOT/one-direct-register.json" >/dev/null
loom project register "$one_root" --backend \
  --idempotency-key slice5-one-backend-replay >"$TMP_ROOT/one-backend-replay.json"
jq -e '
  .ok == true and
  .data.unchanged == true and
  .data.repository_source.classification == "identical_replay" and
  .data.repository_source.source_revision == 1
' "$TMP_ROOT/one-backend-replay.json" >/dev/null

one_moved_root="$PROJECTS_ROOT/slice5-one-moved"
mv "$one_root" "$one_moved_root"
loom project register "$one_moved_root" --backend \
  --idempotency-key slice5-one-relocation >"$TMP_ROOT/one-relocation.json"
jq -e '
  .ok == true and
  .data.updated == true and
  .data.detail.project.project.project_id == "project_01ARZ3NDEKTSV4RRFFQ69G5FAX" and
  .data.repository_source.classification == "source_relocation" and
  .data.repository_source.source_revision == 2
' "$TMP_ROOT/one-relocation.json" >/dev/null || {
  jq . "$TMP_ROOT/one-relocation.json" >&2
  fail 'one-member source relocation response did not preserve identity'
}

mv "$one_moved_root/repos/service" "$one_moved_root/repos/service-renamed"
replace_literal "$one_moved_root/.loom/contracts/repos.yaml" 'path: service' 'path: service-renamed'
loom project register "$one_moved_root" --backend \
  --idempotency-key slice5-one-member-rename >"$TMP_ROOT/one-rename.json"
jq -e '
  .ok == true and
  .data.updated == true and
  .data.detail.project.project.project_id == "project_01ARZ3NDEKTSV4RRFFQ69G5FAX" and
  .data.repository_source.classification == "semantic_change" and
  .data.repository_source.source_revision == 3 and
  .data.repository_source.member_count == 1
' "$TMP_ROOT/one-rename.json" >/dev/null
loom project repos inspect slice5-one repo_01ARZ3NDEKTSV4RRFFQ69G5FAA >"$TMP_ROOT/one-inspect.json"
jq -e '
  .ok == true and
  .data.project.project_id == "project_01ARZ3NDEKTSV4RRFFQ69G5FAX" and
  .data.repository.repository_id == "repo_01ARZ3NDEKTSV4RRFFQ69G5FAA" and
  .data.repository.relative_path == "service-renamed" and
  .data.repository.development_state.posture == "enabled" and
  .data.repository.observation_posture == "observed"
' "$TMP_ROOT/one-inspect.json" >/dev/null
pass 'direct/backend replay, project relocation, member rename, and inspect preserved identity'

log 'rejecting duplicate and cross-project owning repository identities'
duplicate_root="$PROJECTS_ROOT/slice5-duplicate"
install_fixture_project "$duplicate_root" v04-duplicate-project.yaml v04-duplicate-repos.yaml
set +e
loom project validate "$duplicate_root" >"$TMP_ROOT/duplicate-validate.json" 2>"$TMP_ROOT/duplicate-validate.err"
duplicate_status=$?
set -e
[[ "$duplicate_status" -ne 0 ]] || fail 'duplicate repository identity unexpectedly validated'
jq -e '
  .ok == false and
  any(.diagnostics[]; .code == "repos.member_id_duplicate")
' "$TMP_ROOT/duplicate-validate.json" >/dev/null

cross_root="$PROJECTS_ROOT/slice5-cross-conflict"
install_fixture_project "$cross_root" v04-cross-conflict-project.yaml v04-cross-conflict-repos.yaml
init_git_repository "$cross_root/repos/stolen-owner"
set +e
loom project register "$cross_root" --backend \
  --idempotency-key slice5-cross-conflict >"$TMP_ROOT/cross-register.out" 2>"$TMP_ROOT/cross-register.err"
cross_status=$?
set -e
[[ "$cross_status" -ne 0 ]] || fail 'cross-project owning repository conflict unexpectedly registered'
set +e
loom project inspect slice5-cross-conflict >"$TMP_ROOT/cross-inspect.out" 2>"$TMP_ROOT/cross-inspect.err"
cross_inspect_status=$?
set -e
[[ "$cross_inspect_status" -ne 0 ]] || fail 'failed cross-project registration left a project row'
loom project repos inspect slice5-one repo_01ARZ3NDEKTSV4RRFFQ69G5FAA >"$TMP_ROOT/one-owner-after-conflict.json"
jq -e '
  .data.repository.repository_owner_project_id == "project_01ARZ3NDEKTSV4RRFFQ69G5FAX"
' "$TMP_ROOT/one-owner-after-conflict.json" >/dev/null
pass 'duplicate and cross-project identity conflicts fail closed without partial persistence'

log 'building the many-member Git and .repo adversarial matrix'
many_root="$PROJECTS_ROOT/slice5-many"
install_fixture_project "$many_root" v04-many-project.yaml v04-many-repos.yaml

init_git_repository "$many_root/repos/clean"
mkdir -p "$many_root/repos/clean/.repo"
render_fixture "$FIXTURES/repo-identity-valid.yaml" \
  "$many_root/repos/clean/.repo/repo.yaml" \
  project_01ARZ3NDEKTSV4RRFFQ69G5FAY \
  repo_01ARZ3NDEKTSV4RRFFQ69G5FAB
git -C "$many_root/repos/clean" add .repo/repo.yaml
git -C "$many_root/repos/clean" \
  -c user.name='LOOM Slice 5' -c user.email='slice5@invalid' \
  commit -q -m 'fixture: enable repository identity'

init_git_repository "$many_root/repos/dirty"
printf '%s\n' 'dirty change' >>"$many_root/repos/dirty/README.md"

init_git_repository "$many_root/repos/missing-state"
mkdir -p "$many_root/repos/missing-state/.repo"

init_git_repository "$many_root/repos/malformed-state"
mkdir -p "$many_root/repos/malformed-state/.repo"
cp "$FIXTURES/repo-identity-malformed.yaml" "$many_root/repos/malformed-state/.repo/repo.yaml"

init_git_repository "$many_root/repos/mismatched-state"
mkdir -p "$many_root/repos/mismatched-state/.repo"
cp "$FIXTURES/repo-identity-mismatch.yaml" "$many_root/repos/mismatched-state/.repo/repo.yaml"

init_git_repository "$many_root/repos/container"
init_git_repository "$many_root/repos/container/nested"

linked_source="$TMP_ROOT/linked-source"
init_git_repository "$linked_source"
git -C "$linked_source" worktree add -q -b slice5-linked "$many_root/repos/linked"

loom project validate "$many_root" >"$TMP_ROOT/many-validate.json"
loom project plan "$many_root" >"$TMP_ROOT/many-plan.json"
assert_project_analysis "$TMP_ROOT/many-validate.json" repos.contract.v0.4 8
jq -e '.registerable == true and (.repository_members | length) == 8' "$TMP_ROOT/many-plan.json" >/dev/null
loom project register "$many_root" --backend \
  --idempotency-key slice5-many-register >"$TMP_ROOT/many-register.json"
jq -e '
  .ok == true and
  .data.repository_source.classification == "first_registration" and
  .data.repository_source.member_count == 8
' "$TMP_ROOT/many-register.json" >/dev/null

loom project repos status slice5-many --limit 50 >"$TMP_ROOT/many-status.json"
jq -e '
  .ok == true and
  .data.observation.member_count == 8 and
  .data.observation.observed == 7 and
  .data.observation.not_observed == 1 and
  (.data.repositories | length) == 8 and
  any(.data.repositories[];
    .key == "clean" and .observation_posture == "observed" and
    .development_state.posture == "enabled" and .git.dirty.dirty == false) and
  any(.data.repositories[];
    .key == "dirty" and .observation_posture == "observed" and
    .git.dirty.dirty == true and .git.dirty.tracked_changes == true) and
  any(.data.repositories[];
    .key == "absent" and .observation_posture == "not_observed" and
    .reason_code == "member_path_missing" and (has("git") | not)) and
  any(.data.repositories[];
    .key == "missing-state" and .development_state.posture == "invalid" and
    .development_state.reason_code == "identity_missing") and
  any(.data.repositories[];
    .key == "malformed-state" and .development_state.posture == "invalid" and
    .development_state.reason_code == "identity_malformed") and
  any(.data.repositories[];
    .key == "mismatched-state" and .development_state.posture == "mismatched" and
    .development_state.reason_code == "identity_backlink_mismatch") and
  any(.data.repositories[];
    .key == "nested" and .observation_posture == "observed" and
    .git.root_matches_member == true) and
  any(.data.repositories[];
    .key == "linked" and .observation_posture == "observed" and
    .git.worktree == true and .git.worktree_canonical_project_state == false)
' "$TMP_ROOT/many-status.json" >/dev/null || {
  jq . "$TMP_ROOT/many-status.json" >&2
  fail 'many-member repository status did not match the acceptance matrix'
}

loom project repos list slice5-many --limit 3 >"$TMP_ROOT/many-page-one.json"
page_after="$(jq -er '.data.page.next_after' "$TMP_ROOT/many-page-one.json")"
jq -e '
  .ok == true and
  (.data.repositories | length) == 3 and
  .data.page.has_more == true
' "$TMP_ROOT/many-page-one.json" >/dev/null
loom project repos list slice5-many --limit 3 --after "$page_after" >"$TMP_ROOT/many-page-two.json"
jq -e --arg after "$page_after" '
  .ok == true and
  (.data.repositories | length) > 0 and
  all(.data.repositories[]; .repository_id > $after)
' "$TMP_ROOT/many-page-two.json" >/dev/null
pass 'absent, identity, dirty, nested Git, linked worktree, and pagination matrix passed'

log 'proving remote-owned repositories report typed unavailability without local inference'
remote_root="$PROJECTS_ROOT/slice5-remote"
install_fixture_project "$remote_root" v04-remote-project.yaml v04-remote-repos.yaml
loom project register "$remote_root" --backend \
  --idempotency-key slice5-remote-register >"$TMP_ROOT/remote-register.json"
loom project repos status slice5-remote >"$TMP_ROOT/remote-status.json"
jq -e '
  .ok == true and
  .data.observation.member_count == 1 and
  .data.observation.remote_unavailable == 1 and
  .data.repositories[0].relative_path == "remote" and
  .data.repositories[0].observation_posture == "remote_unavailable" and
  .data.repositories[0].reason_code == "owner_node_not_local" and
  (.data.repositories[0] | has("git") | not)
' "$TMP_ROOT/remote-status.json" >/dev/null
pass 'remote-unavailable observation is bounded and does not infer local state'

log 'archiving the many-member project and verifying read-only repository state'
loom project archive slice5-many --dry-run --skip-storage-archive \
  --reason 'Slice 5 disposable acceptance' >"$TMP_ROOT/many-archive-dry-run.json"
jq -e '.ok == true and .data.dry_run == true' "$TMP_ROOT/many-archive-dry-run.json" >/dev/null
loom project archive slice5-many --skip-storage-archive \
  --idempotency-key slice5-many-archive \
  --reason 'Slice 5 disposable acceptance' >"$TMP_ROOT/many-archive.json"
jq -e '
  .ok == true and
  .data.project.project.project.status == "archived" and
  .data.storage_archive_skipped == true and
  .data.safe_to_delete == false
' "$TMP_ROOT/many-archive.json" >/dev/null
loom project archive inspect slice5-many >"$TMP_ROOT/many-archive-inspect.json"
jq -e '
  .ok == true and
  .data.project.project.project.status == "archived" and
  .data.runtime_manifest.project_slug == "slice5-many"
' "$TMP_ROOT/many-archive-inspect.json" >/dev/null
loom project repos list slice5-many >"$TMP_ROOT/many-archived-list.json"
loom project repos status slice5-many >"$TMP_ROOT/many-archived-status.json"
loom project repos inspect slice5-many repo_01ARZ3NDEKTSV4RRFFQ69G5FAB >"$TMP_ROOT/many-archived-inspect.json"
jq -e '.ok == true and .data.project.lifecycle == "archived" and (.data.repositories | length) == 8' \
  "$TMP_ROOT/many-archived-list.json" >/dev/null
jq -e '.ok == true and .data.project.lifecycle == "archived" and .data.observation.member_count == 8' \
  "$TMP_ROOT/many-archived-status.json" >/dev/null
jq -e '.ok == true and .data.project.lifecycle == "archived" and .data.repository.key == "clean"' \
  "$TMP_ROOT/many-archived-inspect.json" >/dev/null
set +e
loom project register "$many_root" --backend \
  --idempotency-key slice5-archived-register-rejected \
  >"$TMP_ROOT/many-archived-register.out" 2>"$TMP_ROOT/many-archived-register.err"
archived_register_status=$?
set -e
[[ "$archived_register_status" -ne 0 ]] || fail 'archived repository registration mutation unexpectedly succeeded'
pass 'archive dry-run/apply/inspect and read-only archived repository state passed'

log "completed $PASS_COUNT checks"

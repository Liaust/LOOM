#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d "/tmp/loom-canonical-filesystem.XXXXXX")"
DB_CONTAINER=""
SMOKE_DB_URL=""
PASS_COUNT=0

cleanup() {
  local exit_status=$?
  trap - EXIT INT TERM HUP
  set +e
  if [[ -n "$DB_CONTAINER" ]]; then
    docker stop "$DB_CONTAINER" >/dev/null 2>&1 || true
  fi
  case "$TMP_DIR" in
    /tmp/loom-canonical-filesystem.*)
      find "$TMP_DIR" -xdev -depth -delete >/dev/null 2>&1 || true
      ;;
  esac
  exit "$exit_status"
}
trap cleanup EXIT INT TERM HUP

log() {
  printf '[canonical-filesystem] %s\n' "$*"
}

pass() {
  PASS_COUNT=$((PASS_COUNT + 1))
  printf '[ok] %s\n' "$*"
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

run_go_test() {
  (
    cd "$ROOT"
    LOOM_TEST_DB_URL="$SMOKE_DB_URL" \
      "$@"
  )
}

run_go_test_without_database() {
  (
    cd "$ROOT"
    LOOM_TEST_DB_URL= "$@"
  )
}

start_database() {
  local port_output port ready_count=0
  if [[ -n "${LOOM_TEST_DB_URL:-}" ]]; then
    log 'ignoring ambient LOOM_TEST_DB_URL; this smoke always owns its disposable database'
  fi
  command -v docker >/dev/null 2>&1 || fail 'install Docker for the smoke-owned isolated PostgreSQL fixture'
  docker info >/dev/null 2>&1 || fail 'start Docker for the smoke-owned isolated PostgreSQL fixture'
  DB_CONTAINER="loom-canonical-filesystem-${PPID}-$$"
  log "starting smoke-owned disposable PostgreSQL container ${DB_CONTAINER}"
  docker run --rm -d \
    --name "$DB_CONTAINER" \
    -e POSTGRES_PASSWORD=loom-smoke \
    -e POSTGRES_DB=loomtest \
    -p 127.0.0.1::5432 \
    pgvector/pgvector:pg16 >/dev/null
  for _ in $(seq 1 120); do
    if docker exec "$DB_CONTAINER" pg_isready -U postgres -d loomtest >/dev/null 2>&1; then
      ready_count=$((ready_count + 1))
      if [[ "$ready_count" -ge 4 ]]; then
        break
      fi
    else
      ready_count=0
    fi
    sleep 0.25
  done
  if [[ "$ready_count" -lt 4 ]]; then
    docker logs --tail 40 "$DB_CONTAINER" >&2 || true
    fail 'isolated PostgreSQL did not become ready'
  fi
  port_output="$(docker port "$DB_CONTAINER" 5432/tcp | sed -n '1p')"
  port="${port_output##*:}"
  [[ "$port" =~ ^[0-9]+$ ]] || fail 'could not resolve the isolated PostgreSQL port'
  SMOKE_DB_URL="postgres://postgres:loom-smoke@127.0.0.1:${port}/loomtest?sslmode=disable"
}

run_box_fixture() {
  local service_root="$TMP_DIR/srv/loom"
  local box_root="$service_root/box"
  local storage_root="$service_root/storage"
  local data_root="$TMP_DIR/var/lib/loom"
  local loom_bin="$TMP_DIR/bin/loom"
  local config_file="$TMP_DIR/isolated.env"
  local dry_run_json="$TMP_DIR/box-init-dry-run.json"
  local init_json="$TMP_DIR/box-init.json"
  local status_json="$TMP_DIR/box-status.json"

  mkdir -p "$TMP_DIR/bin" "$storage_root/imports" "$storage_root/backups" \
    "$storage_root/archive" "$data_root"
  printf '# isolated canonical-filesystem smoke config\n' >"$config_file"
  (cd "$ROOT" && go build -o "$loom_bin" ./cmd/loom)

  local -a loom_env=(
    env
    LOOM_ENV=test
    LOOM_NODE_ID=canonical-filesystem-smoke
    LOOM_NODE_KIND=main
    LOOM_NODE_ROLE=main
    LOOM_RUNTIME_CLASS=main_full
    LOOM_SERVICE_ROOT="$service_root"
    LOOM_BOX_PATH="$box_root"
    LOOM_STORAGE_ROOT="$storage_root"
    LOOM_IMPORTS_ROOT="$storage_root/imports"
    LOOM_USER_BACKUPS_ROOT="$storage_root/backups"
    LOOM_ARCHIVE_ROOT="$storage_root/archive"
    LOOM_GENERATED_ROOT="$data_root/generated"
    LOOM_BOX_STATE_ROOT="$data_root/box-state"
    LOOM_DATA_DIR="$data_root"
    LOOM_OBJECT_STORE="$data_root/object-store"
    LOOM_STORAGE_RETENTION_ROOT="$data_root/storage-retention"
    LOOM_STORAGE_EXPORT_ROOT="$data_root/legacy-export"
    LOOM_MAIN_DOCUMENTS_ROOT="$data_root/legacy-main-documents"
    LOOM_NOTES_PROJECTION_ROOT="$data_root/generated/notes"
    LOOM_SOCKET_PATH="$TMP_DIR/run/loomd.sock"
  )

  "${loom_env[@]}" "$loom_bin" --config "$config_file" --json box init --dry-run --path "$box_root" --profile main >"$dry_run_json"
  jq -e --arg root "$box_root" '.dry_run == true and .root_path == $root and (.planned_dirs | length > 0)' "$dry_run_json" >/dev/null
  [[ ! -e "$box_root" ]] || fail 'Box dry-run mutated the temporary Box root'

  "${loom_env[@]}" "$loom_bin" --config "$config_file" --json box init --path "$box_root" --profile main >"$init_json"
  "${loom_env[@]}" "$loom_bin" --config "$config_file" --json box status --path "$box_root" --profile main >"$status_json"
  jq -e --arg root "$box_root" --arg state "$data_root/box-state" \
    '.root_path == $root and .initialized == true and .runtime_state_root == $state' \
    "$status_json" >/dev/null
  [[ -d "$box_root/Documents" && -d "$box_root/Notes" && -d "$box_root/Projects" ]] || fail 'Box init omitted a canonical source directory'
  [[ ! -e "$box_root/.loom/state" ]] || fail 'Box init recreated visible runtime state under .loom/state'
}

render_smb_contract() {
  local rendered="$TMP_DIR/rendered-smb.json"
  (
    cd "$ROOT"
    nix --extra-experimental-features 'nix-command flakes' eval --json --impure --expr '
      let
        flake = (import ./tests/nix/source-flake.nix {});
        lib = flake.inputs.nixpkgs.lib;
        hardware = enabled: (lib.nixosSystem {
          system = "x86_64-linux";
          specialArgs = { self = flake; };
          modules = [
            ./nix/hosts/hardware-main/configuration.nix
            ({ lib, ... }: { loom.filesystemCutoverEnabled = lib.mkForce enabled; })
          ];
        }).config;
        render = cfg: {
          cutover = cfg.loom.filesystemCutoverEnabled;
          boxPath = cfg.loom.boxPath;
          boxStateRoot = cfg.loom.boxStateRoot;
          storageRoot = cfg.loom.storageRoot;
          importsRoot = cfg.loom.importsRoot;
          userBackupsRoot = cfg.loom.userBackupsRoot;
          canonicalUserBackupsRoot = cfg.loom.canonicalUserBackupsRoot;
          archiveRoot = cfg.loom.archiveRoot;
          notesProjectionRoot = cfg.loom.notesProjectionRoot;
          importsBackupPolicy = cfg.loom.importsBackupPolicy;
          serviceEnvironment = cfg.environment.etc."loom/loom.env".text;
          boxSharePath = cfg.services.samba.settings."loom-main-box".path;
          boxReadOnly = cfg.services.samba.settings."loom-main-box"."read only";
          storageSharePath = cfg.services.samba.settings."loom-storage".path;
          storageReadOnly = cfg.services.samba.settings."loom-storage"."read only";
          bindOnly = cfg.services.samba.settings.global."bind interfaces only";
          interfaces = cfg.services.samba.settings.global.interfaces;
          guestMapping = cfg.services.samba.settings.global."map to guest";
          minimumProtocol = cfg.services.samba.settings.global."server min protocol";
        };
      in {
        preCutover = render (hardware false);
        postCutover = render (hardware true);
        devHost = flake.nixosConfigurations.dev-utm.config.networking.hostName;
        placeholderHost = flake.nixosConfigurations.production-placeholder.config.networking.hostName;
      }
    ' >"$rendered"
  )
  jq -e '
    .devHost == "loom-dev" and
    .placeholderHost == "loom-main-placeholder" and
    .preCutover.cutover == false and
    .preCutover.boxPath == "/home/loomadmin/loom-box" and
    .preCutover.boxStateRoot == "/home/loomadmin/loom-box/.loom/state" and
    .preCutover.storageRoot == "/srv/loom/storage" and
    .preCutover.importsRoot == "/var/lib/loom/lane/accepted" and
    .preCutover.userBackupsRoot == "/var/lib/loom/private-backups" and
    .preCutover.canonicalUserBackupsRoot == "/srv/loom/storage/backups" and
    .preCutover.archiveRoot == "/var/lib/loom/storage-archive" and
    .preCutover.notesProjectionRoot == "/var/lib/loom/loom-notes" and
    .preCutover.boxSharePath == "/home/loomadmin/loom-box" and
    .preCutover.storageSharePath == "/var/lib/loom/storage-views/main-export" and
    (.preCutover.serviceEnvironment | contains("LOOM_LEGACY_SPLIT_ROOTS=true")) and
    .postCutover.cutover == true and
    .postCutover.boxPath == "/srv/loom/box" and
    .postCutover.boxStateRoot == "/var/lib/loom/box-state" and
    .postCutover.storageRoot == "/srv/loom/storage" and
    .postCutover.importsRoot == "/srv/loom/storage/imports" and
    .postCutover.userBackupsRoot == "/srv/loom/storage/backups" and
    .postCutover.canonicalUserBackupsRoot == "/srv/loom/storage/backups" and
    .postCutover.archiveRoot == "/srv/loom/storage/archive" and
    .postCutover.notesProjectionRoot == "/var/lib/loom/generated/notes" and
    .postCutover.boxSharePath == "/srv/loom/box" and
    .postCutover.storageSharePath == "/srv/loom/storage" and
    (.postCutover.serviceEnvironment | contains("LOOM_LEGACY_SPLIT_ROOTS=false")) and
    .preCutover.importsBackupPolicy == "legacy_complete_physical_custody" and
    .postCutover.importsBackupPolicy == "legacy_complete_physical_custody" and
    .preCutover.boxReadOnly == "no" and
    .postCutover.boxReadOnly == "no" and
    .preCutover.storageReadOnly == "yes" and
    .postCutover.storageReadOnly == "yes" and
    .preCutover.bindOnly == "yes" and
    .postCutover.bindOnly == "yes" and
    .preCutover.interfaces == "10.44.0.2/24" and
    .postCutover.interfaces == "10.44.0.2/24" and
    .preCutover.guestMapping == "Never" and
    .postCutover.guestMapping == "Never" and
    .preCutover.minimumProtocol == "SMB3" and
    .postCutover.minimumProtocol == "SMB3"
  ' "$rendered" >/dev/null
}

start_database

log 'applying all migrations to an isolated PostgreSQL fixture'
(
  cd "$ROOT"
  LOOM_BOOTSTRAP_TEST_DB_URL="$SMOKE_DB_URL" \
    go test ./internal/bootstrap -run '^TestEnsureProductionBootstrapIntegration$' -count=1
)
pass 'fresh database migration and production bootstrap contract'

log 'proving temp-root Box setup and external runtime-state separation'
run_box_fixture
run_go_test_without_database go test ./internal/filesystemlayout ./internal/config ./internal/setup ./internal/box -count=1
pass 'canonical layout, setup, compatibility dry-run, and Box state separation'

log 'proving both Lane transports promote into canonical Imports custody'
run_go_test_without_database go test ./internal/lane ./internal/mainstorage -count=1
pass 'Lane file-tree/bundle promotion, repair, and canonical Documents import'

log 'proving Notes source identity, projection, indexing, and search'
run_go_test go test ./internal/knowledge ./internal/notesprojection -count=1
pass 'Notes source/index/search and generated projection contracts'

log 'proving direct and chunked backups, archive custody, backup, and restore evidence'
run_go_test_without_database go test \
  ./internal/sync \
  ./internal/watchedroots \
  ./internal/filetransfer \
  ./internal/storagearchive \
  ./internal/backupcoverage \
  ./internal/backup \
  ./internal/maintenance \
  ./internal/workers/runtimes \
  -count=1
pass 'canonical backup/archive custody and portable verification'

log 'proving safe-delete, fidelity, migration apply/verify/rollback, and catalog transactions'
run_go_test_without_database go test \
  ./internal/storagemigration \
  ./internal/storagecatalog \
  ./internal/storagecleanup \
  ./internal/storagefidelity \
  ./internal/storagedoctor \
  -count=1
pass 'reviewed migration, rollback, safe-delete, fidelity, and typed doctor status'

log 'proving typed status API, CLI, and Portal reachability without generated exports'
run_go_test_without_database go test ./internal/httpapi ./internal/localclient ./internal/loomcli ./internal/loomcli/portal -count=1
pass 'bounded API/CLI/Portal status and retained compatibility diagnostics'

log 'rendering the atomic pre/post-cutover writer and private two-share SMB contracts without activation'
render_smb_contract
run_go_test_without_database go test ./internal/config -run 'TestNixSmbModuleDefinesPrivateCanonicalShares|TestNixProfilesWireSmbForHardwareMainAndDevSmoke' -count=1
pass 'rendered dev/placeholder plus atomic pre/post-cutover roots and SMB configuration'

printf '[canonical-filesystem] local acceptance passed (%d groups; temp roots and database only)\n' "$PASS_COUNT"

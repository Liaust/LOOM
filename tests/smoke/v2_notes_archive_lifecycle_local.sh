#!/usr/bin/env bash
set -euo pipefail

[[ "$#" == 0 ]] || { printf 'Usage: %s\n' "$0" >&2; exit 1; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_ROOT="$(mktemp -d /tmp/loom-notes-archive-XXXXXX)"
PG_STARTED=false
PG_BIN=""
cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ "$PG_STARTED" == true ]]; then
    if ! "$PG_BIN/pg_ctl" -D "$TMP_ROOT/postgres" -m fast -w stop >/dev/null; then
      printf 'FAIL: disposable PostgreSQL did not stop; retained %s\n' "$TMP_ROOT" >&2
      exit 1
    fi
  fi
  case "$TMP_ROOT" in
    /tmp/loom-notes-archive-*)
      find -P "$TMP_ROOT" -type d -exec chmod u+w {} +
      rm -rf -- "$TMP_ROOT"
      [[ ! -e "$TMP_ROOT" ]] || exit 1
      ;;
    *) exit 1 ;;
  esac
  printf 'Disposable Notes acceptance cluster and owned fixture root removed.\n'
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cd "$ROOT"

NIX="$(command -v nix || true)"
if [[ -z "$NIX" && -x /nix/var/nix/profiles/default/bin/nix ]]; then
  NIX=/nix/var/nix/profiles/default/bin/nix
fi
[[ -n "$NIX" ]] || { printf 'Pinned disposable PostgreSQL requires Nix.\n' >&2; exit 1; }
PG_ROOT="$("$NIX" --extra-experimental-features 'nix-command flakes' build --no-link --print-out-paths --impure --expr \
  'let f = (import ./tests/nix/source-flake.nix {}); in f.inputs.nixpkgs.legacyPackages.${builtins.currentSystem}.postgresql_17.withPackages (p: [ p.pgvector ])')"
PDF_ROOT="$("$NIX" --extra-experimental-features 'nix-command flakes' build --no-link --print-out-paths --impure --expr \
  'let f = (import ./tests/nix/source-flake.nix {}); in f.inputs.nixpkgs.legacyPackages.${builtins.currentSystem}.poppler-utils')"
PG_BIN="$PG_ROOT/bin"
export PATH="$PDF_ROOT/bin:$PG_BIN:$PATH"
mkdir -m 0700 "$TMP_ROOT/socket" "$TMP_ROOT/tests"
"$PG_BIN/initdb" -D "$TMP_ROOT/postgres" --username=postgres --auth-local=trust \
  --auth-host=reject --encoding=UTF8 --no-locale >/dev/null
"$PG_BIN/pg_ctl" -D "$TMP_ROOT/postgres" -l "$TMP_ROOT/postgres.log" \
  -o "-c listen_addresses='' -c unix_socket_directories='$TMP_ROOT/socket' -c unix_socket_permissions=0700" -w start >/dev/null
PG_STARTED=true
export LOOM_TEST_DB_URL="postgres://postgres@/postgres?host=$TMP_ROOT/socket&sslmode=disable"
# Go fixtures and the guarded API helper own all sources; no daemon/supervisor
# schedule, Main endpoint, real source root or external model is used.
export TMPDIR="$TMP_ROOT/tests"
export LOOM_BOX_SURFACE_HELPER="$TMP_ROOT/knowledge-http.test"
export LOOM_BOX_SURFACE_CLI="$TMP_ROOT/loom"
go test -c -o "$LOOM_BOX_SURFACE_HELPER" ./internal/httpapi
go build -o "$LOOM_BOX_SURFACE_CLI" ./cmd/loom

CHECK=0
gate() {
  local title="$1"
  shift
  CHECK=$((CHECK + 1))
  if ! "$@" >"$TMP_ROOT/gate-$CHECK.log" 2>&1; then
    tail -80 "$TMP_ROOT/gate-$CHECK.log" >&2
    printf 'FAIL [%s] %s\n' "$CHECK" "$title" >&2
    exit 1
  fi
  if grep -q -- '--- SKIP:' "$TMP_ROOT/gate-$CHECK.log"; then
    printf 'FAIL: required acceptance skipped in %s\n' "$title" >&2
    exit 1
  fi
  printf 'PASS [%s] %s\n' "$CHECK" "$title"
  sed -n '/five-object public lexical\/all search:/p' "$TMP_ROOT/gate-$CHECK.log"
}
gate 'Frozen source/lifecycle contracts' go test -v -count=1 -run 'Test.*(BoxSourcesContract|ArchiveLifecycle)' ./internal/knowledge
gate 'Real owner/projector/receipt/fence/read/search matrix' go test -v -count=1 \
  -run '^TestNotes(Archive(Projection|Read|Search|Surfaces)|Custody)' ./internal/knowledge
gate 'Public API/CLI, projection lag, process restart and replay' go test -v -count=1 \
  -run '^TestNotesArchiveAcceptance' ./internal/knowledge
gate 'Existing coordinator checkpoint and unattended custody replay' go test -v -count=1 \
  -run '^TestKnowledgeCustody' ./internal/workers/runtimes
gate 'Typed controls and Notes Portal routing/inspect' go test -v -count=1 \
  -run '^TestNotesArchive(Lifecycle|Portal)' ./internal/httpapi ./internal/localclient ./internal/loomcli ./internal/loomcli/portal
gate 'Preserved active Box/PDF and exact passage behavior' go test -v -count=1 \
  -run '^Test(BoxSyncedSearchProjection|BoxSourceVersionSearchObservation|BoxSourcesNativeRetrieval|NotesPassage)' ./internal/knowledge
printf 'Notes archive lifecycle acceptance passed (%s checks).\n' "$CHECK"
